package transport

import (
	"context"
	"encoding/base64"
	"fmt"
	"memora/internal/logx"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RequestIDFrom 从请求 context 读取 request ID（由 withProtection 注入）。
// 未注入时返回空串。
func RequestIDFrom(r *http.Request) string {
	if id, ok := r.Context().Value(ctxKeyRequestID).(string); ok {
		return id
	}
	return ""
}

// requestIDProvider 由 requestIDWriter 实现，供 writeJSON 从写侧回填 requestId。
type requestIDProvider interface {
	requestID() string
}

// requestIDWriter 包装 http.ResponseWriter，记录本次请求的 request ID，
// 使 writeJSON 无需持有请求对象即可把 requestId 注入响应体（避免改动 120 处写点）。
type requestIDWriter struct {
	http.ResponseWriter
	reqID string
}

func (w *requestIDWriter) requestID() string { return w.reqID }

// Unwrap 供 http.ResponseController 剥开包装找到底层写器（SetWriteDeadline 等）。
func (w *requestIDWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush 透传底层 Flusher：SSE / 流式问答依赖 w.(http.Flusher) 断言。
func (w *requestIDWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// newRequestID 生成 request ID：base64(时间戳纳秒),无需外部依赖,单调且碰撞概率极低
func newRequestID() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
}

// withProtection HTTP 安全中间件：链式执行
//  1. request ID：取客户端传入或生成,回写响应头并存入 context（供 handler/recovery 读取），
//     同时用 requestIDWriter 包装 w,使 writeJSON 把 requestId 注入每个响应体
//  2. recovery：捕获下游（CORS/路由/处理器）panic → 500,不向客户端回吐 panic 细节
//  3. body 限制：对每个请求套 MaxBytesReader,超限读时触发 MaxBytesError
//
// recovery 为最外层,保证 CORS 与各处理器内的 panic 都能被兜底。
func (m *Module) withProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1) request ID
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		ww := &requestIDWriter{ResponseWriter: w, reqID: id}
		ww.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		r = r.WithContext(ctx)

		// 2) recovery
		defer func() {
			if rec := recover(); rec != nil {
				logx.Error("transport", "handler panic", "requestID", id, "path", r.URL.Path, "panic", fmt.Sprintf("%v", rec))
				writeError(ww, "internal", "服务器内部错误", http.StatusInternalServerError)
			}
		}()

		// 3) body 大小限制
		r.Body = http.MaxBytesReader(ww, r.Body, m.maxBodyBytes)

		next.ServeHTTP(ww, r)
	})
}

// withCORS CORS 中间件
// 服务仅监听 127.0.0.1,但 CORS `*` 会让任意外部网页通过浏览器 fetch 静默读取本地 API（文档全文等）。
// 修复：仅回显 localhost/127.0.0.1 来源的 Origin；无 Origin 的同源请求（Go 内嵌静态资源）直接放行；
// 外部来源不设置 CORS 头,浏览器将拦截响应。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if origin != "" {
			if !isLocalOrigin(origin) {
				// 非本地来源：拒绝所有跨域请求（CSRF 防护）。
				// 浏览器对带 Origin 的跨域 POST 均带此头；外部网页 form/no-cors 也会带,
				// 直接拒绝可阻断对本地 API 的副作用调用（commits/auto、queue/pause 等无 body 端点）。
				w.WriteHeader(http.StatusForbidden)
				return
			}
			// 回显式 ACAO：声明 Vary: Origin,防止缓存把带某 Origin 的响应复用于其他来源
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isLocalOrigin 判断 Origin 是否为本机来源（localhost / 127.0.0.1 / ::1,任意端口）
func isLocalOrigin(origin string) bool {
	if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// 拒绝带 userinfo 的构造（如 http://evil.com@localhost）,浏览器 Origin 序列化从不含 userinfo
	if u.User != nil {
		return false
	}
	host := u.Hostname()
	// URL.Hostname() 对 IPv6 返回无括号形式（::1）,不匹配 "[::1]"
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
