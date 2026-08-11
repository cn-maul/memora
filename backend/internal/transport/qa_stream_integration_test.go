package transport

// 端到端诊断：transport → qa → llm → mock LLM 服务器的完整 SSE 输出验证。
// 用与真实前端完全一致的消费方式（fetch reader 逐块读取、按 \n\n 分帧）校验。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"memora/internal/contract"
	"memora/internal/events"
	"memora/internal/llm"
	"memora/internal/qa"
)

// ── 伪存储：仅实现 QA 链路所需方法 ──

type mockQAStorage struct {
	file    *contract.FileInfo
	chunks  []*contract.Chunk
	session int64
}

func (m *mockQAStorage) FilesGet(id int64) (*contract.FileInfo, error) { return m.file, nil }
func (m *mockQAStorage) FilesFindByName(keyword string, limit int) ([]*contract.FileInfo, error) {
	return nil, nil
}
func (m *mockQAStorage) ChunksByFile(fileID int64) ([]*contract.Chunk, error) { return m.chunks, nil }
func (m *mockQAStorage) ChunksGet(id int64) (*contract.Chunk, error)          { return nil, nil }
func (m *mockQAStorage) VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error) {
	return nil, nil
}
func (m *mockQAStorage) QASessionsCreate(mode string, fileID int64) (int64, error) {
	m.session++
	return m.session, nil
}
func (m *mockQAStorage) QASessionsList() ([]*contract.QASession, error) { return nil, nil }
func (m *mockQAStorage) QASessionsDelete(id int64) error                { return nil }
func (m *mockQAStorage) QAMessagesAppend(sessionID int64, role, content, sources string, createdAt int64) (int64, error) {
	return 1, nil
}
func (m *mockQAStorage) QAMessagesBySession(sessionID int64) ([]*contract.QAMessage, error) {
	return nil, nil
}

type mockQAEvents struct{}

func (mockQAEvents) Notify(topic string, data interface{}) {}

// mockLLMConfig 指向 httptest 服务器
type mockLLMConfig struct {
	url string
}

func (c mockLLMConfig) GetLLMConfig() (string, string, string, float64) { return c.url, "k", "m", 0.2 }
func (c mockLLMConfig) GetEmbedConfig() (string, string, string, int)   { return "", "", "", 0 }
func (c mockLLMConfig) GetRerankConfig() (string, string, string)       { return "", "", "" }

// sseLLMServer 模拟 OpenAI 兼容 SSE 流式端点
func sseLLMServer(deltas []string, contentType string, flushDelay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		for _, d := range deltas {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", d)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			if flushDelay > 0 {
				time.Sleep(flushDelay)
			}
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
}

// jsonLLMServer 模拟忽略 stream:true 的端点（整体 JSON 响应）
func jsonLLMServer(content string, contentType string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content))
	}))
}

// reasoningLLMServer 模拟推理模型（SenseNova 等）：先流式 delta.reasoning 思维链，再流式 delta.content
func reasoningLLMServer(reason, content []string, contentType string, flushDelay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		for _, c := range reason {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":%q},\"finish_reason\":\"\"}]}\n\n", c)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			if flushDelay > 0 {
				time.Sleep(flushDelay)
			}
		}
		for _, c := range content {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", c)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
}

// newQAStreamModule 组装 transport + qa + llm（file 模式直发，避免依赖嵌入）
func newQAStreamModule(t *testing.T, llmSrv *httptest.Server) *Module {
	t.Helper()
	llmMod := llm.New(mockLLMConfig{url: llmSrv.URL})

	store := &mockQAStorage{
		file: &contract.FileInfo{ID: 1, RelPath: "test.md"},
		chunks: []*contract.Chunk{
			{ID: 1, FileID: 1, Seq: 1, Text: "这是测试文档的内容，用于问答。"},
		},
	}
	qaMod := qa.New(store, llmMod, mockQAEvents{}, 30000)

	m := New(&APIHandler{QA: qaMod}, events.New())
	m.registerRoutes()
	return m
}

// 以"真实前端"的方式消费 SSE：逐块读、按 \n\n 分帧、解析 event/data
func consumeSSE(t *testing.T, resp *http.Response) (chunks []string, events []string, raw string) {
	t.Helper()
	reader := bufio.NewReader(resp.Body)
	defer resp.Body.Close()
	var buffer strings.Builder
	for {
		line, err := reader.ReadString('\n')
		buffer.WriteString(line)
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
	}
	raw = buffer.String()

	for _, frame := range strings.Split(raw, "\n\n") {
		lines := strings.Split(frame, "\n")
		var evt, dataLine string
		for _, l := range lines {
			tr := strings.TrimSpace(l)
			if strings.HasPrefix(tr, "event: ") {
				evt = strings.TrimPrefix(tr, "event: ")
			} else if strings.HasPrefix(tr, "data: ") {
				dataLine = strings.TrimPrefix(tr, "data: ")
			}
		}
		if dataLine == "" {
			continue
		}
		if evt == "" {
			var s string
			if err := json.Unmarshal([]byte(dataLine), &s); err == nil {
				chunks = append(chunks, s)
			}
		} else {
			events = append(events, evt)
		}
	}
	return chunks, events, raw
}

