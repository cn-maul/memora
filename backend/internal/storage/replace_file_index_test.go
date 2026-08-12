package storage

import (
	"testing"

	"memora/internal/contract"
)

// makeUnitVec 构造 dim 维单位向量：第 idx 分量为 1，其余为 0。
// 不同向量余弦相似度可区分，避免全等分量导致的排序不确定。
func makeUnitVec(dim, idx int) []float32 {
	v := make([]float32, dim)
	if idx >= 0 && idx < dim {
		v[idx] = 1
	}
	return v
}

// mustInsertFile 插入一个测试文件并返回其 ID
func mustInsertFile(t *testing.T, m *Module) int64 {
	t.Helper()
	id, err := m.FilesUpsert(&contract.FileInfo{
		RelPath:     "test.md",
		Size:        10,
		Mtime:       1,
		ContentHash: "abc",
		DocType:     "md",
		IndexStatus: "pending",
	})
	if err != nil {
		t.Fatalf("插入测试文件失败: %v", err)
	}
	return id
}

// TestReplaceFileIndex_Success 替换后 chunks/vectors 数量正确、查询与检索可见新内容。
func TestReplaceFileIndex_Success(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	fileID := mustInsertFile(t, m)

	chunks := []*contract.Chunk{
		{Seq: 0, TokenEst: 10, Text: "新内容一"},
		{Seq: 1, TokenEst: 20, Text: "新内容二"},
	}
	vecs := [][]float32{makeUnitVec(4, 0), makeUnitVec(4, 1)}

	if err := m.ReplaceFileIndex(fileID, chunks, vecs, 4); err != nil {
		t.Fatalf("ReplaceFileIndex 失败: %v", err)
	}

	// 分块数量与内容正确
	gotChunks, err := m.ChunksByFile(fileID)
	if err != nil {
		t.Fatalf("查询分块失败: %v", err)
	}
	if len(gotChunks) != 2 {
		t.Fatalf("期望 2 个分块, 实际 %d", len(gotChunks))
	}
	if gotChunks[0].Text != "新内容一" || gotChunks[1].Text != "新内容二" {
		t.Fatalf("分块内容不符: %v", gotChunks)
	}

	// 向量数量正确
	cnt, err := m.VectorCount()
	if err != nil {
		t.Fatalf("统计向量失败: %v", err)
	}
	if cnt != 2 {
		t.Fatalf("期望 2 个向量, 实际 %d", cnt)
	}

	// 内存索引可检索到新内容（查询命中"新内容一"的向量）
	res, err := m.VectorsSearch(makeUnitVec(4, 0), 2)
	if err != nil {
		t.Fatalf("向量检索失败: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("期望检索到 2 条, 实际 %d", len(res))
	}
	if res[0].ChunkID != gotChunks[0].ID {
		t.Fatalf("期望最高分命中新分块 %d, 实际 %d", gotChunks[0].ID, res[0].ChunkID)
	}
}

// TestReplaceFileIndex_CountMismatch 向量数 ≠ 分块数时返回错误且旧数据仍在。
func TestReplaceFileIndex_CountMismatch(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	fileID := mustInsertFile(t, m)

	// 先成功写入 1 块
	if err := m.ReplaceFileIndex(fileID, []*contract.Chunk{{Seq: 0, TokenEst: 1, Text: "旧一"}}, [][]float32{makeUnitVec(4, 0)}, 4); err != nil {
		t.Fatalf("初始写入失败: %v", err)
	}

	// 向量数(2) ≠ 分块数(1)
	err = m.ReplaceFileIndex(fileID, []*contract.Chunk{{Seq: 1, TokenEst: 1, Text: "新一"}}, [][]float32{makeUnitVec(4, 1), makeUnitVec(4, 2)}, 4)
	if err == nil {
		t.Fatal("期望向量数量不匹配时返回错误")
	}

	// 旧数据仍在
	gotChunks, err := m.ChunksByFile(fileID)
	if err != nil {
		t.Fatalf("查询分块失败: %v", err)
	}
	if len(gotChunks) != 1 || gotChunks[0].Text != "旧一" {
		t.Fatalf("旧数据被破坏: %v", gotChunks)
	}
	cnt, err := m.VectorCount()
	if err != nil {
		t.Fatalf("统计向量失败: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("期望向量数保持 1, 实际 %d", cnt)
	}
}

// TestReplaceFileIndex_MidTxFailure 注入事务中段写入失败（seq 重复触发
// UNIQUE(file_id, seq)）后整体回滚，旧 chunks/向量与内存索引均完整。
func TestReplaceFileIndex_MidTxFailure(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	fileID := mustInsertFile(t, m)

	// 先成功写入 2 块
	if err := m.ReplaceFileIndex(fileID, []*contract.Chunk{
		{Seq: 0, TokenEst: 1, Text: "旧一"},
		{Seq: 1, TokenEst: 1, Text: "旧二"},
	}, [][]float32{makeUnitVec(4, 0), makeUnitVec(4, 1)}, 4); err != nil {
		t.Fatalf("初始写入失败: %v", err)
	}

	// 注入失败：两个新分块 seq 重复，第二个 INSERT 触发唯一约束
	err = m.ReplaceFileIndex(fileID, []*contract.Chunk{
		{Seq: 0, TokenEst: 1, Text: "新一"},
		{Seq: 0, TokenEst: 1, Text: "新二"},
	}, [][]float32{makeUnitVec(4, 2), makeUnitVec(4, 3)}, 4)
	if err == nil {
		t.Fatal("期望触发约束失败返回错误")
	}

	// 回滚后旧数据完整
	gotChunks, err := m.ChunksByFile(fileID)
	if err != nil {
		t.Fatalf("查询分块失败: %v", err)
	}
	if len(gotChunks) != 2 || gotChunks[0].Text != "旧一" || gotChunks[1].Text != "旧二" {
		t.Fatalf("回滚后旧数据不完整: %v", gotChunks)
	}
	cnt, err := m.VectorCount()
	if err != nil {
		t.Fatalf("统计向量失败: %v", err)
	}
	if cnt != 2 {
		t.Fatalf("回滚后向量数应保持 2, 实际 %d", cnt)
	}

	// 内存索引未受污染
	res, err := m.VectorsSearch(makeUnitVec(4, 0), 10)
	if err != nil {
		t.Fatalf("向量检索失败: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("内存索引被污染, 期望 2 条, 实际 %d", len(res))
	}
}
