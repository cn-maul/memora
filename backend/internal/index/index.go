// Package index 索引模块
// 分块/嵌入/向量写入/增量与全量
package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"memora/internal/contract"
	"memora/internal/logx"
)

// IStorage index 模块所需的 storage 接口
type IStorage interface {
	FilesGet(id int64) (*contract.FileInfo, error)
	FilesFindByRelPath(relPath string) (*contract.FileInfo, error)
	FilesUpsert(f *contract.FileInfo) (int64, error)
	FilesMarkStatus(id int64, status, lastError string) error
	FilesMarkAllPending() error
	FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*contract.FileInfo, int, error)
	ChunksReplaceForFile(fileID int64, chuns []*contract.Chunk) error
	ChunksByFile(fileID int64) ([]*contract.Chunk, error)
	ChunksGet(id int64) (*contract.Chunk, error)
	VectorsInsert(chunkID int64, vec []float32, dim int) error
	VectorsDelete(chunkID int64) error
	VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error)
	FileTagsReplace(fileID int64, tags []contract.FileTag) error
}

// IExtract index 模块所需的 extract 接口
type IExtract interface {
	ExtractFile(filePath string) (text string, cacheKey string, err error)
}

// ILLM index 模块所需的 llm 接口
type ILLM interface {
	Embed(texts []string) ([][]float32, error)
}

// IEvents index 模块所需的事件接口
type IEvents interface {
	Notify(topic string, data interface{})
}

// Module 索引模块
type Module struct {
	storage      IStorage
	extract      IExtract
	llm          ILLM
	events       IEvents
	workspace    string
	chunkSize    int
	chunkOverlap int
	embedDim     int
	mu           sync.Mutex
}

// New 创建索引模块
func New(storage IStorage, extract IExtract, llm ILLM, events IEvents, workspace string, chunkSize, chunkOverlap, embedDim int) *Module {
	return &Module{
		storage:      storage,
		extract:      extract,
		llm:          llm,
		events:       events,
		workspace:    workspace,
		chunkSize:    chunkSize,
		chunkOverlap: chunkOverlap,
		embedDim:     embedDim,
	}
}

// ──────────────────── 分块算法（附录 C.4）────────────────────

// splitIntoParagraphs 按空行分割段落
func splitIntoParagraphs(text string) []string {
	lines := strings.Split(text, "\n")
	var paragraphs []string
	var current strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// 空行 = 段落边界
			if current.Len() > 0 {
				paragraphs = append(paragraphs, current.String())
				current.Reset()
			}
		} else {
			if current.Len() > 0 {
				current.WriteString("\n")
			}
			current.WriteString(line)
		}
	}
	if current.Len() > 0 {
		paragraphs = append(paragraphs, current.String())
	}
	return paragraphs
}

// splitLongParagraph 对 rune 数超过 maxRunes 的段落按句末标点分句后重新组段
func splitLongParagraph(para string, maxRunes int) []string {
	runes := []rune(para)
	if len(runes) <= maxRunes {
		return []string{para}
	}

	// 按句末标点分句
	sentences := splitSentences(para)
	var result []string
	var buf strings.Builder
	bufRunes := 0

	for _, s := range sentences {
		sRunes := utf8.RuneCountInString(s)
		if bufRunes+sRunes > maxRunes && buf.Len() > 0 {
			result = append(result, buf.String())
			buf.Reset()
			bufRunes = 0
		}
		if buf.Len() > 0 {
			buf.WriteString(" ")
			bufRunes++
		}
		buf.WriteString(s)
		bufRunes += sRunes
	}
	if buf.Len() > 0 {
		result = append(result, buf.String())
	}
	return result
}

// splitSentences 按中英文句末标点分句。
// 英文标点（.!?;）需后跟空白/行尾/引号类字符才断句，
// 避免 "3.14"、"v1.2"、"e.g." 这类句中句点被误切。
func splitSentences(text string) []string {
	runes := []rune(text)
	var sentences []string
	var buf strings.Builder
	for i, r := range runes {
		buf.WriteRune(r)

		boundary := false
		switch r {
		case '。', '！', '？', '；':
			boundary = true
		case '.', '!', '?', ';':
			var next rune
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			switch next {
			case 0, ' ', '\t', '\n', '\r', '"', '\'', '”', '’', '」', '）', ')', '】':
				boundary = true
			}
		}

		if boundary {
			sentences = append(sentences, buf.String())
			buf.Reset()
		}
	}
	if buf.Len() > 0 {
		sentences = append(sentences, buf.String())
	}
	return sentences
}

