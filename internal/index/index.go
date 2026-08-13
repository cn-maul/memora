// Package index 索引模块
// 分块/嵌入/向量写入/增量与全量
package index

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"memora/internal/contract"
	"memora/internal/documentpolicy"
	"memora/internal/logx"
)

// indexConcurrency 索引处理的上界并发数：并行处理含 LLM 的嵌入/打标，缩短全量重建索引耗时（A6）。
const indexConcurrency = 4

// fileJob 单文件索引作业
 type fileJob struct {
	file    *contract.FileInfo
	relPath string
}

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
	FileVectorDim(fileID int64) (int, bool, error)
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
	// embedDim 用原子量承载：构造时注入，运行期可由 SetEmbedDim 刷新（如用户改了
	// embed.dimensions 配置触发自动重建索引）。FullReindex 跑在独立 goroutine，
	// ProcessFile 读取维度也必须走原子读，避免"配置已更新但维度仍取旧值"导致幂等跳过失效。
	embedDim atomic.Int64
	mu       sync.Mutex
}

// New 创建索引模块
func New(storage IStorage, extract IExtract, llm ILLM, events IEvents, workspace string, chunkSize, chunkOverlap, embedDim int) *Module {
	m := &Module{
		storage:      storage,
		extract:      extract,
		llm:          llm,
		events:       events,
		workspace:    workspace,
		chunkSize:    chunkSize,
		chunkOverlap: chunkOverlap,
	}
	m.embedDim.Store(int64(embedDim))
	return m
}

// SetEmbedDim 运行期刷新嵌入维度（响应 embed.dimensions 配置变更）。
// 调用方随后应触发 FullReindex，让存量文件按新维度重嵌入；否则查询侧维度与存量
// 向量不匹配，cosine 全 0、检索恒为空。
func (m *Module) SetEmbedDim(dim int64) {
	m.embedDim.Store(dim)
	logx.Info("index", "嵌入维度已刷新", "dim", dim)
}

// embedDimGet 原子读取当前嵌入维度
func (m *Module) embedDimGet() int64 {
	return m.embedDim.Load()
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

func (m *Module) processJobsConcurrent(jobs []fileJob, errs *[]error) {
	const concurrency = indexConcurrency
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, j := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func(job fileJob) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := m.ProcessFile(job.file); err != nil {
				mu.Lock()
				*errs = append(*errs, fmt.Errorf("[index] 处理文件失败 %s: %w", job.relPath, err))
				mu.Unlock()
			}
		}(j)
	}
	wg.Wait()
}

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

	var errs []error

	jobs := make([]fileJob, 0, total)
	for _, relPath := range files {
		docType := documentpolicy.DetectDocType(relPath)
		if docType == "" || docType == "ignored" {
			continue
		}

		fileInfo := &contract.FileInfo{
			RelPath:     relPath,
			DocType:     docType,
			IndexStatus: "pending",
		}
		if stat, err := os.Stat(filepath.Join(m.workspace, relPath)); err == nil && !stat.IsDir() {
			fileInfo.Size = stat.Size()
			fileInfo.Mtime = stat.ModTime().UnixMilli()
		}
		if existing, err := m.storage.FilesFindByRelPath(relPath); err == nil && existing != nil {
			fileInfo.ContentHash = existing.ContentHash
		}

		id, err := m.storage.FilesUpsert(fileInfo)
		if err != nil {
			errs = append(errs, fmt.Errorf("[index] 写入文件元数据失败 %s: %w", relPath, err))
			continue
		}
		fileInfo.ID = id
		jobs = append(jobs, fileJob{file: fileInfo, relPath: relPath})
	}

	// A6：并行处理含 LLM 的嵌入/打标，缩短全量重建索引耗时
	if len(jobs) > 0 {
		m.processJobsConcurrent(jobs, &errs)
	}

	m.events.Notify("index_progress", map[string]interface{}{
		"phase": "done",
		"total": total,
	})

	// 清理幽灵索引：磁盘上已不存在的文件，从索引中移除其 chunks/vectors
	// （修复审计发现：FullReindex 仅 upsert 现有文件，若 watch 漏删则旧向量仍参与检索）
	if err := m.cleanupMissingFiles(files); err != nil {
		errs = append(errs, fmt.Errorf("[index] 清理已删除文件索引失败: %w", err))
	}

	if len(errs) > 0 {
		logx.Warn("index", "全量重建索引存在失败项", "count", len(errs))
		return errors.Join(errs...)
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

	var errs []error
	removed := 0
	for _, f := range dbFiles {
		// ignored（含已标记删除的文件）无需处理
		if f.IndexStatus == "ignored" {
			continue
		}
		if onDiskSet[f.RelPath] {
			continue
		}
		// P1-15：收集清理失败而非仅 logx.Warn，避免伪成功
		if err := m.DeleteFile(f.RelPath); err != nil {
			errs = append(errs, fmt.Errorf("[index] 清理幽灵文件失败 %s: %w", f.RelPath, err))
			continue
		}
		removed++
	}
	if removed > 0 {
		logx.Info("index", "已清理已删除文件的索引", "count", removed)
	}
	return errors.Join(errs...)
}

