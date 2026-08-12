package storage

import (
	"sort"
	"testing"

	"memora/internal/contract"
)

// TestVectorsSearch_TopKMatchesFullSort 表驱动：不同 topK 下 bounded 小顶堆结果与
// "全量计算分数 + 全量排序取前 K"的参考结果一致（集合与顺序）。
func TestVectorsSearch_TopKMatchesFullSort(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	fileID := mustInsertFile(t, m)
	chunks := []*contract.Chunk{
		{Seq: 0, TokenEst: 10, Text: "c0"},
		{Seq: 1, TokenEst: 10, Text: "c1"},
		{Seq: 2, TokenEst: 10, Text: "c2"},
		{Seq: 3, TokenEst: 10, Text: "c3"},
		{Seq: 4, TokenEst: 10, Text: "c4"},
	}
	// 与查询向量 makeUnitVec(4,0) 的余弦相似度：1.0、0.707、0、0、0（含并列）
	vecs := [][]float32{
		makeUnitVec(4, 0),
		{1, 1, 0, 0}, // cos = 1/√2 ≈ 0.707
		makeUnitVec(4, 1),
		makeUnitVec(4, 2),
		makeUnitVec(4, 3),
	}
	if err := m.ReplaceFileIndex(fileID, chunks, vecs, 4); err != nil {
		t.Fatalf("ReplaceFileIndex 失败: %v", err)
	}

	query := makeUnitVec(4, 0)

	// 参考：全量分数 + 全量排序（score 降序，并列 ChunkID 升序，与 topKVectors 规则一致）
	all, err := m.VectorsLoadAll()
	if err != nil {
		t.Fatalf("VectorsLoadAll 失败: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("期望 5 条向量, 实际 %d", len(all))
	}
	type ref struct {
		ChunkID int64
		Score   float64
	}
	refs := make([]ref, 0, len(all))
	for _, e := range all {
		refs = append(refs, ref{ChunkID: e.ChunkID, Score: cosineSimilarity(query, e.Vec)})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Score != refs[j].Score {
			return refs[i].Score > refs[j].Score
		}
		return refs[i].ChunkID < refs[j].ChunkID
	})

	cases := []struct {
		name string
		topK int
	}{
		{"topK=1", 1},
		{"topK=3", 3},
		{"topK=len", len(refs)},
		{"topK>len", len(refs) + 100},
		{"topK=0", 0},
		{"topK<0", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := m.VectorsSearch(query, c.topK)
			if err != nil {
				t.Fatalf("VectorsSearch 失败: %v", err)
			}
			wantN := c.topK
			if wantN > len(refs) {
				wantN = len(refs)
			}
			if wantN < 0 {
				wantN = 0
			}
			if len(got) != wantN {
				t.Fatalf("返回 %d 条, 期望 %d", len(got), wantN)
			}
			for i := 0; i < wantN; i++ {
				if got[i].ChunkID != refs[i].ChunkID {
					t.Fatalf("第 %d 条 ChunkID=%d, 期望 %d（顺序应与全量排序一致）", i, got[i].ChunkID, refs[i].ChunkID)
				}
				if i > 0 && got[i].Score > got[i-1].Score {
					t.Fatalf("分数应降序: [%d]=%f > [%d]=%f", i-1, got[i-1].Score, i, got[i].Score)
				}
			}
		})
	}
}

// TestVectorsSearch_EmptyIndex 空索引返回空切片而非 nil。
func TestVectorsSearch_EmptyIndex(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	got, err := m.VectorsSearch(makeUnitVec(4, 0), 5)
	if err != nil {
		t.Fatalf("VectorsSearch 失败: %v", err)
	}
	if got == nil {
		t.Fatalf("空索引应返回空切片而非 nil")
	}
	if len(got) != 0 {
		t.Fatalf("空索引应返回 0 条, 实际 %d", len(got))
	}
}

// TestVectorsSearch_TieBreaks 相同分数的并列条目按 ChunkID 升序返回（确定性）。
func TestVectorsSearch_TieBreaks(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	fileID := mustInsertFile(t, m)
	// 两个分块向量完全相同 → 与查询相似度并列
	if err := m.ReplaceFileIndex(fileID, []*contract.Chunk{
		{Seq: 0, TokenEst: 10, Text: "a"},
		{Seq: 1, TokenEst: 10, Text: "b"},
	}, [][]float32{makeUnitVec(4, 0), makeUnitVec(4, 0)}, 4); err != nil {
		t.Fatalf("ReplaceFileIndex 失败: %v", err)
	}
	dbChunks, err := m.ChunksByFile(fileID)
	if err != nil || len(dbChunks) != 2 {
		t.Fatalf("查询分块失败: %v", err)
	}
	minID, maxID := dbChunks[0].ID, dbChunks[1].ID
	if minID > maxID {
		minID, maxID = maxID, minID
	}

	// topK=1：分数并列时取 ChunkID 较小的
	got, err := m.VectorsSearch(makeUnitVec(4, 0), 1)
	if err != nil {
		t.Fatalf("VectorsSearch 失败: %v", err)
	}
	if len(got) != 1 || got[0].ChunkID != minID {
		t.Fatalf("并列时 top1 应为 ChunkID=%d, 实际 %+v", minID, got)
	}

	// topK=2：两条都返回，按 ChunkID 升序
	got2, err := m.VectorsSearch(makeUnitVec(4, 0), 2)
	if err != nil {
		t.Fatalf("VectorsSearch 失败: %v", err)
	}
	if len(got2) != 2 || got2[0].ChunkID != minID || got2[1].ChunkID != maxID {
		t.Fatalf("并列时 top2 应按 ChunkID 升序: %+v", got2)
	}
}