// isMarkdownHeading 判断段落是否为 Markdown 标题（1-6 级 "# " 开头）
func isMarkdownHeading(para string) bool {
	count := 0
	for _, r := range para {
		if r == '#' {
			count++
			if count > 6 {
				return false
			}
			continue
		}
		return count > 0 && r == ' '
	}
	return false
}

// tailSentenceOverlap 取上一块末尾的 overlap runes，并回退到最近的句子边界，
// 保证 overlap 从完整句子开始而非从句子中间截断，避免语义割裂。
func tailSentenceOverlap(lastChunk string, overlap int) string {
	runes := []rune(lastChunk)
	if len(runes) <= overlap {
		return lastChunk
	}

	tail := runes[len(runes)-overlap:]

	// 从尾部向前找最近的非末尾句子边界；若边界恰在末尾（无后续内容），
	// 继续向前找真正的分句点，从而返回最后一个完整句子。
	// 长 tail 下界为 1/3 处（防止 overlap 过短）；短 tail 允许回退到任意位置。
	minKeep := len(tail) / 3
	if len(tail) < 12 {
		minKeep = 1
	}
	for i := len(tail) - 1; i >= minKeep; i-- {
		switch tail[i] {
		case '。', '！', '？', '；', '.', '!', '?', ';', '\n':
			if i == len(tail)-1 {
				continue
			}
			return string(tail[i+1:])
		}
	}
	return string(tail)
}

// chunkText 将文本按 rune 数分块（附录 C.4 算法）
// 增强：Markdown 标题感知（标题强制开新块，保证标题与正文同块）；
// overlap 按句子边界回退，避免语义割裂。
func (m *Module) chunkText(text string) []string {
	// Step 1: 按空行分段落
	paragraphs := splitIntoParagraphs(text)

	// Step 2: 拆分长段落（上限 = 2×chunkSize，跟随配置而非硬编码）
	var allParas []string
	for _, p := range paragraphs {
		parts := splitLongParagraph(p, m.chunkSize*2)
		allParas = append(allParas, parts...)
	}

	// Step 3: 贪心合并
	var chunks []string
	var current strings.Builder
	currentRunes := 0

	// flushAndStart 结束当前块并以其尾部 overlap 为新块前缀，再写入 para
	flushAndStart := func(para string) {
		if current.Len() > 0 {
			chunks = append(chunks, current.String())
		}

		// 新块前缀 = 上一块末尾 overlap（句子级回退）
		overlap := ""
		if len(chunks) > 0 && m.chunkOverlap > 0 {
			overlap = tailSentenceOverlap(chunks[len(chunks)-1], m.chunkOverlap)
		}

		current.Reset()
		current.WriteString(overlap)
		if overlap != "" {
			current.WriteString("\n\n")
		}
		current.WriteString(para)
		currentRunes = utf8.RuneCountInString(current.String())
	}

	for _, para := range allParas {
		paraRunes := utf8.RuneCountInString(para)

		// Markdown 标题：强制作为新块开头，让标题与其正文处于同一嵌入块
		if isMarkdownHeading(para) && current.Len() > 0 {
			flushAndStart(para)
			continue
		}

		if currentRunes+paraRunes <= m.chunkSize+m.chunkSize/10 { // 累计达 chunkSize（±10%）
			if current.Len() > 0 {
				current.WriteString("\n\n")
			}
			current.WriteString(para)
			currentRunes += paraRunes
		} else {
			flushAndStart(para)
		}
	}

	// Step 5: 文件尾不足 200 runes 的零头并入上一块
	last := current.String()
	if utf8.RuneCountInString(last) < 200 && len(chunks) > 0 {
		chunks[len(chunks)-1] = chunks[len(chunks)-1] + "\n\n" + last
	} else if last != "" {
		chunks = append(chunks, last)
	}

	return chunks
}

// ──────────────────── 核心流程 ────────────────────