// Incremental 增量索引
// P1-02 修复：不再忽略 ProcessFile/DeleteFile 的返回错误。推进所有文件后汇总错误，
// 若有任何失败返回聚合错误（errors.Join），单个文件失败不中断其余文件的处理。
func (m *Module) Incremental(changed, removed []string) error {
	var errs []error

	// 先做元数据 upsert，收集作业；再并行处理（A6）
	jobs := make([]fileJob, 0, len(changed))
	for _, relPath := range changed {
		docType := documentpolicy.DetectDocType(relPath)
		if docType == "" || docType == "ignored" {
			continue
		}

		fullPath := filepath.Join(m.workspace, relPath)
		stat, err := os.Stat(fullPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("[index] 增量索引文件 %s 状态检查失败: %w", relPath, err))
			continue
		}
		if stat.IsDir() {
			continue
		}

		fileInfo, err := m.storage.FilesFindByRelPath(relPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("[index] 增量索引文件 %s 查找失败: %w", relPath, err))
			continue
		}

		if fileInfo == nil {
			fileInfo = &contract.FileInfo{
				RelPath:     relPath,
				DocType:     docType,
				Size:        stat.Size(),
				Mtime:       stat.ModTime().UnixMilli(),
				IndexStatus: "pending",
			}
			id, err := m.storage.FilesUpsert(fileInfo)
			if err != nil {
				errs = append(errs, fmt.Errorf("[index] 增量索引新文件 %s 插入失败: %w", relPath, err))
				continue
			}
			fileInfo.ID = id
		} else {
			fileInfo.Size = stat.Size()
			fileInfo.Mtime = stat.ModTime().UnixMilli()
			id, err := m.storage.FilesUpsert(fileInfo)
			if err != nil {
				errs = append(errs, fmt.Errorf("[index] 增量索引文件 %s 元数据更新失败: %w", relPath, err))
				continue
			}
			fileInfo.ID = id
		}

		jobs = append(jobs, fileJob{file: fileInfo, relPath: relPath})
	}

	if len(jobs) > 0 {
		m.processJobsConcurrent(jobs, &errs)
	}

	// 处理删除的文件
	for _, relPath := range removed {
		if err := m.DeleteFile(relPath); err != nil {
			errs = append(errs, fmt.Errorf("[index] 增量索引文件 %s 删除失败: %w", relPath, err))
		}
	}

	return errors.Join(errs...)
}

