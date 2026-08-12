// Package search 搜索模块
// 语义检索 + 标签过滤 + 结果组装
package search

import (
	"fmt"
	"sort"
	"strings"

	"memora/internal/contract"
	"memora/internal/logx"
)

// ILLM search 模块所需的 llm 接口
type ILLM interface {
	Embed(texts []string) ([][]float32, error)
	EmbedQuery(text string) ([]float32, error)             // 查询嵌入（自动加检索指令前缀）
	Rerank(query string, docs []string) ([]float64, error) // 可选：未配置时返回错误
}

// IStorage search 模块所需的 storage 接口
type IStorage interface {
	ChunksGet(id int64) (*contract.Chunk, error)
	FilesGet(id int64) (*contract.FileInfo, error)
	FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*contract.FileInfo, int, error)
	TagsList() ([]*contract.TagInfo, error)
	FileTagsListByFile(fileID int64) ([]contract.FileTag, error)
	FileTagsListByTag(tagID int64) ([]int64, error)
	VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error)
}

// Module 搜索模块
type Module struct {
	llm     ILLM
	storage IStorage
}

// New 创建搜索模块
func New(llm ILLM, storage IStorage) *Module {
	return &Module{
		llm:     llm,
		storage: storage,
	}
}

// parseTagFilter 从查询中解析 tag:xxx 过滤器
func parseTagFilter(q string) (query string, tags []string) {
	parts := strings.Fields(q)
	var queryParts []string
	for _, p := range parts {
		if strings.HasPrefix(p, "tag:") {
			tag := strings.TrimPrefix(p, "tag:")
			if tag != "" {
				tags = append(tags, tag)
			}
		} else {
			queryParts = append(queryParts, p)
		}
	}
	return strings.Join(queryParts, " "), tags
}

// Query 搜索（§4.10 内部流程）
func (m *Module) Query(q string, tagFilter []string, page int) ([]*contract.SearchResult, int, error) {
	// 从查询中解析 tag:xxx
	queryText, inlineTags := parseTagFilter(q)

	// 合并标签过滤（tagFilter 来自 URL 查询参数，inlineTags 来自 q 中的 tag: 语法）
	allTags := append(tagFilter, inlineTags...)
	if queryText == "" && len(allTags) == 0 {
		return nil, 0, nil
	}

	// 嵌入查询：纯标签查询（queryText 为空）时跳过向量检索，直接从文件表按标签过滤，
	// 避免向嵌入端点发送空串（多数端点对空串返回 400，导致 tag: 搜索必败）。
	if queryText == "" {
		return m.queryByTagsOnly(allTags, page)
	}

	queryVec, err := m.llm.EmbedQuery(queryText)
	if err != nil {
		return nil, 0, fmt.Errorf("[search] 嵌入查询失败: %w", err)
	}

	// 宽召回：向量检索 top-100 分块，避免相关文件排在 20 名后被漏掉
	topK := 100
	entries, err := m.storage.VectorsSearch(queryVec, topK)
	if err != nil {
		return nil, 0, fmt.Errorf("[search] 向量检索失败: %w", err)
	}

	// 按文件分组去重，取组内最高分块
	type fileResult struct {
		file          *contract.FileInfo
		bestChunk     *contract.Chunk
		bestScore     float64
		matchedChunks int
	}

	fileMap := make(map[int64]*fileResult)
	var fileOrder []int64

	for _, entry := range entries {
		chunk, err := m.storage.ChunksGet(entry.ChunkID)
		if err != nil || chunk == nil {
			continue
		}

		if existing, ok := fileMap[chunk.FileID]; ok {
			existing.matchedChunks++
			if entry.Score > existing.bestScore {
				existing.bestScore = entry.Score
				existing.bestChunk = chunk
			}
		} else {
			file, err := m.storage.FilesGet(chunk.FileID)
			if err != nil || file == nil {
				continue
			}
			fileMap[chunk.FileID] = &fileResult{
				file:          file,
				bestChunk:     chunk,
				bestScore:     entry.Score,
				matchedChunks: 1,
			}
			fileOrder = append(fileOrder, chunk.FileID)
		}
	}

	// 重排精排（若已配置）：对每个文件的最高分块用交叉编码器重新打分，
	// 替代余弦分数作为主要排序依据（重排失败时保持原分数，回退现有逻辑）
	if len(fileOrder) > 1 {
		texts := make([]string, len(fileOrder))
		for i, fid := range fileOrder {
			texts[i] = fileMap[fid].bestChunk.Text
		}
		if rs, rerr := m.llm.Rerank(queryText, texts); rerr == nil && len(rs) == len(fileOrder) {
			for i, fid := range fileOrder {
				fileMap[fid].bestScore = rs[i]
			}
		}
	}

	// 标签过滤 + 命中加分：一次性构建「匹配标签 → 文件ID集合」，
	// 避免对每个文件×每个标签重复 FileTagsListByTag 查询（修复审计发现的 N+1 双重遍历）。
	if len(allTags) > 0 {
		tagSet := make(map[string]bool)
		for _, t := range allTags {
			tagSet[t] = true
		}
		allTagsInfo, err := m.storage.TagsList()
		if err != nil {
			// 标签查询失败时返回错误，而不是静默返回未过滤的全部结果（修复 review 发现的不一致行为）
			return nil, 0, fmt.Errorf("[search] 获取标签列表失败: %w", err)
		}
		// 仅查询用户指定的匹配标签，构建 tagID → 文件ID 集合
		tagFileSets := make(map[int64]map[int64]bool)
		for _, ti := range allTagsInfo {
			if !tagSet[ti.Name] {
				continue
			}
			ids, lerr := m.storage.FileTagsListByTag(ti.ID)
			if lerr != nil {
				continue
			}
			set := make(map[int64]bool, len(ids))
			for _, fid := range ids {
				set[fid] = true
			}
			tagFileSets[ti.ID] = set
		}

		var filtered []int64
		for _, fileID := range fileOrder {
			fr := fileMap[fileID]
			hasTag := false
			for _, set := range tagFileSets {
				if set[fileID] {
					hasTag = true
					fr.bestScore += 0.5 // 命中匹配标签加分
				}
			}
			if hasTag {
				filtered = append(filtered, fileID)
			}
		}
		fileOrder = filtered
	}

	// 排序：相关性分（重排或余弦）优先，命中块数其次
	sort.Slice(fileOrder, func(i, j int) bool {
		ri := fileMap[fileOrder[i]]
		rj := fileMap[fileOrder[j]]
		if ri.bestScore != rj.bestScore {
			return ri.bestScore > rj.bestScore
		}
		return ri.matchedChunks > rj.matchedChunks
	})

	// 组装结果（分页）
	pageSize := 20
	if page < 0 {
		page = 0 // 防御：负数 page 会致 start 为负、slice 越界 panic
	}
	// 防溢出：page 极大时 page*pageSize 可能 int 溢出为负（修复 review should-fix）
	if page > len(fileOrder)/pageSize {
		return []*contract.SearchResult{}, len(fileOrder), nil
	}
	start := page * pageSize
	end := start + pageSize
	if end > len(fileOrder) {
		end = len(fileOrder)
	}

	var results []*contract.SearchResult
	for _, fileID := range fileOrder[start:end] {
		fr := fileMap[fileID]
		hitText := truncateMatchText(fr.bestChunk.Text, fr.bestChunk.Seq)
		tags := m.getFileTags(fr.file.ID)
		results = append(results, &contract.SearchResult{
			FileID:        fr.file.ID,
			RelPath:       fr.file.RelPath,
			HitText:       hitText,
			Score:         fr.bestScore,
			Tags:          tags,
			Mtime:         fr.file.Mtime,
			MatchedChunks: fr.matchedChunks,
		})
	}

	// 检索诊断日志：便于排查"找不到文件"——命中文件数与 top 结果
	if len(results) > 0 {
		var top []string
		for i, r := range results {
			if i >= 3 {
				break
			}
			top = append(top, r.RelPath)
		}
		logx.Debug("search", "检索命中", "total", len(fileOrder), "top", strings.Join(top, ","))
	}

	return results, len(fileOrder), nil
}