// FullReindex 全量重建索引
func (m *Module) FullReindex() error {
	logx.Info("index", "开始全量重建索引")

	// 遍历工作目录下的所有文件
	files, err := m.scanWorkspaceFiles()
	if err != nil {
		return fmt.Errorf("[index] 扫描工作目录失败: %w", err)
	}

	total := len(files)
	logx.Info("index", "发现待索引文件", "total", total)

	// 重建开始：先将所有文件状态重置为 pending（让重建过程在列表中可见）
	if err := m.storage.FilesMarkAllPending(); err != nil {
		logx.Warn("index", "重置文件状态失败", "err", err.Error())
	}
	m.events.Notify("index_progress", map[string]interface{}{
		"phase": "reset",
		"total": total,
	})

	for i, relPath := range files {
		// 检查文档类型
		docType := detectDocType(relPath)
		if docType == "" || docType == "ignored" {
			continue
		}

		fileInfo := &contract.FileInfo{
			RelPath:     relPath,
			DocType:     docType,
			IndexStatus: "pending",
		}
		// 扫描时补充文件元数据（修复全量重建后大小恒为 0B 的问题）
		if stat, err := os.Stat(filepath.Join(m.workspace, relPath)); err == nil && !stat.IsDir() {
			fileInfo.Size = stat.Size()
			fileInfo.Mtime = stat.ModTime().UnixMilli()
		}
		// 复用已有 ContentHash：FilesUpsert 在 hash 为空时保留库中旧值，
		// 但 fileInfo 结构体本身未回填，导致 ProcessFile 的幂等检查
		// `file.ContentHash == cacheKey` 恒 false、每次全量重建都全量重提取+重嵌入（修复 review 发现）。
		if existing, err := m.storage.FilesFindByRelPath(relPath); err == nil && existing != nil {
			fileInfo.ContentHash = existing.ContentHash
		}

		// 写入文件元数据
		id, err := m.storage.FilesUpsert(fileInfo)
		if err != nil {
			logx.Warn("index", "写入文件元数据失败", "relPath", relPath, "err", err.Error())
			continue
		}
		fileInfo.ID = id

		// 处理单个文件
		if err := m.ProcessFile(fileInfo); err != nil {
			logx.Warn("index", "处理文件失败", "relPath", relPath, "err", err.Error())
		}

		// 广播进度
		m.events.Notify("index_progress", map[string]interface{}{
			"phase":   "processing",
			"done":    i + 1,
			"total":   total,
			"current": relPath,
		})
	}

	// 重建完成
	m.events.Notify("index_progress", map[string]interface{}{
		"phase": "done",
		"total": total,
	})

	// 清理幽灵索引：磁盘上已不存在的文件，从索引中移除其 chunks/vectors
	// （修复审计发现：FullReindex 仅 upsert 现有文件，若 watch 漏删则旧向量仍参与检索）
	if err := m.cleanupMissingFiles(files); err != nil {
		logx.Warn("index", "清理已删除文件索引警告", "err", err.Error())
	}

	logx.Info("index", "全量重建索引完成")
	return nil
}

// cleanupMissingFiles 清理数据库中存在但磁盘上已不存在的文件索引。
// onDisk 为本次全量扫描发现的文件相对路径集合。
func (m *Module) cleanupMissingFiles(onDisk []string) error {
	onDiskSet := make(map[string]bool, len(onDisk))
	for _, p := range onDisk {
		onDiskSet[p] = true
	}

	dbFiles, _, err := m.storage.FilesList("", "", 0, 100000, "time:asc")
	if err != nil {
		return err
	}

	removed := 0
	for _, f := range dbFiles {
		// ignored（含已标记删除的文件）无需处理
		if f.IndexStatus == "ignored" {
			continue
		}
		if onDiskSet[f.RelPath] {
			continue
		}
		if err := m.DeleteFile(f.RelPath); err != nil {
			logx.Warn("index", "清理幽灵文件失败", "relPath", f.RelPath, "err", err.Error())
			continue
		}
		removed++
	}
	if removed > 0 {
		logx.Info("index", "已清理已删除文件的索引", "count", removed)
	}
	return nil
}

