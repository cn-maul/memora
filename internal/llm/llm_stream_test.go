package llm

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"memora/internal/contract"
)

type fakeConfig struct {
	llmBaseURL, llmKey, llmModel string
	llmTemp                      float64
}

func (f *fakeConfig) GetLLMConfig() (string, string, string, float64) {
	return f.llmBaseURL, f.llmKey, f.llmModel, f.llmTemp
}
func (f *fakeConfig) GetEmbedConfig() (string, string, string, int) { return "", "", "", 0 }
func (f *fakeConfig) GetRerankConfig() (string, string, string)     { return "", "", "" }

func testServer(t *testing.T, contentType string, body string, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		if delay > 0 {
			for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
				fmt.Fprintf(w, "%s\n", line)
				fl, _ := w.(http.Flusher)
				fl.Flush()
				time.Sleep(delay)
			}
		} else {
			io.WriteString(w, body)
		}
		fl, _ := w.(http.Flusher)
		fl.Flush()
	}))
}

func collectChunks(t *testing.T, m *Module, cancel chan struct{}) (string, error) {
	t.Helper()
	ch, err := m.ChatStream("sys", "user", nil, cancel)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for c := range ch {
		if strings.HasPrefix(c, "__ERROR__:") {
			return "", fmt.Errorf("%s", strings.TrimPrefix(c, "__ERROR__:"))
		}
		sb.WriteString(c)
	}
	return sb.String(), nil
}

func sseBody(deltaChunks []string) string {
	var b strings.Builder
	for _, c := range deltaChunks {
		fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", c)
	}
	b.WriteString("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// reasoningBody 推理模型（SenseNova/DeepSeek-R1 等）流式响应：
// 先整段输出 delta.reasoning 思维链，最后才输出 delta.content 最终答案。
func reasoningBody(reasonChunks, contentChunks []string) string {
	var b strings.Builder
	for _, c := range reasonChunks {
		fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{\"reasoning\":%q},\"finish_reason\":\"\"}]}\n\n", c)
	}
	for _, c := range contentChunks {
		fmt.Fprintf(&b, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", c)
	}
	b.WriteString("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// 标准 SSE 端点：应逐块返回内容
func TestChatStreamStandardSSE(t *testing.T) {
	cancel := make(chan struct{})
	srv := testServer(t, "text/event-stream", sseBody([]string{"你", "好", "，", "世界"}), 5*time.Millisecond)
	defer srv.Close()

	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})
	ch, err := m.ChatStream("sys", "user", nil, cancel)
	if err != nil {
		t.Fatalf("ChatStream err: %v", err)
	}
	var parts []string
	for c := range ch {
		if strings.HasPrefix(c, "__ERROR__:") {
			t.Fatalf("stream error: %s", c)
		}
		parts = append(parts, c)
	}
	joined := strings.Join(parts, "")
	if joined != "你好，世界" {
		t.Fatalf("got %q, want %q (parts=%v)", joined, "你好，世界", parts)
	}
}

// 端点忽略 stream:true，返回普通 JSON
func TestChatStreamNonSSE(t *testing.T) {
	cancel := make(chan struct{})
	body := `{"choices":[{"message":{"role":"assistant","content":"完整回答"},"finish_reason":"stop"}]}`
	srv := testServer(t, "application/json", body, 0)
	defer srv.Close()

	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})
	out, err := collectChunks(t, m, cancel)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "完整回答" {
		t.Fatalf("got %q, want %q", out, "完整回答")
	}
}

// 处理中途：客户端取消应尽快中断读取并正常返回已收内容
func TestChatStreamCancelMidStream(t *testing.T) {
	parts := []string{"第", "一", "段", "内", "容"}
	srv := testServer(t, "text/event-stream", sseBody(parts), 5*time.Millisecond)
	defer srv.Close()

	cancel := make(chan struct{})
	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})

	ch, err := m.ChatStream("sys", "user", nil, cancel)
	if err != nil {
		t.Fatalf("ChatStream err: %v", err)
	}

	var got []string
	for c := range ch {
		got = append(got, c)
		// 收到 2 块后取消
		if len(got) == 2 {
			close(cancel)
		}
	}
	t.Logf("cancel 后收到 %d 块: %v", len(got), got)
	if len(got) == 0 {
		t.Fatalf("cancel 前应至少收到内容")
	}
	// 触发取消后流应已结束（不悬挂）
	if len(got) == len(parts) {
		t.Logf("全部块在取消前已到达（竞态结果，可接受）")
	}
}

