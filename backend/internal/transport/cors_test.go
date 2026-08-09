package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// isLocalOrigin 表驱动测试
func TestIsLocalOrigin(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		want   bool
	}{
		{"localhost 无端口", "http://localhost", true},
		{"localhost 有端口", "http://localhost:19000", true},
		{"127.0.0.1", "http://127.0.0.1:19000", true},
		{"127.0.0.1 无端口", "http://127.0.0.1", true},
		{"IPv6", "http://[::1]:19000", true},
		{"https localhost", "https://localhost:19000", true},
		{"子域伪造", "http://localhost.evil.com", false},
		{"userinfo 伪造", "http://evil.com@localhost:19000", false},
		{"非本地域名", "http://evil.com", false},
		{"外部端口", "http://192.168.1.1:19000", false},
		{"ftp 协议", "ftp://localhost", false},
		{"file 协议", "file:///c:/x", false},
		{"Origin null", "null", false},
		{"空串", "", false},
	}
	for _, c := range cases {
		if got := isLocalOrigin(c.origin); got != c.want {
			t.Errorf("isLocalOrigin(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}

// withCORS 中间件：非本地 Origin 403、本地 Origin 回显、无 Origin 放行
func TestWithCORS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	do := func(origin, method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/x", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rr := httptest.NewRecorder()
		withCORS(next).ServeHTTP(rr, req)
		return rr
	}

	// 非本地 Origin → 403，且不调用 next
	rr := do("http://evil.com", http.MethodPost)
	if rr.Code != http.StatusForbidden {
		t.Errorf("非本地 Origin POST = %d, want 403", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("非本地 Origin 不应有 ACAO 头, got %q", got)
	}

	// 本地 Origin → 200 + 回显 ACAO + Vary
	rr = do("http://localhost:19000", http.MethodPost)
	if rr.Code != http.StatusOK {
		t.Errorf("本地 Origin POST = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:19000" {
		t.Errorf("ACAO = %q, want http://localhost:19000", got)
	}
	if got := rr.Header().Get("Vary"); got == "" {
		t.Errorf("应有 Vary: Origin")
	}

	// 无 Origin（同源 / Go 内嵌静态资源）→ 200
	rr = do("", http.MethodGet)
	if rr.Code != http.StatusOK {
		t.Errorf("无 Origin GET = %d, want 200", rr.Code)
	}

	// 非本地 OPTIONS 预检 → 403
	rr = do("http://evil.com", http.MethodOptions)
	if rr.Code != http.StatusForbidden {
		t.Errorf("非本地 OPTIONS = %d, want 403", rr.Code)
	}
}