// Incremental 增量索引
func (m *Module) Incremental(changed, removed []string) error {
	// 处理变更的文件
	for _, relPath := range changed {
		// 防御校验：跳过目录、未知类型及 ignored 类型（修复 H-04）
		docType := detectDocType(relPath)
		if docType == "" || docType == "ignored" {
			continue
		}

		fullPath := filepath.Join(m.workspace, relPath)
		stat, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if stat.IsDir() {
			continue
		}

		fileInfo, err := m.storage.FilesFindByRelPath(relPath)
		if err != nil {
			logx.Warn("index", "查找文件失败", "relPath", relPath, "err", err.Error())
			continue
		}

		if fileInfo == nil {
			// 新文件
			fileInfo = &contract.FileInfo{
				RelPath:     relPath,
				DocType:     docType,
				Size:        stat.Size(),
				Mtime:       stat.ModTime().UnixMilli(),
				IndexStatus: "pending",
			}
			id, err := m.storage.FilesUpsert(fileInfo)
			if err != nil {
				logx.Warn("index", "插入新文件失败", "relPath", relPath, "err", err.Error())
				continue
			}
			fileInfo.ID = id
		} else {
			fileInfo.Size = stat.Size()
			fileInfo.Mtime = stat.ModTime().UnixMilli()
			m.storage.FilesUpsert(fileInfo)
		}

		m.ProcessFile(fileInfo)
	}

	// 处理删除的文件
	for _, relPath := range removed {
		m.DeleteFile(relPath)
	}

	return nil
}

// ProcessFile 处理单个文件（核心流程，§4.8 内部流程）
func (m *Module) ProcessFile(file *contract.FileInfo) error {
	// Step 1: 置 extracting
	m.storage.FilesMarkStatus(file.ID, "extracting", "")

	fullPath := filepath.Join(m.workspace, file.RelPath)

	// Step 2: 提取文本
	text, cacheKey, err := m.extract.ExtractFile(fullPath)
	if err != nil {
		errMsg := err.Error()
		m.storage.FilesMarkStatus(file.ID, "failed", errMsg)
		m.events.Notify("extract_failed", map[string]interface{}{
			"relPath": file.RelPath,
			"error":   errMsg,
		})
		return fmt.Errorf("[index] 提取失败: %w", err)
	}

	// 幂等检查：content_hash 相同且已有分块则跳过。
	// 校验分块存在是为了避免自愈失效：若上次在嵌入/写向量阶段失败（状态 failed，
	// 但 content_hash 已保留旧值），仅凭 hash 相同会错误跳过，缺失的向量永远补不齐。
	if file.ContentHash == cacheKey {
		if existingChunks, cerr := m.storage.ChunksByFile(file.ID); cerr == nil && len(existingChunks) > 0 {
			m.storage.FilesMarkStatus(file.ID, "indexed", "")
			return nil
		}
		// hash 相同但无分块：上次索引未完成，继续走完整流程以自愈
		logx.Info("index", "幂等跳过失效，重新索引", "relPath", file.RelPath)
	}

	// Step 3: 分块
	chunkTexts := m.chunkText(text)
	var chunks []*contract.Chunk
	for seq, ct := range chunkTexts {
		chunks = append(chunks, &contract.Chunk{
			FileID:   file.ID,
			Seq:      seq + 1,
			TokenEst: utf8.RuneCountInString(ct),
			Text:     ct,
		})
	}

	if len(chunks) == 0 {
		m.storage.FilesMarkStatus(file.ID, "indexed", "")
		return nil
	}

	// Step 4: 置 embedding 并调用嵌入
	m.storage.FilesMarkStatus(file.ID, "embedding", "")

	// 分批嵌入（每批 ≤16）
	var allVectors [][]float32
	batchSize := 16
	for i := 0; i < len(chunkTexts); i += batchSize {
		end := i + batchSize
		if end > len(chunkTexts) {
			end = len(chunkTexts)
		}
		batch := chunkTexts[i:end]

		vecs, err := m.llm.Embed(batch)
		if err != nil {
			m.storage.FilesMarkStatus(file.ID, "failed", fmt.Sprintf("嵌入失败: %v", err))
			return fmt.Errorf("[index] 嵌入失败: %w", err)
		}
		allVectors = append(allVectors, vecs...)
	}

	// 校验向量维度
	for i, vec := range allVectors {
		if len(vec) != m.embedDim {
			errMsg := fmt.Sprintf("向量维度不匹配: 期望 %d, 实际 %d (第 %d 块)", m.embedDim, len(vec), i+1)
			m.storage.FilesMarkStatus(file.ID, "failed", errMsg)
			return fmt.Errorf("[index] %s", errMsg)
		}
	}

	// Step 5: 事务写入分块和向量
	if err := m.storage.ChunksReplaceForFile(file.ID, chunks); err != nil {
		m.storage.FilesMarkStatus(file.ID, "failed", fmt.Sprintf("写入分块失败: %v", err))
		return fmt.Errorf("[index] 写入分块失败: %w", err)
	}

	// 重新查询 chunks 获取真实 ID，再写入向量
	dbChunks, err := m.storage.ChunksByFile(file.ID)
	if err != nil {
		m.storage.FilesMarkStatus(file.ID, "failed", fmt.Sprintf("查询分块失败: %v", err))
		return fmt.Errorf("[index] 查询分块失败: %w", err)
	}

	for i, dbChunk := range dbChunks {
		if i < len(allVectors) {
			if err := m.storage.VectorsInsert(dbChunk.ID, allVectors[i], m.embedDim); err != nil {
				m.storage.FilesMarkStatus(file.ID, "failed", fmt.Sprintf("写入向量失败: %v", err))
				return fmt.Errorf("[index] 写入向量失败: %w", err)
			}
		}
	}

	// 更新文件状态和 content_hash（写回库）
	now := time.Now().UnixMilli()
	m.storage.FilesMarkStatus(file.ID, "indexed", "")
	file.ContentHash = cacheKey
	file.LastIndexedAt = now
	// 写回 content_hash 到数据库
	m.storage.FilesUpsert(file)

	// 广播索引进度（完成）
	m.events.Notify("index_progress", map[string]interface{}{
		"done":    true,
		"fileId":  file.ID,
		"relPath": file.RelPath,
	})

	return nil
}

