package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"memora/internal/logx"
	"net/http"
	"os"
	"time"
)

// SSEEvent SSE 事件
type SSEEvent struct {
	Topic string      `json:"topic"`
	Data  interface{} `json:"data"`
}

// ──────────────────── SSE ────────────────────

// SSE/流式写空闲超时与心跳间隔。包级变量便于测试注入较短值验证超时断开。
var (
	// sseWriteIdleTimeout SSE 事件帧之间的空闲写超时：超时后下一帧写出将以 deadline exceeded 失败并断开
	sseWriteIdleTimeout = 30 * time.Second
	// sseHeartbeatInterval SSE 心跳间隔,须小于 sseWriteIdleTimeout,否则心跳也会触发断开
	sseHeartbeatInterval = 15 * time.Second
	// qaStreamWriteIdleTimeout 流式问答每 chunk 之间的空闲写超时
	qaStreamWriteIdleTimeout = 60 * time.Second
)

// writeStreamFrame 写一个 SSE 帧并刷新,随后武装空闲写超时：
// 下一帧若在超时后才写入,写操作将以 os.ErrDeadlineExceeded 失败,由调用方断开连接。
// 不支持的 ResponseWriter（如 httptest 记录器）忽略 SetWriteDeadline 错误。
func writeStreamFrame(w http.ResponseWriter, flusher http.Flusher, frame string, idle time.Duration) error {
	if _, err := io.WriteString(w, frame); err != nil {
		return err
	}
	flusher.Flush()
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(idle))
	return nil
}

// broadcastSSE 向所有 SSE 连接广播事件
func (m *Module) broadcastSSE(topic string, data interface{}) {
	evt := SSEEvent{Topic: topic, Data: data}
	jsonData, err := json.Marshal(evt)
	if err != nil {
		return
	}

	msg := fmt.Sprintf("data: %s\n\n", string(jsonData))

	m.mu.Lock()
	defer m.mu.Unlock()

	for ch := range m.sseConns {
		select {
		case ch <- msg:
		default:
			// 写失败即移除该连接
			delete(m.sseConns, ch)
			close(ch)
		}
	}
}

// handleSSE SSE 长连接
func (m *Module) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 16)

	m.mu.Lock()
	m.sseConns[ch] = struct{}{}
	m.mu.Unlock()

	// 断开/退出清理
	notify := r.Context().Done()
	defer func() {
		m.mu.Lock()
		delete(m.sseConns, ch)
		m.mu.Unlock()
	}()

	// 心跳
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := writeStreamFrame(w, flusher, msg, sseWriteIdleTimeout); err != nil {
				// 写失败或空闲超时（连接已死）：关闭连接退出
				if errors.Is(err, os.ErrDeadlineExceeded) {
					logx.Warn("transport", "SSE 空闲超时,断开连接")
				}
				return
			}
		case <-ticker.C:
			if err := writeStreamFrame(w, flusher, ": ping\n\n", sseWriteIdleTimeout); err != nil {
				return
			}
		case <-notify:
			return
		}
	}
}
