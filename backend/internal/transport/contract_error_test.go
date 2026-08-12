package transport

// P1-13 契约/错误统一断言：锁定错误响应体形状与 requestId 联动。
// 复用 contract_characterization_test.go 的假实现与基建（newCharFakeHandler/doReq/assertStatus/decodeResp）。

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"memora/internal/contract"
)

// protectedHandler 走生产完整中间件链（withProtection + withCORS），
// 才能验证 X-Request-ID 响应头与响应体 requestId 的联动（测试直连 mux 不经过中间件）。
func protectedHandler(m *Module) http.Handler {
	return m.withProtection(withCORS(m.mux))
}

// doProtected 带可选 X-Request-ID 头经完整中间件链发请求。
func doProtected(m *Module, method, target, body, reqID string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if reqID != "" {
		req.Header.Set("X-Request-ID", reqID)
	}
	rr := httptest.NewRecorder()
	protectedHandler(m).ServeHTTP(rr, req)
	return rr
}

// contractErrorCode 判断 code 是否属于 contract 包定义的稳定错误码。
func contractErrorCode(code string) bool {
	switch code {
	case contract.ErrCodeBadRequest, contract.ErrCodeInvalidParam, contract.ErrCodeNotFound,
		contract.ErrCodeNotConfigured, contract.ErrCodeUnauthorized, contract.ErrCodeForbidden,
		contract.ErrCodeConflict, contract.ErrCodeRateLimited, contract.ErrCodeTimeout,
		contract.ErrCodeCanceled, contract.ErrCodeLLM, contract.ErrCodeExtract, contract.ErrCodeInternal:
		return true
	}
	return false
}

// stableErrorCode 判断 code 是否属于稳定契约码集合：contract 错误码 +
// transport 遗留的非契约码（not_ready/ai_unavailable/request_too_large/stats_disabled/ok），
// 后者具有独立语义与专属 HTTP 状态，行为被 characterization 测试锁定，故列为允许值。
func stableErrorCode(code string) bool {
	if contractErrorCode(code) {
		return true
	}
	switch code {
	case "ok", "stats_disabled", "ai_unavailable", "not_ready", "request_too_large":
		return true
	}
	return false
}

// 遍历多个错误端点：每个错误响应体都必须有非空 code 与 requestId，
// 且 requestId 与 X-Request-ID 头回显一致。
func TestErrorResponseBodyContract(t *testing.T) {
	base := newCharFakeHandler(t, nil)
	suggest := newCharFakeHandler(t, func(h *APIHandler) { h.Timeline = charTimelineErr{} })

	cases := []struct {
		name     string
		m        *Module
		method   string
		target   string
		body     string
		want     int
		wantCode string
		// pureContract 为 true 时额外断言 code 必须落在 contract 错误码集合内
		pureContract bool
	}{
		{"file not found", base, http.MethodGet, "/api/files/999", "", http.StatusNotFound, "not_found", true},
		{"method not allowed", base, http.MethodGet, "/api/workspace/init", "", http.StatusBadRequest, "bad_request", true},
		{"qa empty question", base, http.MethodPost, "/api/qa/stream", `{}`, http.StatusBadRequest, "bad_request", true},
		{"queue not ready", base, http.MethodGet, "/api/queue/status", "", http.StatusServiceUnavailable, "not_ready", false},
		{"ai unavailable", suggest, http.MethodPost, "/api/commits/suggest", `{}`, http.StatusUnprocessableEntity, "ai_unavailable", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reqID := "req-" + c.name
			rr := doProtected(c.m, c.method, c.target, c.body, reqID)
			assertStatus(t, rr, c.want)

			var resp struct {
				Code      string `json:"code"`
				RequestId string `json:"requestId"`
			}
			decodeResp(t, rr, &resp)
			if resp.Code == "" {
				t.Fatalf("错误响应 code 为空: %s", rr.Body.String())
			}
			if resp.Code != c.wantCode {
				t.Fatalf("code = %q, want %q", resp.Code, c.wantCode)
			}
			if !stableErrorCode(resp.Code) {
				t.Fatalf("code = %q 不在稳定错误码集合内", resp.Code)
			}
			if c.pureContract && !contractErrorCode(resp.Code) {
				t.Fatalf("code = %q 应属于 contract 错误码集合", resp.Code)
			}
			if resp.RequestId == "" {
				t.Fatalf("错误响应 requestId 为空: %s", rr.Body.String())
			}
			if got := rr.Header().Get("X-Request-ID"); got != reqID {
				t.Fatalf("X-Request-ID = %q, want %q", got, reqID)
			}
			if resp.RequestId != reqID {
				t.Fatalf("body requestId = %q, want %q（应回显请求头）", resp.RequestId, reqID)
			}
		})
	}
}