// Content-Type 不是 text/event-stream 但实际是 SSE 流（分块）时，不应丢弃内容
func TestChatStreamCTJsonButStreams(t *testing.T) {
	cancel := make(chan struct{})
	// 端点实际按块返回，但 Content-Type 声明为 application/json
	parts := []string{"你", "好", "，", "世界"}
	srv := testServer(t, "application/json", sseBody(parts), 5*time.Millisecond)
	defer srv.Close()

	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})
	ch, err := m.ChatStream("sys", "user", nil, cancel)
	if err != nil {
		t.Fatalf("ChatStream err: %v", err)
	}
	var got []string
	for c := range ch {
		got = append(got, c)
	}
	t.Logf("received %d chunks: %v", len(got), got)
	if len(got) == 0 {
		t.Fatalf("stream returned nothing")
	}
}

// 推理模型流式：delta.reasoning 思维链带前缀发往 ch，delta.content 正常发出，
// 二者不应互相污染（回答里不含思维链文本）
func TestChatStreamReasoningDelta(t *testing.T) {
	cancel := make(chan struct{})
	reason := []string{"用户", "问的是", "预算", "数字"}
	content := []string{"研发投入", "**120万**", "，增长", "20%"}
	srv := testServer(t, "text/event-stream", reasoningBody(reason, content), 2*time.Millisecond)
	defer srv.Close()

	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})
	ch, err := m.ChatStream("sys", "user", nil, cancel)
	if err != nil {
		t.Fatalf("ChatStream err: %v", err)
	}

	var thinking []string
	var answer []string
	for c := range ch {
		if strings.HasPrefix(c, contract.ThinkChunkPrefix) {
			thinking = append(thinking, strings.TrimPrefix(c, contract.ThinkChunkPrefix))
		} else {
			answer = append(answer, c)
		}
	}
	t.Logf("thinking=%v answer=%v", thinking, answer)
	if got := strings.Join(answer, ""); got != "研发投入**120万**，增长20%" {
		t.Fatalf("answer=%q, want 思维链之后的最终内容", got)
	}
	if got := strings.Join(thinking, ""); got != "用户问的是预算数字" {
		t.Fatalf("thinking=%q, want 思维链内容", got)
	}
}

// 推理模型流式：Content-Type 错误标注为 application/json 时，思维链与答案都要恢复
func TestChatStreamReasoningCTJson(t *testing.T) {
	cancel := make(chan struct{})
	reason := []string{"先分析", "问题结构"}
	content := []string{"最终答案", "内容"}
	srv := testServer(t, "application/json", reasoningBody(reason, content), 2*time.Millisecond)
	defer srv.Close()

	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})
	ch, err := m.ChatStream("sys", "user", nil, cancel)
	if err != nil {
		t.Fatalf("ChatStream err: %v", err)
	}
	var thinking []string
	var answer []string
	for c := range ch {
		if strings.HasPrefix(c, contract.ThinkChunkPrefix) {
			thinking = append(thinking, strings.TrimPrefix(c, contract.ThinkChunkPrefix))
		} else {
			answer = append(answer, c)
		}
	}
	if got := strings.Join(answer, ""); got != "最终答案内容" {
		t.Fatalf("answer=%q", got)
	}
	if got := strings.Join(thinking, ""); got != "先分析问题结构" {
		t.Fatalf("thinking=%q", got)
	}
}