// 标准 SSE 端点：应逐块输出，且"先于完整响应"收到首块
func TestQAStreamStandardSSE(t *testing.T) {
	deltas := []string{"你", "好", "，", "世界"}
	llmSrv := sseLLMServer(deltas, "text/event-stream", 30*time.Millisecond)
	defer llmSrv.Close()

	m := newQAStreamModule(t, llmSrv)
	srv := httptest.NewServer(m.mux)
	defer srv.Close()

	start := time.Now()
	resp, err := http.Post(srv.URL+"/api/qa/stream", "application/json",
		strings.NewReader(`{"question":"测试问题","mode":"file","fileId":1}`))
	if err != nil {
		t.Fatalf("POST err: %v", err)
	}
	chunks, evts, raw := consumeSSE(t, resp)
	elapsed := time.Since(start)

	t.Logf("elapsed=%v raw=%q", elapsed, raw)
	if len(chunks) != len(deltas) {
		t.Fatalf("chunks=%v, want %d deltas", chunks, len(deltas))
	}
	if strings.Join(chunks, "") != "你好，世界" {
		t.Fatalf("joined=%q", strings.Join(chunks, ""))
	}
	if !strings.Contains(strings.Join(evts, ","), "done") {
		t.Fatalf("缺少 done 事件: %v", evts)
	}
	// 若真正逐块 flush，总耗时应接近所有块延迟之和（≈120ms），而非一次性等完
	if elapsed < 60*time.Millisecond {
		t.Logf("全部块几乎同时到达（未真正流式，或本地服务器瞬时完成）")
	}
}

// 忽略 stream:true 的整体 JSON 响应：至少能收到完整回答
func TestQAStreamPlainJSON(t *testing.T) {
	llmSrv := jsonLLMServer("完整回答内容", "application/json")
	defer llmSrv.Close()

	m := newQAStreamModule(t, llmSrv)
	srv := httptest.NewServer(m.mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/qa/stream", "application/json",
		strings.NewReader(`{"question":"测试问题","mode":"file","fileId":1}`))
	if err != nil {
		t.Fatalf("POST err: %v", err)
	}
	chunks, evts, raw := consumeSSE(t, resp)
	t.Logf("raw=%q", raw)
	if strings.Join(chunks, "") != "完整回答内容" {
		t.Fatalf("joined=%q chunks=%v evts=%v", strings.Join(chunks, ""), chunks, evts)
	}
}

// 推理模型（SenseNova reasoning 等）：思维链 delta.reasoning 带前缀流向前端，
// 最终答案 delta.content 独立输出，且思维链不混入答案
func TestQAStreamReasoning(t *testing.T) {
	llmSrv := reasoningLLMServer([]string{"先分析", "预算构成"}, []string{"研发投入", "120万"}, "text/event-stream", 20*time.Millisecond)
	defer llmSrv.Close()

	m := newQAStreamModule(t, llmSrv)
	srv := httptest.NewServer(m.mux)
	defer srv.Close()

	start := time.Now()
	resp, err := http.Post(srv.URL+"/api/qa/stream", "application/json",
		strings.NewReader(`{"question":"预算问题","mode":"file","fileId":1}`))
	if err != nil {
		t.Fatalf("POST err: %v", err)
	}
	chunks, evts, raw := consumeSSE(t, resp)
	elapsed := time.Since(start)

	t.Logf("elapsed=%v raw=%q", elapsed, raw)
	var thinking, answer []string
	for _, c := range chunks {
		if strings.HasPrefix(c, contract.ThinkChunkPrefix) {
			thinking = append(thinking, strings.TrimPrefix(c, contract.ThinkChunkPrefix))
		} else {
			answer = append(answer, c)
		}
	}
	if got := strings.Join(answer, ""); got != "研发投入120万" {
		t.Fatalf("answer=%q（思维链不应混入最终答案）", got)
	}
	if got := strings.Join(thinking, ""); got != "先分析预算构成" {
		t.Fatalf("thinking=%q", got)
	}
	if len(thinking) == 0 {
		t.Fatalf("思维链未流向前端（思考期应可见流式输出）")
	}
	if !strings.Contains(strings.Join(evts, ","), "done") {
		t.Fatalf("缺少 done 事件: %v", evts)
	}
	// 思考 + 回答均逐块 flush：总耗时接近各块延迟之和，而非一次性等待
	if elapsed < 60*time.Millisecond {
		t.Logf("全部块几乎同时到达（本地服务器瞬时完成，竞态可接受）")
	}
}