// queryByTagsOnly 纯标签搜索：不嵌入查询文本，直接列出已索引文件并按标签过滤。
// 多个标签之间为"任一命中"语义，与 Query 内联标签过滤保持一致。
func (m *Module) queryByTagsOnly(allTags []string, page int) ([]*contract.SearchResult, int, error) {
	if len(allTags) == 0 {
		return nil, 0, nil
	}

	files, _, err := m.storage.FilesList("indexed", "", 0, 100000, "time:desc")
	if err != nil {
		return nil, 0, fmt.Errorf("[search] 列出文件失败: %w", err)
	}

	// 构建 tagID → 文件ID 集合（仅匹配用户指定的标签）
	tagSet := make(map[string]bool)
	for _, t := range allTags {
		tagSet[t] = true
	}
	allTagsInfo, err := m.storage.TagsList()
	if err != nil {
		return nil, 0, fmt.Errorf("[search] 获取标签列表失败: %w", err)
	}
	tagFileSets := make(map[int64]map[int64]bool)
	for _, ti := range allTagsInfo {
		if !tagSet[ti.Name] {
			continue
		}
		ids, lerr := m.storage.FileTagsListByTag(ti.ID)
		if lerr != nil {
			continue
		}
		set := make(map[int64]bool, len(ids))
		for _, fid := range ids {
			set[fid] = true
		}
		tagFileSets[ti.ID] = set
	}

	// 过滤：文件命中任一标签
	var matched []*contract.FileInfo
	for _, f := range files {
		for _, set := range tagFileSets {
			if set[f.ID] {
				matched = append(matched, f)
				break
			}
		}
	}

	// 分页（与 Query 一致：pageSize=20）
	pageSize := 20
	if page < 0 {
		page = 0
	}
	if page > len(matched)/pageSize {
		return []*contract.SearchResult{}, len(matched), nil
	}
	start := page * pageSize
	end := start + pageSize
	if end > len(matched) {
		end = len(matched)
	}

	var results []*contract.SearchResult
	for _, f := range matched[start:end] {
		tags := m.getFileTags(f.ID)
		results = append(results, &contract.SearchResult{
			FileID:        f.ID,
			RelPath:       f.RelPath,
			Score:         1.0, // 纯标签命中，无相似度分数
			Tags:          tags,
			Mtime:         f.Mtime,
			MatchedChunks: 0,
		})
	}
	return results, len(matched), nil
}

// getFileTags 获取文件的标签列表（从 file_tags 表取 origin）
func (m *Module) getFileTags(fileID int64) []contract.FileTag {
	tags, err := m.storage.FileTagsListByFile(fileID)
	if err != nil {
		return nil
	}
	return tags
}

// truncateMatchText 截取匹配文本（保留关键部分）
func truncateMatchText(text string, seq int) string {
	// 简单截取前 300 个字符作为摘要
	runes := []rune(text)
	if len(runes) <= 300 {
		return text
	}
	return string(runes[:300]) + "..."
}
