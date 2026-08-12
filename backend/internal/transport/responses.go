package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"memora/internal/contract"
	"memora/internal/logx"
	"net/http"
	"strconv"
	"strings"
)

// Response 统一响应
type Response struct {
	Code      string      `json:"code"`
	Data      interface{} `json:"data,omitempty"`
	Message   string      `json:"message,omitempty"`
	RequestId string      `json:"requestId,omitempty"`
}

// ──────────────────── 辅助 ────────────────────

// writeJSON 写入 JSON 响应
func writeJSON(w http.ResponseWriter, code int, resp *Response) {
	// 从 withProtection 注入的包装写器读 request ID 回填响应体；
	// 未包装（测试直连 mux 等）时按需生成，保证请求级可追踪。
	if p, ok := w.(requestIDProvider); ok && p.requestID() != "" {
		resp.RequestId = p.requestID()
	}
	if resp.RequestId == "" {
		resp.RequestId = newRequestID()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(resp)
}

// writeOK 写入成功响应
func writeOK(w http.ResponseWriter, data interface{}) {
	writeJSON(w, http.StatusOK, &Response{Code: "ok", Data: data})
}

// writeError 写入错误响应
func writeError(w http.ResponseWriter, code string, message string, httpCode int, data ...interface{}) {
	var dataVal interface{}
	if len(data) > 0 {
		dataVal = data[0]
	}
	writeJSON(w, httpCode, &Response{Code: code, Message: message, Data: dataVal})
}

// wrapErr 构造契约错误：稳定 code + 面向用户的中文消息（不携带内部错误文本），
// 原始错误仅作为内部 cause 挂在错误对象上，供日志排查，不序列化。
func wrapErr(code, message string, err error) *contract.AppError {
	return contract.NewAppError(code, message, 0).WithCause(err)
}

// writeContractError 基于契约错误模型（contract）写错误响应：
//   - 未知错误经 contract.AsAppError 归一为 code=internal / 500 / "内部错误"，
//     内部 cause 仅保留在错误对象（供日志），不泄露 SQL/Go 细节到响应体；
//   - *contract.AppError 原样保留其稳定 code / message / status。
func writeContractError(w http.ResponseWriter, err error) {
	ae := contract.AsAppError(err)
	if ae == nil {
		ae = contract.NewAppError(contract.ErrCodeInternal, "内部错误", http.StatusInternalServerError)
	}
	if ae.Status <= 0 {
		ae.Status = contract.StatusForCode(ae.Code)
	}
	// 内部细节只进日志，不序列化到响应
	inner := err
	if ae.Err != nil {
		inner = ae.Err
	}
	if inner != nil {
		logx.Warn("transport", "请求失败", "code", ae.Code, "op", ae.Op, "err", inner.Error())
	}
	writeJSON(w, ae.Status, &Response{Code: ae.Code, Message: ae.Message})
}

// readBody 读取请求体
func readBody(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("读取请求体失败: %w", err)
	}
	return json.Unmarshal(data, v)
}

// decodeStrictBody 严格解码请求体（最大体写请求使用）：
// 套 MaxBytesReader 限制大小,并 DisallowUnknownFields 拒绝未知字段。
// 解析失败时直接写出 400/413 并返回 false；成功返回 true。
func (m *Module) decodeStrictBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, m.maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, "request_too_large", "请求体过大", http.StatusRequestEntityTooLarge)
		} else {
			writeContractError(w, wrapErr(contract.ErrCodeBadRequest, "请求体不合法", err))
		}
		return false
	}
	return true
}

// getPathParam 从 URL 路径中提取参数（如 /api/files/{id}）
func getPathParam(path, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	param := strings.TrimPrefix(path, prefix)
	param = strings.TrimSuffix(param, "/")
	parts := strings.SplitN(param, "/", 2)
	return parts[0]
}

// getQueryParam 获取查询参数
func getQueryParam(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}

// getQueryInt 获取整数查询参数
func getQueryInt(r *http.Request, key string, defaultVal int) int {
	s := getQueryParam(r, key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}
