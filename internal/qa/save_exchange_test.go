package qa

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"memora/internal/contract"
)

// ── 伪存储：实现 qa 包 IStorage，记录 SaveExchange 调用 ──

type fakeStorage struct {
	mu        sync.Mutex
	file      *contract.FileInfo
	chunks    []*contract.Chunk
	saveErr   error
	exchanges []exchangeCall
	notify    []string
	createdAt int64

	// buildContext 检索路径（测试用）：向量检索条目与逐条/批量查询数据
	entries    []contract.VectorEntry
	chunksByID map[int64]*contract.Chunk
	filesByID  map[int64]*contract.FileInfo

	chunksGetCalls int
	filesGetCalls  int
}

type exchangeCall struct {
	sessionID    int64
	mode         string
	fileID       int64
	userMsg      string
	assistantMsg string
	sources      string
}

func (f *fakeStorage) FilesGet(id int64) (*contract.FileInfo, error) {
	f.mu.Lock()
	f.filesGetCalls++
	f.mu.Unlock()
	if f.filesByID != nil {
		return f.filesByID[id], nil
	}
	return f.file, nil
}
func (f *fakeStorage) FilesFindByName(keyword string, limit int) ([]*contract.FileInfo, error) {
	return nil, nil
}
func (f *fakeStorage) ChunksByFile(fileID int64) ([]*contract.Chunk, error) { return f.chunks, nil }
func (f *fakeStorage) ChunksGet(id int64) (*contract.Chunk, error) {
	f.mu.Lock()
	f.chunksGetCalls++
	f.mu.Unlock()
	if f.chunksByID != nil {
		return f.chunksByID[id], nil
	}
	return nil, nil
}
func (f *fakeStorage) VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error) {
	return f.entries, nil
}
func (f *fakeStorage) QASessionsCreate(mode string, fileID int64) (int64, error) { return 1, nil }
func (f *fakeStorage) QASessionsList() ([]*contract.QASession, error)            { return nil, nil }
func (f *fakeStorage) QASessionsDelete(id int64) error                           { return nil }
func (f *fakeStorage) SaveExchange(sessionID int64, mode string, fileID int64, userMsg, assistantMsg, sources string, createdAt int64) (int64, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanges = append(f.exchanges, exchangeCall{
		sessionID: sessionID, mode: mode, fileID: fileID,
		userMsg: userMsg, assistantMsg: assistantMsg, sources: sources,
	})
	if f.saveErr != nil {
		return 0, 0, f.saveErr
	}
	return sessionID, 2, nil
}
func (f *fakeStorage) QAMessagesBySession(sessionID int64) ([]*contract.QAMessage, error) {
	return nil, nil
}

func (f *fakeStorage) calls() []exchangeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]exchangeCall(nil), f.exchanges...)
}

// ── 伪 LLM：实现 qa 包 ILLM ──

type fakeLLM struct {
	answer string
}

func (f *fakeLLM) Chat(system, user string, opts *contract.ChatOptions) (string, error) {
	return f.answer, nil
}
func (f *fakeLLM) ChatStream(system, user string, opts *contract.ChatOptions, cancel <-chan struct{}) (<-chan string, error) {
	ch := make(chan string)
	go func() {
		ch <- f.answer
		close(ch)
	}()
	return ch, nil
}
func (f *fakeLLM) Embed(texts []string) ([][]float32, error) { return nil, nil }
func (f *fakeLLM) EmbedQuery(text string) ([]float32, error) { return nil, nil }
func (f *fakeLLM) Rerank(query string, docs []string) ([]float64, error) {
	return nil, errors.New("not configured")
}

// ── 伪事件：记录 qa_ready ──

type fakeEvents struct {
	mu    sync.Mutex
	ready int
}

func (f *fakeEvents) Notify(topic string, data interface{}) {
	if topic == "qa_ready" {
		f.mu.Lock()
		f.ready++
		f.mu.Unlock()
	}
}

func (f *fakeEvents) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func newFakeModule(st IStorage, llm ILLM, ev *fakeEvents) *Module {
	if llm == nil {
		llm = &fakeLLM{answer: "测试回答"}
	}
	return New(st, llm, ev, 30000)
}

// 单文件 small chunk → 走"全文直发"路径，不发嵌入请求（离线）
func newFileStorage() *fakeStorage {
	return &fakeStorage{
		file:   &contract.FileInfo{ID: 1, RelPath: "test.md"},
		chunks: []*contract.Chunk{{ID: 1, FileID: 1, Seq: 1, Text: "这是测试文档的内容。"}},
	}
}

