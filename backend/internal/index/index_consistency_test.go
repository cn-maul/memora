package index

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"memora/internal/contract"
)

// ──────────────────── fake 依赖 ────────────────────

type replaceCall struct {
	fileID int64
	chunks []*contract.Chunk
	isNil  bool
}

type fakeStorage struct {
	files        map[int64]*contract.FileInfo
	byPath       map[string]*contract.FileInfo
	chunks       map[int64][]*contract.Chunk
	statuses     map[int64]string
	lastErr      string
	replaceCalls []replaceCall
	vectorCount  map[int64]int
	vectorDim    int
	markErr      error
	replaceErr   error
	upsertErr    error
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{
		files:       map[int64]*contract.FileInfo{},
		byPath:      map[string]*contract.FileInfo{},
		chunks:      map[int64][]*contract.Chunk{},
		statuses:    map[int64]string{},
		vectorCount: map[int64]int{},
		vectorDim:   4,
	}
}

func (s *fakeStorage) FilesGet(id int64) (*contract.FileInfo, error) { return s.files[id], nil }
func (s *fakeStorage) FilesFindByRelPath(relPath string) (*contract.FileInfo, error) {
	if f, ok := s.byPath[relPath]; ok {
		return f, nil
	}
	return nil, nil
}
func (s *fakeStorage) FilesUpsert(f *contract.FileInfo) (int64, error) {
	if s.upsertErr != nil {
		return 0, s.upsertErr
	}
	if f.ID == 0 {
		f.ID = int64(len(s.files) + 1)
	}
	s.files[f.ID] = f
	s.byPath[f.RelPath] = f
	return f.ID, nil
}
func (s *fakeStorage) FilesMarkStatus(id int64, status, lastError string) error {
	if s.markErr != nil {
		return s.markErr
	}
	s.statuses[id] = status
	s.lastErr = lastError
	return nil
}
func (s *fakeStorage) FilesMarkAllPending() error { return nil }
func (s *fakeStorage) FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*contract.FileInfo, int, error) {
	var out []*contract.FileInfo
	for _, f := range s.files {
		out = append(out, f)
	}
	return out, len(out), nil
}
func (s *fakeStorage) ChunksReplaceForFile(fileID int64, chuns []*contract.Chunk) error {
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.replaceCalls = append(s.replaceCalls, replaceCall{fileID: fileID, chunks: chuns, isNil: chuns == nil})
	s.chunks[fileID] = chuns
	return nil
}
func (s *fakeStorage) ChunksByFile(fileID int64) ([]*contract.Chunk, error) {
	return s.chunks[fileID], nil
}
func (s *fakeStorage) ChunksGet(id int64) (*contract.Chunk, error) { return nil, nil }
func (s *fakeStorage) VectorsInsert(chunkID int64, vec []float32, dim int) error {
	s.vectorCount[chunkID] = len(vec)
	return nil
}
func (s *fakeStorage) VectorsDelete(chunkID int64) error { return nil }
func (s *fakeStorage) VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error) {
	return nil, nil
}
func (s *fakeStorage) FileVectorDim(fileID int64) (int, bool, error) {
	n := 0
	for _, c := range s.chunks[fileID] {
		n += s.vectorCount[c.ID]
	}
	return s.vectorDim, n > 0, nil
}
func (s *fakeStorage) FileTagsReplace(fileID int64, tags []contract.FileTag) error { return nil }

type fakeExtract struct {
	err      error
	cacheKey string
	text     string
}

func (e *fakeExtract) ExtractFile(filePath string) (text string, cacheKey string, err error) {
	if e.err != nil {
		return "", "", e.err
	}
	return e.text, e.cacheKey, nil
}

type fakeLLM struct {
	err         error
	vecPerBatch int
	dim         int
}

func (l *fakeLLM) Embed(texts []string) ([][]float32, error) {
	if l.err != nil {
		return nil, l.err
	}
	n := l.vecPerBatch
	if n == 0 {
		n = len(texts)
	}
	var out [][]float32
	for i := 0; i < n; i++ {
		v := make([]float32, l.dim)
		v[0] = float32(i + 1)
		out = append(out, v)
	}
	return out, nil
}

