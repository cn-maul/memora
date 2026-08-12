//go:build ignore

// gen_data.go — Phase 5 基准数据生成器。
//
// 生成 500 / 5000 文件、10k / 50k chunks 量级的工作区 Markdown 数据：
//   - 500 文件 × 20 块/文件 ≈ 10k chunks（默认 -paras 20）
//   - 5000 文件 × 10 块/文件 ≈ 50k chunks
//   - 内容为随机英文/中文段落，可含标题、列表、代码块，近似真实文档。
//
// 使用固定随机种子（默认 42，-seed 覆盖），保证数据可复现。
// 仅依赖 Go 标准库，不引用 memora 内部包。
//
// 用法（在 benchmarks/ 下）：
//
//	go run gen_data.go -files 500  -paras 20 -dir ./benchdata -seed 42
//	go run gen_data.go -files 5000 -paras 10 -dir ./benchdata -seed 42
//
// 说明：本文件带 //go:build ignore 标记，因此不会参与 `go build ./...` /
// `go test ./...`（benchmarks 目录被当作纯测试包）。按文件名显式运行
// `go run gen_data.go` 时构建约束被忽略，可正常执行。

package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---- 内容素材（中英混合，保证文件之间有差异且可读）----

var enSubjects = []string{
	"The indexer", "The search service", "The embed client", "The reconciler",
	"The storage layer", "The SSE gateway", "The query planner", "The cache layer",
	"The migration runner", "The workspace scanner", "The health probe",
	"The checkpoint writer",
}

var enVerbs = []string{
	"merges", "batches", "streams", "normalizes", "deduplicates", "persists",
	"evicts", "retries", "throttles", "compacts", "snapshots", "reconciles",
	"schedules", "refreshes",
}

var enObjects = []string{
	"candidate chunks", "metadata rows", "embedding batches", "pending events",
	"stale snapshots", "cold cache entries", "incremental diffs", "checkpoint files",
	"mutation logs", "revision tables", "backoff timers", "queue drains",
}

var enAdverbials = []string{
	"in bounded time", "under backpressure", "with exponential backoff",
	"in a single transaction", "per connection", "before commit",
	"after reconciliation", "on every poll", "at a fixed interval",
	"without full scans", "across restarts", "under cold cache",
}

var enNounPhrases = []string{
	"A bounded min-heap keeps the top-K candidates without a full sort.",
	"Batch JOIN reduces per-query metadata lookups to O(1) regardless of topK.",
	"Warm cache queries exercise only the pure retrieval path.",
	"Cold cache runs include the full index load before measurement starts.",
	"Percentiles use the nearest-rank method over sorted run durations.",
	"The SSE stream is throttled and merged during refresh bursts.",
	"Low-frequency reconciliation replaces the recursive full-disk scan.",
	"Embedding dimensions are truncated to the configured value.",
	"Seed 42 reproduces the identical dataset on any machine.",
	"Vector candidates are compared with float32 cosine similarity.",
	"Idle state performs no high-frequency recursive directory scans.",
	"Versioned cache keys, quotas and TTL bound the text cache growth.",
	"Streaming hashes keep memory flat for large files.",
	"The metadata fetch is a single bounded query per search request.",
	"Rerank runs remotely and is excluded from the local p95 budget.",
}

var cnSentences = []string{
	"向量检索模块在本地内存中线性扫描候选向量，避免远端往返。",
	"候选元数据通过批量 JOIN 一次取出，消除逐条查询的 N+1 开销。",
	"top-K 使用有界小顶堆维护，避免全量排序带来的额外开销。",
	"空闲状态下采用低频对账取代 8 秒递归全盘扫描。",
	"SSE 刷新进行合并与节流，仅在断线后恢复轮询。",
	"文本缓存采用版本化键、配额与 TTL，并定期清理过期项。",
	"大文件通过流式哈希降低内存占用，保持常数量级开销。",
	"每次候选检索的 SQL 查询数保持 O(1)，不随 topK 线性增长。",
	"基准数据使用固定种子生成，保证结果在任意机器上可复现。",
	"预热完成后进行不少于 30 次的正式测量，记录 p50 与 p95。",
	"冷缓存条件下首次查询包含完整的索引加载过程。",
	"热缓存条件下索引常驻内存，测量聚焦纯检索路径。",
	"本地候选检索的 p95 目标低于 300 毫秒，且不包含远端 rerank。",
	"磁盘为 NVMe SSD，随机读取延迟远低于 HDD，适合本地索引。",
	"数据生成器只依赖 Go 标准库，不引入任何第三方依赖。",
	"工作区根目录始终被排除在文件扫描范围之外。",
	"大文件同步采用流式处理，避免一次性载入内存。",
	"所有变更在单事务内提交，保证索引替换的原子性。",
	"请求失败时按指数退避重试，防止雪崩效应。",
	"诊断摘要定期汇总健康、延迟与吞吐指标。",
}

var cnTopics = []string{
	"向量检索", "索引构建", "候选生成", "缓存策略", "文件同步",
	"流式处理", "对账机制", "错误处理", "性能预算", "可观测性",
}

var codeSamples = []string{
	"items := make([]float32, 0, 1024)",
	"score := cosineSimilarity(query, chunk)",
	"heap.Push(&topK, item{score: s, idx: i})",
	"if h.Len() < topK { heap.Push(&h, x) } else { heap.Fix(&h, 0) }",
	"rows, err := db.QueryContext(ctx, q, args...)",
	"if err != nil { return nil, err }",
	"mu.Lock(); defer mu.Unlock()",
	"ctx, cancel := context.WithTimeout(parent, 5*time.Second)",
	"b.ReportMetric(float64(ms)*1e6/float64(b.N), \"ns/op\")",
	"defer close(done)",
	"select { case <-ctx.Done(): return default: }",
	"lastIndex := store.ReplaceFileIndex(fileID, chunks)",
	"evicted := cache.EvictIfOverQuota(ttl)",
	"summary.QueueDrains.Set(0)",
	"go reconciler.Run(interval)",
}

