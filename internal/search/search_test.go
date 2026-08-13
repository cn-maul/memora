package search

import (
	"errors"
	"testing"

	"memora/internal/contract"
)

// fakeSearchLLM 返回固定查询向量，Rerank 未配置（返回错误 → 回退余弦分数）。
type fakeSearchLLM struct{}

func (fakeSearchLLM) Embed(texts []string) ([][]float32, error) { return nil, nil }
func (fakeSearchLLM) EmbedQuery(text string) ([]float32, error) { return []float32{1, 0}, nil }
func (fakeSearchLLM) Rerank(query string, docs []string) ([]float64, error) {
	return nil, errors.New("not configured")
}

// fakeSearchStorage 记录批量/逐条查询调用次数，用于断言 N+1 已被消除。
type fakeSearchStorage struct {
	entries    []contract.VectorEntry
	chunksByID map[int64]*contract.Chunk
	filesByID  map[int64]*contract.FileInfo

	chunksByIDsCalls int
	filesByIDsCalls  int
	chunksGetCalls   int
	filesGetCalls    int
}

func (f *fakeSearchStorage) ChunksGet(id int64) (*contract.Chunk, error) {
	f.chunksGetCalls++
	return f.chunksByID[id], nil
}
func (f *fakeSearchStorage) ChunksByIDs(ids []int64) (map[int64]*contract.Chunk, error) {
	f.chunksByIDsCalls++
	return f.chunksByID, nil
}
func (f *fakeSearchStorage) FilesGet(id int64) (*contract.FileInfo, error) {
	f.filesGetCalls++
	return f.filesByID[id], nil
}
func (f *fakeSearchStorage) FilesByIDs(ids []int64) (map[int64]*contract.FileInfo, error) {
	f.filesByIDsCalls++
	return f.filesByID, nil
}
func (f *fakeSearchStorage) FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*contract.FileInfo, int, error) {
	return nil, 0, nil
}
func (f *fakeSearchStorage) TagsList() ([]*contract.TagInfo, error) { return nil, nil }
func (f *fakeSearchStorage) FilesFindByName(keyword string, limit int) ([]*contract.FileInfo, error) {
	return nil, nil
}
func (f *fakeSearchStorage) FileTagsListByFile(fileID int64) ([]contract.FileTag, error) {
	return nil, nil
}
func (f *fakeSearchStorage) FileTagsListByTag(tagID int64) ([]int64, error) { return nil, nil }
func (f *fakeSearchStorage) VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error) {
	return f.entries, nil
}

// TestQueryBatchRetrieval Query 应一次性批量取回分块与文件（各 1 次），
// 不再逐条 ChunksGet/FilesGet；结果顺序与分数语义不变。
func TestQueryBatchRetrieval(t *testing.T) {
	st := &fakeSearchStorage{
		entries: []contract.VectorEntry{
			{ChunkID: 10, Score: 0.9},
			{ChunkID: 11, Score: 0.8},
			{ChunkID: 12, Score: 0.7},
		},
		chunksByID: map[int64]*contract.Chunk{
			10: {ID: 10, FileID: 100, Seq: 1, Text: "c10"},
			11: {ID: 11, FileID: 100, Seq: 2, Text: "c11"},
			12: {ID: 12, FileID: 101, Seq: 1, Text: "c12"},
		},
		filesByID: map[int64]*contract.FileInfo{
			100: {ID: 100, RelPath: "a.md", Mtime: 1},
			101: {ID: 101, RelPath: "b.md", Mtime: 2},
		},
	}
	m := New(fakeSearchLLM{}, st)

	results, total, err := m.Query("查询", nil, 0)
	if err != nil {
		t.Fatalf("Query 失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("total=%d, want 2", total)
	}
	if len(results) != 2 {
		t.Fatalf("results=%d, want 2", len(results))
	}
	if results[0].FileID != 100 || results[1].FileID != 101 {
		t.Fatalf("结果顺序不符: %+v", results)
	}
	if results[0].Score != 0.9 || results[1].Score != 0.7 {
		t.Fatalf("score 不符: %+v", results)
	}
	if results[0].MatchedChunks != 2 || results[1].MatchedChunks != 1 {
		t.Fatalf("matchedChunks 不符: %+v", results)
	}
	if results[0].HitText != "c10" || results[1].HitText != "c12" {
		t.Fatalf("hitText 不符: %+v", results)
	}

	// 批量路径：chunks/files 各 1 次批量查询，0 次逐条查询
	if st.chunksByIDsCalls != 1 || st.filesByIDsCalls != 1 {
		t.Fatalf("批量查询应各调用 1 次: chunksByIDs=%d filesByIDs=%d", st.chunksByIDsCalls, st.filesByIDsCalls)
	}
	if st.chunksGetCalls != 0 || st.filesGetCalls != 0 {
		t.Fatalf("不应调用逐条查询: chunksGet=%d filesGet=%d", st.chunksGetCalls, st.filesGetCalls)
	}
}

// TestQueryBatchSkipsMissing 缺失分块或缺失文件的条目应被跳过（与逐条查询行为一致）。
func TestQueryBatchSkipsMissing(t *testing.T) {
	st := &fakeSearchStorage{
		entries: []contract.VectorEntry{
			{ChunkID: 10, Score: 0.9}, // 有分块有文件
			{ChunkID: 99, Score: 0.8}, // 分块不存在 → 跳过
			{ChunkID: 11, Score: 0.7}, // 文件不存在 → 跳过
		},
		chunksByID: map[int64]*contract.Chunk{
			10: {ID: 10, FileID: 100, Seq: 1, Text: "c10"},
			11: {ID: 11, FileID: 999, Seq: 1, Text: "c11"},
		},
		filesByID: map[int64]*contract.FileInfo{
			100: {ID: 100, RelPath: "a.md"},
		},
	}
	m := New(fakeSearchLLM{}, st)

	results, total, err := m.Query("查询", nil, 0)
	if err != nil {
		t.Fatalf("Query 失败: %v", err)
	}
	if total != 1 || len(results) != 1 || results[0].FileID != 100 {
		t.Fatalf("期望仅命中文件 100: total=%d results=%+v", total, results)
	}
}