// ProcessFile 处理单个文件（核心流程，§4.8 内部流程）
func (m *Module) ProcessFile(file *contract.FileInfo) error {
	// Step 1: 置 extracting
	m.storage.FilesMarkStatus(file.ID, "extracting", "")

	// 最终路径 containment：跟随 symlink/junction 解析，拒绝越出工作区的路径（P1-16）
	fullPath, err := documentpolicy.FinalPath(m.workspace, file.RelPath)
	if err != nil {
		errMsg := err.Error()
		m.storage.FilesMarkStatus(file.ID, "failed", errMsg)
		m.events.Notify("extract_failed", map[string]interface{}{
			"relPath": file.RelPath,
			"error":   errMsg,
		})
		return fmt.Errorf("[index] 提取失败: %w", err)
	}

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
	// 额外校验向量维度：切换嵌入模型后维度变化（如 bge 1024 → Qwen 4096），
	// 旧向量与查询维度不匹配导致检索恒为空，必须强制重新嵌入（修复）。
	if file.ContentHash == cacheKey {
		skip := false
		if existingChunks, cerr := m.storage.ChunksByFile(file.ID); cerr == nil && len(existingChunks) > 0 {
			dim, hasVec, derr := m.storage.FileVectorDim(file.ID)
			if derr == nil && hasVec && int(dim) == int(m.embedDimGet()) {
				skip = true
			} else {
				logx.Info("index", "幂等跳过失效（维度变化），重新索引", "relPath", file.RelPath, "storedDim", dim, "expectDim", int(m.embedDimGet()), "hasVec", hasVec)
			}
		} else {
			// hash 相同但无分块：上次索引未完成，继续走完整流程以自愈
			logx.Info("index", "幂等跳过失效，重新索引", "relPath", file.RelPath)
		}
		if skip {
			m.storage.FilesMarkStatus(file.ID, "indexed", "")
			return nil
		}
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

	// 内容为空：清除该文件的旧索引（chunks/vectors），保持"已索引但为空"的明确状态。
	// P1-01 修复：此前只标记 indexed 直接返回，若文件内容被清空，旧 chunks/vectors 仍
	// 残留于库中参与检索，导致已删除/清空的内容仍被搜索到。
	if len(chunks) == 0 {
		if err := m.storage.ChunksReplaceForFile(file.ID, nil); err != nil {
			errMsg := fmt.Sprintf("清空分块失败: %v", err)
			m.storage.FilesMarkStatus(file.ID, "failed", errMsg)
			return fmt.Errorf("[index] %s: %w", errMsg, err)
		}
		// 空内容也视为已索引状态：写回 content_hash，避免下次重复全量处理
		now := time.Now().UnixMilli()
		file.ContentHash = cacheKey
		file.LastIndexedAt = now
		if _, err := m.storage.FilesUpsert(file); err != nil {
			errMsg := fmt.Sprintf("写回文件元数据失败: %v", err)
			m.storage.FilesMarkStatus(file.ID, "failed", errMsg)
			return fmt.Errorf("[index] %s: %w", errMsg, err)
		}
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
	wantDim := int(m.embedDimGet())
	for i, vec := range allVectors {
		if len(vec) != wantDim {
			errMsg := fmt.Sprintf("向量维度不匹配: 期望 %d, 实际 %d (第 %d 块)", wantDim, len(vec), i+1)
			m.storage.FilesMarkStatus(file.ID, "failed", errMsg)
			// 控制台同步输出：排查"找不到文件"的关键日志（sources=[] 时查文件是否 failed）
			logx.Error("index", "向量维度不匹配，文件标记失败", "relPath", file.RelPath, "expect", wantDim, "actual", len(vec))
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

	// 向量基数校验：向量数量必须与分块数量严格一致（P1-15 修复）。
	// 此前 `if i < len(allVectors)` 会静默跳过多出的 dbChunk / 缺失的向量，
	// 导致部分分块无向量、检索漏命中而查询方无从感知。
	if len(allVectors) != len(dbChunks) {
		errMsg := fmt.Sprintf("向量基数不匹配: 分块 %d 个, 向量 %d 个", len(dbChunks), len(allVectors))
		m.storage.FilesMarkStatus(file.ID, "failed", errMsg)
		logx.Error("index", "向量基数不匹配，文件标记失败", "relPath", file.RelPath, "chunks", len(dbChunks), "vectors", len(allVectors))
		return fmt.Errorf("[index] %s", errMsg)
	}

	for i, dbChunk := range dbChunks {
		if err := m.storage.VectorsInsert(dbChunk.ID, allVectors[i], int(m.embedDimGet())); err != nil {
			m.storage.FilesMarkStatus(file.ID, "failed", fmt.Sprintf("写入向量失败: %v", err))
			return fmt.Errorf("[index] 写入向量失败: %w", err)
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

// ──────────────────── 辅助函数 ────────────────────

// scanWorkspaceFiles 扫描工作目录下的所有支持文件
func (m *Module) scanWorkspaceFiles() ([]string, error) {
	var files []string
	err := filepath.Walk(m.workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// 忽略隐藏目录、.git/.memora 及重型/非文档目录（统一 documentpolicy 规则）
			if documentpolicy.IsIgnoredDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, _ := filepath.Rel(m.workspace, path)
		docType := documentpolicy.DetectDocType(relPath)
		if docType != "" && docType != "ignored" {
			files = append(files, relPath)
		}
		return nil
	})
	return files, err
}

// isHeavyDirName / detectDocType 已统一迁移到 documentpolicy 包：
// IsIgnoredDir / DetectDocType。避免各模块重复维护一份规则（P2-14）。
