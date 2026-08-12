package llm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 复用 llm_stream_test.go 的 fakeConfig。

// awaitClientOrTimeout 等待客户端断开（最多 1s），超时则返回。
// 避免服务端 handler 永久阻塞在 r.Context().Done()（部分平台对端关闭
// 未必及时取消请求上下文），从而卡住 httptest.Server.Close。
func awaitClientOrTimeout(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(time.Second):
	}
}

// TestTransportTimeoutFields 结构校验：New 创建的 Transport 必须显式带上
// Dial/TLS/ResponseHeader 三项超时，且流式与非流式共享同一连接池。
func TestTransportTimeoutFields(t *testing.T) {
	m := New(&fakeConfig{})
	tr, ok := m.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("httpClient.Transport 不是 *http.Transport")
	}
	if tr.DialContext == nil {
		t.Fatal("DialContext 未设置：TCP 建连无超时上限")
	}
	if tr.TLSHandshakeTimeout <= 0 {
		t.Fatal("TLSHandshakeTimeout 未设置")
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatal("ResponseHeaderTimeout 未设置：对端只建连不响应头时会永久挂")
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Fatalf("IdleConnTimeout = %v，期望 90s", tr.IdleConnTimeout)
	}
	// 流式客户端应共享同一传输（连接池复用语义），从而同样获得 ResponseHeaderTimeout 兜底
	if m.streamClient.Transport != m.httpClient.Transport {
		t.Fatal("streamClient 与 httpClient 未共享 Transport")
	}
	if m.streamClient.Timeout != 0 {
		t.Fatalf("streamClient.Timeout = %v，期望 0（无整体超时，长回答不被截断）", m.streamClient.Timeout)
	}
}

// TestNonStreamResponseHeaderTimeout 非流式请求：对端只建立 TCP 连接但永不写响应头，
// 应在 ResponseHeaderTimeout（测试中注入 80ms）后返回超时错误，而非永久悬挂。
func TestNonStreamResponseHeaderTimeout(t *testing.T) {
	old := responseHeaderTimeout
	responseHeaderTimeout = 80 * time.Millisecond
	t.Cleanup(func() { responseHeaderTimeout = old })

	// 不写任何响应头，只等到客户端超时断开
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		awaitClientOrTimeout(r)
	}))
	defer srv.Close()

	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})
	start := time.Now()
	_, err := m.doRequest("POST", srv.URL, map[string]interface{}{"model": "test"}, "key")
	if err == nil {
		t.Fatal("对端不响应头时应返回错误")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("应在有限时间返回，耗时 %v", elapsed)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("期望超时错误，实际: %v", err)
	}
	t.Logf("响应头超时: %v (%v)", err, time.Since(start))
}

// TestNonStreamBodyReadTimeout 非流式请求：对端已发响应头但 body 永不完结，
// 应由 doRequest 的请求级超时 context（注入 80ms）按时返回，而非永久悬挂。
func TestNonStreamBodyReadTimeout(t *testing.T) {
	old := requestTimeout
	requestTimeout = 80 * time.Millisecond
	t.Cleanup(func() { requestTimeout = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		awaitClientOrTimeout(r) // 响应体永不结束，直到客户端超时断开
	}))
	defer srv.Close()

	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})
	start := time.Now()
	_, err := m.doRequest("POST", srv.URL, map[string]interface{}{"model": "test"}, "key")
	if err == nil {
		t.Fatal("body 永不结束时应返回错误")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("应在有限时间返回，耗时 %v", elapsed)
	}
	if !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("期望超时错误，实际: %v", err)
	}
	t.Logf("body 读取超时: %v (%v)", err, time.Since(start))
}

// TestChatStreamCancelUnblocksRead cancel 中断流式读：服务端发一块后停推
// （客户端读阻塞在 resp.Body.Read），关闭 cancel 应尽快结束流，不悬挂。
func TestChatStreamCancelUnblocksRead(t *testing.T) {
	cancel := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// 发一块后停推：制造读侧阻塞点，直到客户端断开
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"第一块\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		awaitClientOrTimeout(r) // 停推直到客户端断开
	}))
	defer srv.Close()

	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})
	ch, err := m.ChatStream("sys", "user", nil, cancel)
	if err != nil {
		t.Fatalf("ChatStream err: %v", err)
	}

	// 先确认收到首块内容
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("流异常提前结束")
		}
		if strings.HasPrefix(c, "__ERROR__:") {
			t.Fatalf("流式错误: %s", c)
		}
		t.Logf("收到首块: %q", c)
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内未收到任何流式内容")
	}

	// 服务端已停推，读侧阻塞——关闭 cancel 应解除阻塞、流在短时间结束
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	close(cancel)
	select {
	case <-done:
		t.Log("cancel 后流在短时间内结束，未悬挂")
	case <-time.After(2 * time.Second):
		t.Fatal("流式 goroutine 悬挂：cancel 后 ch 未关闭")
	}
}

// TestChatStreamReadIdleTimeout 流式读侧 idle 超时：服务端发完响应头后永久停推，
// 由 idleReadCloser（注入 100ms）中断阻塞的 Read，流应在有限时间结束而非悬挂。
func TestChatStreamReadIdleTimeout(t *testing.T) {
	old := streamReadIdleTimeout
	streamReadIdleTimeout = 100 * time.Millisecond
	t.Cleanup(func() { streamReadIdleTimeout = old })

	cancel := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		awaitClientOrTimeout(r) // 发完头后停推，直到客户端超时/取消断开
	}))
	defer srv.Close()

	m := New(&fakeConfig{llmBaseURL: srv.URL, llmModel: "test-model", llmTemp: 0.2})
	ch, err := m.ChatStream("sys", "user", nil, cancel)
	if err != nil {
		t.Fatalf("ChatStream err: %v", err)
	}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("读侧空闲超时应尽快结束，耗时 %v", elapsed)
		}
		t.Logf("流在读侧空闲超时后结束，耗时 %v", time.Since(start))
	case <-time.After(3 * time.Second):
		t.Fatal("读侧空闲超时未生效：流永久悬挂")
	}
}