var enVarNames = []string{"query", "vec", "chunk", "rows", "scores", "cands", "ev", "acc"}

// ---- 内容块生成 ----

func enSentence(rng *rand.Rand) string {
	if rng.Intn(5) == 0 {
		return enNounPhrases[rng.Intn(len(enNounPhrases))]
	}
	return fmt.Sprintf("%s %s %s %s.",
		enSubjects[rng.Intn(len(enSubjects))],
		enVerbs[rng.Intn(len(enVerbs))],
		enObjects[rng.Intn(len(enObjects))],
		enAdverbials[rng.Intn(len(enAdverbials))])
}

func cnParagraph(rng *rand.Rand) string {
	n := 3 + rng.Intn(4) // 3-6 句
	parts := make([]string, 0, n)
	seen := map[int]bool{}
	for len(parts) < n {
		i := rng.Intn(len(cnSentences))
		if seen[i] && len(parts) > 1 {
			continue
		}
		seen[i] = true
		parts = append(parts, cnSentences[i])
	}
	// cnSentences 自带结尾句号，先去除再以句号连接，避免出现 "。。"。
	clean := make([]string, 0, len(parts))
	for _, s := range parts {
		clean = append(clean, strings.TrimSuffix(s, "。"))
	}
	return strings.Join(clean, "。") + "。"
}

func enParagraph(rng *rand.Rand) string {
	n := 3 + rng.Intn(4) // 3-6 句
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, enSentence(rng))
	}
	return strings.Join(parts, " ")
}

// paragraph 以 2/3 概率为英文、1/3 概率为中文。
func paragraph(rng *rand.Rand) string {
	if rng.Intn(3) == 0 {
		return cnParagraph(rng)
	}
	return enParagraph(rng)
}

func listBlock(rng *rand.Rand) string {
	n := 3 + rng.Intn(4)
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, "- "+enSentence(rng))
	}
	return strings.Join(lines, "\n")
}

func codeBlock(rng *rand.Rand) string {
	n := 3 + rng.Intn(5)
	lines := make([]string, 0, n+2)
	lines = append(lines, "```go")
	for i := 0; i < n; i++ {
		lines = append(lines, codeSamples[rng.Intn(len(codeSamples))])
	}
	lines = append(lines, "```")
	return strings.Join(lines, "\n")
}

func headingBlock(rng *rand.Rand) string {
	if rng.Intn(4) == 0 {
		return fmt.Sprintf("## %s 说明", cnTopics[rng.Intn(len(cnTopics))])
	}
	return fmt.Sprintf("## Section %d — %s",
		1+rng.Intn(12), strings.Title(enObjects[rng.Intn(len(enObjects))]))
}

// block 生成一个内容块；每块近似一个 chunk。
func block(rng *rand.Rand) string {
	switch rng.Intn(10) {
	case 0, 1:
		return headingBlock(rng)
	case 2, 3:
		return listBlock(rng)
	case 4:
		return codeBlock(rng)
	default:
		return paragraph(rng)
	}
}

func genFile(rng *rand.Rand, path string, idx, paras int) (blocks int, err error) {
	var b strings.Builder
	title := "Bench Doc"
	if rng.Intn(3) == 0 {
		title = cnTopics[rng.Intn(len(cnTopics))]
	}
	b.WriteString(fmt.Sprintf("# %s %06d\n\n", title, idx))
	for i := 0; i < paras; i++ {
		b.WriteString(block(rng))
		b.WriteString("\n\n")
	}
	return paras, os.WriteFile(path, []byte(b.String()), 0o644)
}

func main() {
	files := flag.Int("files", 500, "生成文件数（500 或 5000）")
	dir := flag.String("dir", "./benchdata", "输出目录")
	paras := flag.Int("paras", 20, "每文件内容块数（近似 chunk 数）")
	seed := flag.Int64("seed", 42, "随机种子（固定保证可复现）")
	flag.Parse()

	if *files <= 0 {
		fmt.Fprintln(os.Stderr, "files 必须 >= 1")
		os.Exit(2)
	}
	if *paras <= 0 {
		fmt.Fprintln(os.Stderr, "paras 必须 >= 1")
		os.Exit(2)
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "创建输出目录失败: %v\n", err)
		os.Exit(1)
	}

	rng := rand.New(rand.NewSource(*seed))
	start := time.Now()

	totalBlocks := 0
	totalBytes := int64(0)
	for i := 0; i < *files; i++ {
		name := fmt.Sprintf("doc_%06d.md", i)
		path := filepath.Join(*dir, name)
		blocks, err := genFile(rng, path, i, *paras)
		if err != nil {
			fmt.Fprintf(os.Stderr, "写入 %s 失败: %v\n", path, err)
			os.Exit(1)
		}
		st, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取 %s 失败: %v\n", path, err)
			os.Exit(1)
		}
		totalBlocks += blocks
		totalBytes += st.Size()
	}

	fmt.Printf("生成完成 seed=%d files=%d paras=%d blocks≈%d bytes=%d elapsed=%s dir=%s\n",
		*seed, *files, *paras, totalBlocks, totalBytes, time.Since(start).Round(time.Millisecond), *dir)
}
