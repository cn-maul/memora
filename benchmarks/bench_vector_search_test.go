// bench_vector_search_test.go — 本地向量候选检索基准（Phase 5）。
//
// 本文件只做"候选检索"层面的测量，不要求跑通整系统，也不依赖 memora 生产包：
//   - 余弦相似度 + top-K 小顶堆为"拷贝版"实现（直接写在本文件内，与生产
//     实现同思路：线性扫描 + bounded heap），避免改动测试基础设施。
//   - 对比两种实现：全量排序 vs 有界小顶堆 topK（默认 topK=100）。
//   - 用标准 testing.B 跑 ns/op、allocs/op 基线，并用自定义时间统计
//     （预热 + 不少于 30 次正式运行）给出 p50 / p95。
//
// p50/p95 换算：对正式运行每次耗时排序后采用 nearest-rank：
//
//	index = ceil(p × n) - 1，越界时取 0 或 n-1。
//	p50 = 中位数；p95 = 第 95 百分位。
//
// 运行（在 benchmarks/ 下）：
//
//	go test -run 'TestBench.*P50P95' -v -count=1 .   # 自定义统计 p50/p95
//	go test -bench 'BenchmarkCandidate' -benchmem .  # 吞吐/分配基线
//	go test . -args -bench.dim 1024 -bench.vectors 50000 -bench.topk 100 \
//	    -bench.warmup 5 -bench.runs 30               # 调整参数
//
// 注意：本文件为 package benchmarks；gen_data.go 因 //go:build ignore 不参与
// 编译，二者可共存于同一目录（Go 单目录单包约束）。

package benchmarks

import (
	"container/heap"
	"flag"
	"math"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---- 可配置参数（对应 README §2/§4）----

var (
	benchDim          int
	benchNumVectors   int
	benchTopK         int
	benchWarmupRuns   int
	benchMeasuredRuns int
)

func init() {
	flag.IntVar(&benchDim, "bench.dim", 1024, "向量维度（默认 1024，与 embed.dimensions 一致）")
	flag.IntVar(&benchNumVectors, "bench.vectors", 50000, "候选向量数（默认 50000，对应 50k chunks 规模）")
	flag.IntVar(&benchTopK, "bench.topk", 100, "top-K 取值（默认 100）")
	flag.IntVar(&benchWarmupRuns, "bench.warmup", 5, "预热运行次数")
	flag.IntVar(&benchMeasuredRuns, "bench.runs", 30, "正式运行次数（不少于 30 次）")
}

func sanitizeBenchConfig() {
	if benchDim <= 0 {
		benchDim = 1024
	}
	if benchNumVectors <= 0 {
		benchNumVectors = 50000
	}
	if benchTopK <= 0 {
		benchTopK = 100
	}
	if benchTopK > benchNumVectors {
		benchTopK = benchNumVectors
	}
	if benchWarmupRuns < 0 {
		benchWarmupRuns = 5
	}
}

// ---- 拷贝版余弦相似度（不 import 生产包）----

func cosineSimilarity(a, b []float32) float32 {
	var dot, na, nb float64
	for i := range a {
		x := float64(a[i])
		y := float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	denom := math.Sqrt(na * nb)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// ---- top-K 数据结构与两种实现 ----

type scoreItem struct {
	score float32
	idx   int
}

// minScoreHeap 是有界小顶堆：堆顶是当前 top-K 中分数最小者，便于淘汰。
type minScoreHeap []scoreItem

func (h minScoreHeap) Len() int           { return len(h) }
func (h minScoreHeap) Less(i, j int) bool { return h[i].score < h[j].score }
func (h minScoreHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minScoreHeap) Push(x any)        { *h = append(*h, x.(scoreItem)) }
func (h *minScoreHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// fullSortSearch：计算全部相似度后全量排序取 topK（对比基线）。
func fullSortSearch(query []float32, vectors [][]float32, topK int) []scoreItem {
	scores := make([]scoreItem, len(vectors))
	for i, v := range vectors {
		scores[i] = scoreItem{score: cosineSimilarity(query, v), idx: i}
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].score > scores[j].score })
	return scores[:topK]
}

// topKSearch：线性扫描 + 有界小顶堆维护 topK，避免全量排序。
func topKSearch(query []float32, vectors [][]float32, topK int) []scoreItem {
	h := make(minScoreHeap, 0, topK)
	for i, v := range vectors {
		s := cosineSimilarity(query, v)
		if h.Len() < topK {
			heap.Push(&h, scoreItem{score: s, idx: i})
		} else if s > h[0].score {
			h[0] = scoreItem{score: s, idx: i}
			heap.Fix(&h, 0)
		}
	}
	out := make([]scoreItem, 0, h.Len())
	for h.Len() > 0 {
		out = append(out, heap.Pop(&h).(scoreItem))
	}
	// 小顶堆依次弹出为升序，反转为降序。
	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

// ---- 随机向量生成（固定 seed 42，可复现）----

var (
	dataOnce    sync.Once
	sharedQuery []float32
	sharedVecs  [][]float32
)

func randVec(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var sum float64
	for i := range v {
		x := rng.NormFloat64()
		v[i] = float32(x)
		sum += x * x
	}
	norm := math.Sqrt(sum)
	if norm > 0 {
		for i := range v {
			v[i] = float32(float64(v[i]) / norm)
		}
	}
	return v
}

func benchData() ([]float32, [][]float32) {
	dataOnce.Do(func() {
		sanitizeBenchConfig()
		rng := rand.New(rand.NewSource(42))
		sharedQuery = randVec(rng, benchDim)
		sharedVecs = make([][]float32, benchNumVectors)
		for i := range sharedVecs {
			sharedVecs[i] = randVec(rng, benchDim)
		}
	})
	return sharedQuery, sharedVecs
}

// ---- 百分位统计（nearest-rank）----

func percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(n))) - 1
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	return sorted[i]
}

