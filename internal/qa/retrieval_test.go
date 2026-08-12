package qa

import (
	"reflect"
	"strings"
	"testing"

	"memora/internal/contract"
)

// batchFakeStorage 在 fakeStorage 基础上实现可选批量接口 batchStore，
// 用于验证 buildContext 走批量路径（消除 N+1）。
type batchFakeStorage struct {
	*fakeStorage
	chunksByIDsCalls int
	filesByIDsCalls  int
}

func (b *batchFakeStorage) ChunksByIDs(ids []int64) (map[int64]*contract.Chunk, error) {
	b.chunksByIDsCalls++
	return b.chunksByID, nil
}

func (b *batchFakeStorage) FilesByIDs(ids []int64) (map[int64]*contract.FileInfo, error) {
	b.filesByIDsCalls++
	return b.filesByID, nil
}

// TestBuildContextGlobalBatch 全局模式：批量拉取分块与文件各 1 次，不做逐条查询。
func TestBuildContextGlobalBatch(t *testing.T) {
	inner := &fakeStorage{
		entries: []contract.VectorEntry{
			{ChunkID: 10, Score: 0.9},
			{ChunkID: 11, Score: 0.8},
			{ChunkID: 12, Score: 0.7},
		},
		chunksByID: map[int64]*contract.Chunk{
			10: {ID: 10, FileID: 1, Seq: 1, Text: "块一"},
			11: {ID: 11, FileID: 1, Seq: 2, Text: "块二"},
			12: {ID: 12, FileID: 2, Seq: 1, Text: "块三"},
		},
		filesByID: map[int64]*contract.FileInfo{
			1: {ID: 1, RelPath: "a.md"},
			2: {ID: 2, RelPath: "b.md"},
		},
	}
	st := &batchFakeStorage{fakeStorage: inner}
	m := newFakeModule(st, nil, &fakeEvents{})

	blocks, sources, err := m.buildContext(&contract.QARequest{Question: "测试问题", Mode: "global"})
	if err != nil {
		t.Fatalf("buildContext 失败: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("期望 3 个上下文块, 实际 %d: %v", len(blocks), blocks)
	}
	wantSources := []contract.QASource{
		{RelPath: "a.md", Seq: 1},
		{RelPath: "a.md", Seq: 2},
		{RelPath: "b.md", Seq: 1},
	}
	if !reflect.DeepEqual(sources, wantSources) {
		t.Fatalf("sources 不符:\n got %v\nwant %v", sources, wantSources)
	}
	if st.chunksByIDsCalls != 1 || st.filesByIDsCalls != 1 {
		t.Fatalf("批量查询应各调用 1 次: chunksByIDs=%d filesByIDs=%d", st.chunksByIDsCalls, st.filesByIDsCalls)
	}
	if inner.chunksGetCalls != 0 || inner.filesGetCalls != 0 {
		t.Fatalf("批量路径不应调用逐条查询: chunksGet=%d filesGet=%d", inner.chunksGetCalls, inner.filesGetCalls)
	}
}

// TestBuildContextGlobalFallback 存储未实现 batchStore 时回退逐条 ChunksGet/FilesGet，行为等价。
func TestBuildContextGlobalFallback(t *testing.T) {
	st := &fakeStorage{
		entries: []contract.VectorEntry{
			{ChunkID: 10, Score: 0.9},
			{ChunkID: 11, Score: 0.8},
		},
		chunksByID: map[int64]*contract.Chunk{
			10: {ID: 10, FileID: 1, Seq: 1, Text: "块一"},
			11: {ID: 11, FileID: 1, Seq: 2, Text: "块二"},
		},
		filesByID: map[int64]*contract.FileInfo{
			1: {ID: 1, RelPath: "a.md"},
		},
	}
	m := newFakeModule(st, nil, &fakeEvents{})

	blocks, sources, err := m.buildContext(&contract.QARequest{Question: "测试问题", Mode: "global"})
	if err != nil {
		t.Fatalf("buildContext 失败: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("期望 2 个上下文块, 实际 %d: %v", len(blocks), blocks)
	}
	wantSources := []contract.QASource{
		{RelPath: "a.md", Seq: 1},
		{RelPath: "a.md", Seq: 2},
	}
	if !reflect.DeepEqual(sources, wantSources) {
		t.Fatalf("sources 不符: got %v want %v", sources, wantSources)
	}
	// 回退路径：逐条查询确实被调用
	if st.chunksGetCalls != 2 || st.filesGetCalls != 2 {
		t.Fatalf("回退路径应逐条查询: chunksGet=%d filesGet=%d", st.chunksGetCalls, st.filesGetCalls)
	}
}

// TestBuildContextFileModeBatchFilters 文件模式超限检索：批量拉取分块并按 fileID 过滤。
func TestBuildContextFileModeBatchFilters(t *testing.T) {
	inner := &fakeStorage{
		file: &contract.FileInfo{ID: 1, RelPath: "target.md"},
		// 全文超过 maxContextChars → 走向量检索分支
		chunks: []*contract.Chunk{
			{ID: 1, FileID: 1, Seq: 1, Text: strings.Repeat("长", 300)},
		},
		entries: []contract.VectorEntry{
			{ChunkID: 10, Score: 0.9}, // 属于目标文件
			{ChunkID: 20, Score: 0.8}, // 属于其他文件 → 过滤
		},
		chunksByID: map[int64]*contract.Chunk{
			10: {ID: 10, FileID: 1, Seq: 2, Text: "目标块"},
			20: {ID: 20, FileID: 99, Seq: 1, Text: "其他块"},
		},
	}
	st := &batchFakeStorage{fakeStorage: inner}
	m := New(st, &fakeLLM{}, &fakeEvents{}, 100)

	blocks, sources, err := m.buildContext(&contract.QARequest{Question: "问题", Mode: "file", FileID: 1})
	if err != nil {
		t.Fatalf("buildContext 失败: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("期望 1 个上下文块, 实际 %d: %v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0], "目标块") {
		t.Fatalf("上下文块内容不符: %q", blocks[0])
	}
	wantSources := []contract.QASource{{RelPath: "target.md", Seq: 2}}
	if !reflect.DeepEqual(sources, wantSources) {
		t.Fatalf("sources 不符: got %v want %v", sources, wantSources)
	}
	if st.chunksByIDsCalls != 1 {
		t.Fatalf("批量分块查询应调用 1 次, 实际 %d", st.chunksByIDsCalls)
	}
	if inner.chunksGetCalls != 0 {
		t.Fatalf("批量路径不应调用 ChunksGet, 实际 %d", inner.chunksGetCalls)
	}
}

// TestTopKByScore bounded 小顶堆选出分数最高 topK 个，返回降序；含 topK>len 与并列。
func TestTopKByScore(t *testing.T) {
	items := []int{10, 30, 20, 50, 40} // 值即分数
	score := func(v int) float64 { return float64(v) }

	got := topKByScore(items, 3, score)
	if !reflect.DeepEqual(got, []int{50, 40, 30}) {
		t.Fatalf("top3 应为 [50 40 30], 实际 %v", got)
	}

	gotAll := topKByScore(items, 100, score)
	if !reflect.DeepEqual(gotAll, []int{50, 40, 30, 20, 10}) {
		t.Fatalf("topK>len 应返回全部降序, 实际 %v", gotAll)
	}

	if got0 := topKByScore(items, 0, score); len(got0) != 0 {
		t.Fatalf("topK=0 应返回空, 实际 %v", got0)
	}
	if gotEmpty := topKByScore([]int{}, 3, score); len(gotEmpty) != 0 {
		t.Fatalf("空输入应返回空, 实际 %v", gotEmpty)
	}
}