// 转换后的 GET /api/files/{id} 不存在：404 + not_found + requestId 回显请求头。
func TestFileDetailNotFoundEchoesRequestID(t *testing.T) {
	m := newCharFakeHandler(t, nil) // charStorage.file == nil

	rr := doProtected(m, http.MethodGet, "/api/files/999", "", "req-file-42")
	assertStatus(t, rr, http.StatusNotFound)

	var resp struct {
		Code      string `json:"code"`
		RequestId string `json:"requestId"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "not_found" {
		t.Fatalf("code = %q, want not_found", resp.Code)
	}
	if resp.RequestId != "req-file-42" {
		t.Fatalf("body requestId = %q, want req-file-42", resp.RequestId)
	}
	if got := rr.Header().Get("X-Request-ID"); got != "req-file-42" {
		t.Fatalf("X-Request-ID = %q, want req-file-42", got)
	}
}

// writeContractError 包装未知错误：code=internal / 500 / "内部错误"，
// 且响应体绝不包含内部错误文本（不泄露 SQL/Go 细节）。
func TestWriteContractErrorHidesCause(t *testing.T) {
	rr := httptest.NewRecorder()
	w := &requestIDWriter{ResponseWriter: rr, reqID: "req-secret-1"}
	writeContractError(w, errors.New("sql: SELECT * FROM secrets; no such table: users"))

	assertStatus(t, rr, http.StatusInternalServerError)
	var resp struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestId string `json:"requestId"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != contract.ErrCodeInternal {
		t.Fatalf("code = %q, want internal", resp.Code)
	}
	if resp.Message != "内部错误" {
		t.Fatalf("message = %q, want 内部错误", resp.Message)
	}
	if resp.RequestId != "req-secret-1" {
		t.Fatalf("requestId = %q, want req-secret-1", resp.RequestId)
	}
	if strings.Contains(rr.Body.String(), "sql:") {
		t.Fatalf("响应体不应泄露内部错误文本: %s", rr.Body.String())
	}
}

// writeContractError 遇到 *contract.AppError 时保留其稳定 code/message/status。
func TestWriteContractErrorPreservesAppError(t *testing.T) {
	rr := httptest.NewRecorder()
	w := &requestIDWriter{ResponseWriter: rr, reqID: "req-1"}
	writeContractError(w, contract.NewAppError(contract.ErrCodeNotFound, "文件不存在", 0))

	assertStatus(t, rr, http.StatusNotFound)
	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != contract.ErrCodeNotFound || resp.Message != "文件不存在" {
		t.Fatalf("AppError 未原样保留: code=%q message=%q", resp.Code, resp.Message)
	}
}

// 成功响应同样携带 requestId，且与 X-Request-ID 头一致。
func TestSuccessResponseCarriesRequestID(t *testing.T) {
	m := newCharFakeHandler(t, nil)

	// 客户端传入 ID → 回显
	rr := doProtected(m, http.MethodGet, "/api/tags", "", "req-tags-7")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code      string `json:"code"`
		RequestId string `json:"requestId"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "ok" {
		t.Fatalf("code = %q, want ok", resp.Code)
	}
	if resp.RequestId != "req-tags-7" {
		t.Fatalf("body requestId = %q, want req-tags-7", resp.RequestId)
	}
	if got := rr.Header().Get("X-Request-ID"); got != "req-tags-7" {
		t.Fatalf("X-Request-ID = %q, want req-tags-7", got)
	}

	// 未传 ID → 中间件生成并同时写入响应头与响应体
	rr = doProtected(m, http.MethodGet, "/api/tags", "", "")
	assertStatus(t, rr, http.StatusOK)
	decodeResp(t, rr, &resp)
	gen := rr.Header().Get("X-Request-ID")
	if gen == "" {
		t.Fatalf("未传 X-Request-ID 时应生成")
	}
	if resp.RequestId == "" {
		t.Fatalf("成功响应 requestId 为空: %s", rr.Body.String())
	}
	if resp.RequestId != gen {
		t.Fatalf("body requestId = %q, want 与响应头一致 %q", resp.RequestId, gen)
	}
}
