package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMaxBodyBytesLimit MaxBytesReader + 严格解码：超限 body → 413,正常 body → 200
func TestMaxBodyBytesLimit(t *testing.T) {
	m := &Module{maxBodyBytes: 1 << 10} // 1KB 上限
	handler := m.withProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		if !m.decodeStrictBody(w, r, &req) {
			return
		}
		writeOK(w, req)
	}))

	// 超限
	big := strings.Repeat("a", 2048)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"data":"`+big+`"}`))
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限 body = %d, want 413 (body=%s)", rr.Code, rr.Body.String())
	}

	// 正常
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"data":"ok"}`))
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("正常 body = %d, want 200", rr2.Code)
	}
}

// TestStrictDecoderRejectsUnknownField 严格 decoder：未知字段 → 400
func TestStrictDecoderRejectsUnknownField(t *testing.T) {
	m := &Module{maxBodyBytes: 32 << 20}
	handler := m.withProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Question string `json:"question"`
		}
		if !m.decodeStrictBody(w, r, &req) {
			return
		}
		writeOK(w, req)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(`{"question":"hi","unknownField":1}`))
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("未知字段 = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestWithProtectionRecovery panic → 500,不泄漏 panic 细节,响应头含 X-Request-ID
func TestWithProtectionRecovery(t *testing.T) {
	m := &Module{maxBodyBytes: 32 << 20}
	handler := m.withProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("secret-detail")
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("panic = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret-detail") {
		t.Errorf("响应不应泄漏 panic 细节: %s", rr.Body.String())
	}
	if got := rr.Header().Get("X-Request-ID"); got == "" {
		t.Errorf("panic 响应应含 X-Request-ID")
	}
}

// TestRequestID 不带 X-Request-ID → 中间件生成并回显；带 → 保留客户端值
func TestRequestID(t *testing.T) {
	m := &Module{maxBodyBytes: 32 << 20}
	handler := m.withProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w, map[string]string{"requestID": RequestIDFrom(r)})
	}))

	// 未带 → 生成
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	handler.ServeHTTP(rr, req)
	gen := rr.Header().Get("X-Request-ID")
	if gen == "" {
		t.Fatalf("未带 X-Request-ID 时应生成")
	}
	if !strings.Contains(rr.Body.String(), gen) {
		t.Errorf("context 中的 requestID 应等于响应头, gen=%s body=%s", gen, rr.Body.String())
	}

	// 带 → 保留
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req2.Header.Set("X-Request-ID", "client-supplied-42")
	handler.ServeHTTP(rr2, req2)
	if got := rr2.Header().Get("X-Request-ID"); got != "client-supplied-42" {
		t.Errorf("带 X-Request-ID = %q, want client-supplied-42", got)
	}
	if !strings.Contains(rr2.Body.String(), "client-supplied-42") {
		t.Errorf("context 应保留客户端 requestID, body=%s", rr2.Body.String())
	}
}

// deadlineWriter 模拟真实连接：支持 Flush 与 SetWriteDeadline,
// 超过武装的写截止时间后写操作以 os.ErrDeadlineExceeded 失败。
type deadlineWriter struct {
	mu       sync.Mutex
	header   http.Header
	written  []byte
	deadline time.Time
}

func (d *deadlineWriter) Header() http.Header {
	if d.header == nil {
		d.header = make(http.Header)
	}
	return d.header
}

func (d *deadlineWriter) WriteHeader(int) {}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.deadline.IsZero() && time.Now().After(d.deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	d.written = append(d.written, p...)
	return len(p), nil
}

func (d *deadlineWriter) Flush() {}

func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadline = t
	return nil
}

// TestSSEIdleTimeoutDisconnects SSE 空闲写超时：心跳写因 deadline exceeded 失败后连接断开并清理
func TestSSEIdleTimeoutDisconnects(t *testing.T) {
	oldIdle := sseWriteIdleTimeout
	oldHB := sseHeartbeatInterval
	sseWriteIdleTimeout = 10 * time.Millisecond
	sseHeartbeatInterval = 30 * time.Millisecond
	defer func() {
		sseWriteIdleTimeout = oldIdle
		sseHeartbeatInterval = oldHB
	}()

	m := &Module{
		maxBodyBytes: 32 << 20,
		sseConns:     make(map[chan string]struct{}),
	}

	w := &deadlineWriter{}
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		m.handleSSE(w, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("SSE 连接未在空闲超时后断开")
	}

	m.mu.Lock()
	left := len(m.sseConns)
	m.mu.Unlock()
	if left != 0 {
		t.Errorf("连接关闭后应清理 sseConns, 剩余 %d 个", left)
	}
}