type fakeEvents struct{}

func (e *fakeEvents) Notify(topic string, data interface{}) {}

func newTestModule(s *fakeStorage, l *fakeLLM) *Module {
	return New(s, &fakeExtract{cacheKey: "ck-1", text: "file text"}, l, &fakeEvents{}, ".", 200, 40, 4)
}

func newTestModuleWithText(s *fakeStorage, l *fakeLLM, text string) *Module {
	return New(s, &fakeExtract{cacheKey: "ck-1", text: text}, l, &fakeEvents{}, ".", 200, 40, 4)
}

// ──────────────────── 测试 ────────────────────

// P1-01：空文件（len(chunks)==0）必须执行原子 Replace（以 nil 清空旧索引），
// 并更新 content_hash、标记 indexed。
func TestProcessFile_EmptyFileClearsIndex(t *testing.T) {
	s := newFakeStorage()
	m := newTestModuleWithText(s, &fakeLLM{dim: 4}, "")

	f := &contract.FileInfo{ID: 1, RelPath: "empty.md", ContentHash: "old"}
	s.files[1] = f
	// 预置旧索引，验证空文件处理会将其清除
	s.chunks[1] = []*contract.Chunk{{ID: 10, FileID: 1, Seq: 1, Text: "旧内容"}}
	s.vectorCount[10] = 4

	if err := m.ProcessFile(f); err != nil {
		t.Fatalf("ProcessFile(empty) 返回错误: %v", err)
	}

	if len(s.replaceCalls) != 1 {
		t.Fatalf("期望 ChunksReplaceForFile 被调用 1 次，实际 %d", len(s.replaceCalls))
	}
	if !s.replaceCalls[0].isNil {
		t.Fatalf("期望 ChunksReplaceForFile 以 nil 调用清空旧索引，实际 %v", s.replaceCalls[0].chunks)
	}
	if s.replaceCalls[0].fileID != 1 {
		t.Fatalf("期望替换 fileID=1，实际 %d", s.replaceCalls[0].fileID)
	}
	if got := s.statuses[1]; got != "indexed" {
		t.Fatalf("期望状态 indexed，实际 %q", got)
	}
	if f.ContentHash != "ck-1" {
		t.Fatalf("期望 content_hash 更新为 ck-1，实际 %q", f.ContentHash)
	}
	if stored := s.files[1]; stored == nil || stored.ContentHash != "ck-1" {
		t.Fatalf("期望库中 content_hash 更新为 ck-1，实际 %v", stored)
	}
}

// P1-01 失败路径：清空失败应标记 failed 并返回错误。
func TestProcessFile_EmptyFileReplaceFailure(t *testing.T) {
	s := newFakeStorage()
	s.replaceErr = errors.New("db down")
	m := newTestModuleWithText(s, &fakeLLM{dim: 4}, "")

	f := &contract.FileInfo{ID: 1, RelPath: "empty.md", ContentHash: "old"}
	s.files[1] = f

	if err := m.ProcessFile(f); err == nil {
		t.Fatal("期望返回错误，实际 nil")
	} else if !strings.Contains(err.Error(), "清空分块失败") {
		t.Fatalf("错误应包含清空分块失败，实际 %v", err)
	}
	if got := s.statuses[1]; got != "failed" {
		t.Fatalf("期望状态 failed，实际 %q", got)
	}
}

// P1-15：向量数量与分块数量不一致时应返回错误并标记 failed，而非静默跳过。
func TestProcessFile_VectorCardinalityMismatch(t *testing.T) {
	s := newFakeStorage()
	// 文本产生 1 个分块，但 LLM 返回 2 个向量
	m := newTestModule(s, &fakeLLM{dim: 4, vecPerBatch: 2})

	f := &contract.FileInfo{ID: 1, RelPath: "a.txt", ContentHash: "old"}
	s.files[1] = f

	if err := m.ProcessFile(f); err == nil {
		t.Fatal("期望向量基数不匹配错误，实际 nil")
	} else if !strings.Contains(err.Error(), "向量基数不匹配") {
		t.Fatalf("错误应包含向量基数不匹配，实际 %v", err)
	}
	if got := s.statuses[1]; got != "failed" {
		t.Fatalf("期望状态 failed，实际 %q", got)
	}
}