type searchFunc func(query []float32, vectors [][]float32, topK int) []scoreItem

// ---- 正确性 / 边界测试 ----

func TestCosineSimilaritySanity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	c := []float32{2, 0, 0}
	if s := cosineSimilarity(a, c); s < 0.99 {
		t.Fatalf("平行向量余弦应≈1，got %v", s)
	}
	if s := cosineSimilarity(a, b); s > 1e-6 {
		t.Fatalf("正交向量余弦应≈0，got %v", s)
	}
}

func TestCandidateSearchCorrectness(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const dim, n, topK = 8, 60, 10
	query := randVec(rng, dim)
	vectors := make([][]float32, n)
	for i := range vectors {
		vectors[i] = randVec(rng, dim)
	}

	full := fullSortSearch(query, vectors, topK)
	heapRes := topKSearch(query, vectors, topK)

	if len(full) != topK || len(heapRes) != topK {
		t.Fatalf("len(full)=%d len(heap)=%d，期望 %d", len(full), len(heapRes), topK)
	}
	for i := range full {
		if full[i].idx != heapRes[i].idx || full[i].score != heapRes[i].score {
			t.Fatalf("第 %d 名不一致: full(idx=%d,score=%v) vs heap(idx=%d,score=%v)",
				i, full[i].idx, full[i].score, heapRes[i].idx, heapRes[i].score)
		}
	}
}

// ---- 自定义时间统计：预热 + 不少于 30 次正式运行，输出 p50/p95 ----

func runMeasurement(t *testing.T, name string, fn searchFunc) {
	t.Helper()
	query, vectors := benchData()
	sanitizeBenchConfig()

	if benchMeasuredRuns < 30 {
		t.Fatalf("bench.runs=%d 低于最低要求 30 次正式运行，请用 -bench.runs 上调", benchMeasuredRuns)
	}

	for i := 0; i < benchWarmupRuns; i++ {
		_ = fn(query, vectors, benchTopK)
	}

	durations := make([]time.Duration, 0, benchMeasuredRuns)
	for i := 0; i < benchMeasuredRuns; i++ {
		start := time.Now()
		_ = fn(query, vectors, benchTopK)
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	p50 := percentile(durations, 0.50)
	p95 := percentile(durations, 0.95)

	t.Logf("实现=%s 规模=%d 文件/%d chunks | 维度=%d topK=%d | 预热=%d 正式=%d | "+
		"p50=%v p95=%v 目标 p95<300ms(%v)",
		name, benchNumVectors/10, benchNumVectors, benchDim, benchTopK,
		benchWarmupRuns, benchMeasuredRuns, p50, p95, p95 < 300*time.Millisecond)
}

func TestBenchFullSortP50P95(t *testing.T) {
	runMeasurement(t, "full-sort", fullSortSearch)
}

func TestBenchTopKP50P95(t *testing.T) {
	runMeasurement(t, "topk-heap", topKSearch)
}

// ---- 标准 testing.B 基线（ns/op、allocs/op，并附 p50/p95 采样）----

func runBenchmark(b *testing.B, name string, fn searchFunc) {
	query, vectors := benchData()
	sanitizeBenchConfig()

	durations := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		_ = fn(query, vectors, benchTopK)
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50 := percentile(durations, 0.50)
	p95 := percentile(durations, 0.95)

	b.ReportAllocs()
	b.ReportMetric(float64(p50/time.Microsecond), "p50_us")
	b.ReportMetric(float64(p95/time.Microsecond), "p95_us")
	b.Logf("实现=%s 维度=%d 规模=%d topK=%d 采样=%d p50=%v p95=%v",
		name, benchDim, benchNumVectors, benchTopK, len(durations), p50, p95)
}

func BenchmarkCandidateFullSort(b *testing.B) {
	runBenchmark(b, "full-sort", fullSortSearch)
}

func BenchmarkCandidateTopK(b *testing.B) {
	runBenchmark(b, "topk-heap", topKSearch)
}