// DeleteFile 删除文件索引
func (m *Module) DeleteFile(relPath string) error {
	file, err := m.storage.FilesFindByRelPath(relPath)
	if err != nil || file == nil {
		return nil
	}

	// 删除分块（级联删除向量）
	if err := m.storage.ChunksReplaceForFile(file.ID, nil); err != nil {
		return fmt.Errorf("[index] 删除文件索引失败: %w", err)
	}

	m.storage.FilesMarkStatus(file.ID, "ignored", "文件已删除")
	return nil
}

// Query 向量查询
// 注意：tagFilter 参数仅用于接口兼容，实际的标签过滤由 search 模块负责。
func (m *Module) Query(vec []float32, topK int, tagFilter []string) ([]contract.SearchResult, error) {
	entries, err := m.storage.VectorsSearch(vec, topK)
	if err != nil {
		return nil, err
	}

	var results []contract.SearchResult
	fileSeen := make(map[int64]bool)
	for _, entry := range entries {
		chunk, err := m.storage.ChunksGet(entry.ChunkID)
		if err != nil || chunk == nil {
			continue
		}
		if fileSeen[chunk.FileID] {
			continue
		}
		file, err := m.storage.FilesGet(chunk.FileID)
		if err != nil || file == nil {
			continue
		}
		results = append(results, contract.SearchResult{
			FileID:  file.ID,
			RelPath: file.RelPath,
			HitText: truncateText(chunk.Text, 200),
			Score:   entry.Score,
			Mtime:   file.Mtime,
		})
		fileSeen[chunk.FileID] = true
		if len(results) >= topK {
			break
		}
	}

	return results, nil
}

// ──────────────────── 辅助函数 ────────────────────

// scanWorkspaceFiles 扫描工作目录下的所有支持文件
func (m *Module) scanWorkspaceFiles() ([]string, error) {
	var files []string
	err := filepath.Walk(m.workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// 忽略隐藏目录、.git/.memora 及重型/非文档目录（与 pollPendingFiles 一致）
			if strings.HasPrefix(info.Name(), ".") || isHeavyDirName(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, _ := filepath.Rel(m.workspace, path)
		docType := detectDocType(relPath)
		if docType != "" && docType != "ignored" {
			files = append(files, relPath)
		}
		return nil
	})
	return files, err
}

// isHeavyDirName 判断是否为应跳过的重型/非文档目录（避免全量扫描 node_modules 等）
func isHeavyDirName(name string) bool {
	switch name {
	case "node_modules", "bin", "obj", "dist", "build", "out", "target", "vendor",
		"__pycache__", ".venv", "venv", ".idea", ".vscode", ".mvn", ".gradle":
		return true
	}
	return false
}

// detectDocType 检测文档类型
func detectDocType(relPath string) string {
	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	case ".pptx":
		return "pptx"
	case ".xlsx":
		return "xlsx"
	case ".txt":
		return "txt"
	case ".md":
		return "md"
	case ".doc":
		return "ignored" // D39：不支持 doc，提示另存 docx
	default:
		return ""
	}
}

// truncateText 截断文本（按 rune）
func truncateText(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	// 截取并加上省略号
	half := maxRunes / 2
	return string(runes[:half]) + "..." + string(runes[len(runes)-half:])
}