// SaveExchange 失败：Ask 必须返回错误（不得成功），且不得广播 qa_ready。
func TestAskSaveExchangeErrorReturnsErrorNoNotify(t *testing.T) {
	st := newFileStorage()
	st.saveErr = errors.New("boom")
	ev := &fakeEvents{}
	m := newFakeModule(st, nil, ev)

	_, err := m.Ask(&contract.QARequest{Question: "测试问题", Mode: "file", FileID: 1})
	if err == nil {
		t.Fatalf("Ask 应返回错误，却返回成功")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("错误未包含根因: %v", err)
	}
	if ev.count() != 0 {
		t.Fatalf("失败路径不应广播 qa_ready，实际广播 %d 次", ev.count())
	}
}

// Ask 成功：SaveExchange 应以正确参数被调用（userMsg/assistantMsg/sources）。
func TestAskSaveExchangeRecordsCorrectArguments(t *testing.T) {
	st := newFileStorage()
	ev := &fakeEvents{}
	m := newFakeModule(st, nil, ev)

	resp, err := m.Ask(&contract.QARequest{Question: "测试问题", Mode: "file", FileID: 1})
	if err != nil {
		t.Fatalf("Ask 失败: %v", err)
	}
	if resp.Answer != "测试回答" {
		t.Fatalf("answer=%q", resp.Answer)
	}
	if ev.count() != 1 {
		t.Fatalf("成功路径应广播 1 次 qa_ready，实际 %d", ev.count())
	}

	calls := st.calls()
	if len(calls) != 1 {
		t.Fatalf("SaveExchange 应恰好调用 1 次，实际 %d", len(calls))
	}
	c := calls[0]
	if c.mode != "file" || c.fileID != 1 {
		t.Fatalf("mode/fileID 错误: %+v", c)
	}
	if c.userMsg != "测试问题" {
		t.Fatalf("userMsg=%q", c.userMsg)
	}
	if c.assistantMsg != "测试回答" {
		t.Fatalf("assistantMsg=%q", c.assistantMsg)
	}
	// sources 应为"未找到"走 json.Marshal 的序列化结果（含 test.md）
	if !strings.Contains(c.sources, "test.md") {
		t.Fatalf("sources 未含来源路径: %q", c.sources)
	}
}

// Ask 空上下文短路：SaveExchange 失败时 Ask 必须返回错误且不广播 qa_ready。
// 用全局模式触发空上下文（无向量检索结果 → 语境为空，走短路分支）。
func TestAskEmptyContextSaveExchangeError(t *testing.T) {
	st := &fakeStorage{}
	st.saveErr = errors.New("boom")
	ev := &fakeEvents{}
	m := newFakeModule(st, nil, ev)

	_, err := m.Ask(&contract.QARequest{Question: "测试问题", Mode: "global"})
	if err == nil {
		t.Fatalf("空上下文 + 保存失败应返回错误，却返回成功")
	}
	if ev.count() != 0 {
		t.Fatalf("失败路径不应广播 qa_ready，实际 %d", ev.count())
	}
}

// AskStream：SaveExchange 失败时 done 必须携带 Error（无 Answer），
// 且不得广播 qa_ready。
func TestAskStreamSaveExchangeError(t *testing.T) {
	st := newFileStorage()
	st.saveErr = errors.New("boom")
	ev := &fakeEvents{}
	m := newFakeModule(st, nil, ev)

	ch, done := m.AskStream(
		&contract.QARequest{Question: "测试问题", Mode: "file", FileID: 1},
		make(chan struct{}),
	)
	// 消费增量，避免 goroutine 阻塞
	for range ch {
	}
	res := <-done
	if res.Error == "" {
		t.Fatalf("done 应携带 Error，实际为空")
	}
	if res.Answer != "" {
		t.Fatalf("失败路径不应有 Answer: %q", res.Answer)
	}
	if !strings.Contains(res.Error, "boom") {
		t.Fatalf("Error 未含根因: %v", res.Error)
	}
	if ev.count() != 0 {
		t.Fatalf("失败路径不应广播 qa_ready，实际 %d", ev.count())
	}

	if len(st.calls()) != 1 {
		t.Fatalf("SaveExchange 应恰好调用 1 次，实际 %d", len(st.calls()))
	}
}

// AskStream 空上下文短路：SaveExchange 失败时 done 携带 Error。
// 用全局模式触发空上下文（无向量检索结果 → 语境为空，走短路分支）。
func TestAskStreamEmptyContextSaveExchangeError(t *testing.T) {
	st := &fakeStorage{}
	st.saveErr = errors.New("boom")
	ev := &fakeEvents{}
	m := newFakeModule(st, nil, ev)

	ch, done := m.AskStream(
		&contract.QARequest{Question: "测试问题", Mode: "global"},
		make(chan struct{}),
	)
	for range ch {
	}
	res := <-done
	if res.Error == "" {
		t.Fatalf("done 应携带 Error")
	}
	if !strings.Contains(res.Error, "boom") {
		t.Fatalf("Error 未含根因: %v", res.Error)
	}
	if ev.count() != 0 {
		t.Fatalf("失败路径不应广播 qa_ready，实际 %d", ev.count())
	}
}