// P1-02：Incremental 不应吞掉 ProcessFile 的错误，错误需向上传播（含路径）。
func TestIncremental_PropagatesProcessError(t *testing.T) {
	s := newFakeStorage()
	m := newTestModule(s, &fakeLLM{err: errors.New("embed 服务不可用")})

	tmpDir := t.TempDir()
	m.workspace = tmpDir
	writeTestFile(t, tmpDir, "a.md", "hello world")

	f := &contract.FileInfo{ID: 1, RelPath: "a.md", DocType: "md", ContentHash: "old"}
	s.files[1] = f
	s.byPath["a.md"] = f

	err := m.Incremental([]string{"a.md"}, nil)
	if err == nil {
		t.Fatal("Incremental 期望返回非 nil 错误，实际 nil")
	}
	if !strings.Contains(err.Error(), "a.md") {
		t.Fatalf("聚合错误应包含路径 a.md，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "嵌入失败") {
		t.Fatalf("聚合错误应包含根因（嵌入失败），实际 %v", err)
	}
}

// P1-02 成功路径：全部成功时返回 nil。
func TestIncremental_AllSuccessReturnsNil(t *testing.T) {
	s := newFakeStorage()
	m := newTestModule(s, &fakeLLM{dim: 4})

	tmpDir := t.TempDir()
	m.workspace = tmpDir
	writeTestFile(t, tmpDir, "a.md", "hello world")

	f := &contract.FileInfo{ID: 1, RelPath: "a.md", DocType: "md", ContentHash: "old"}
	s.files[1] = f
	s.byPath["a.md"] = f

	if err := m.Incremental([]string{"a.md"}, nil); err != nil {
		t.Fatalf("期望 nil，实际 %v", err)
	}
}

// P1-02 删除路径：DeleteFile 失败时错误向上传播。
func TestIncremental_PropagatesDeleteError(t *testing.T) {
	s := newFakeStorage()
	s.replaceErr = errors.New("db down")
	m := newTestModule(s, &fakeLLM{dim: 4})

	f := &contract.FileInfo{ID: 1, RelPath: "gone.md", DocType: "md"}
	s.files[1] = f
	s.byPath["gone.md"] = f

	err := m.Incremental(nil, []string{"gone.md"})
	if err == nil {
		t.Fatal("Incremental 期望返回非 nil 错误，实际 nil")
	}
	if !strings.Contains(err.Error(), "gone.md") {
		t.Fatalf("聚合错误应包含路径 gone.md，实际 %v", err)
	}
}

// P1-15：FullReindex 单文件失败时返回聚合错误（不伪成功）。
func TestFullReindex_ReturnsAggregatedError(t *testing.T) {
	s := newFakeStorage()
	s.replaceErr = errors.New("db down") // 处理任一非空文件时会失败
	m := newTestModule(s, &fakeLLM{dim: 4})

	// 需要一个真实存在的文件供 scanWorkspaceFiles 发现
	tmpDir := t.TempDir()
	m.workspace = tmpDir
	m.events = &fakeEvents{}
	writeTestFile(t, tmpDir, "a.md", "hello world")

	err := m.FullReindex()
	if err == nil {
		t.Fatal("FullReindex 期望返回聚合错误，实际 nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "写入分块失败") && !strings.Contains(msg, "清空分块失败") {
		t.Fatalf("聚合错误应包含处理失败根因，实际 %v", msg)
	}
}

// P1-15：FullReindex 全部成功时返回 nil。
func TestFullReindex_AllSuccessReturnsNil(t *testing.T) {
	s := newFakeStorage()
	m := newTestModule(s, &fakeLLM{dim: 4})

	tmpDir := t.TempDir()
	m.workspace = tmpDir
	m.events = &fakeEvents{}
	writeTestFile(t, tmpDir, "a.md", "hello world")

	if err := m.FullReindex(); err != nil {
		t.Fatalf("期望 nil，实际 %v", err)
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
}
