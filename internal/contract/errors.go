// Package contract 定义所有模块的接口契约与共享类型。
// 本文件：统一错误模型（P1-13）——AppError、稳定错误码、集中 HTTP 映射。
package contract

import (
	"errors"
	"net/http"
)

// ──────────────────────────── 错误码（稳定契约，勿随意改名） ────────────────────────────

const (
	ErrCodeBadRequest    = "bad_request"
	ErrCodeInvalidParam  = "invalid_param"
	ErrCodeNotFound      = "not_found"
	ErrCodeNotConfigured = "not_configured"
	ErrCodeUnauthorized  = "unauthorized"
	ErrCodeForbidden     = "forbidden"
	ErrCodeConflict      = "conflict"
	ErrCodeRateLimited   = "rate_limited"
	ErrCodeTimeout       = "timeout"
	ErrCodeCanceled      = "canceled"
	ErrCodeLLM           = "llm_error"
	ErrCodeExtract       = "extract_error"
	ErrCodeInternal      = "internal"
)

// AppError 统一业务错误：稳定 code + 面向用户的 message + HTTP 映射。
// 通过 errors.As 提取后可直接写响应；未知错误统一归一为 ErrCodeInternal。
type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"` // HTTP 状态码；0 表示按 Code 映射
	Op      string `json:"-"` // 出错操作标识（operationId 段），仅日志用
	Err     error  `json:"-"` // 原始错误（仅内部保留，不序列化）
}

func (e *AppError) Error() string { return e.Message }
func (e *AppError) Unwrap() error { return e.Err }

// WithOp 附加上下文操作标识（不影响序列化）。
func (e *AppError) WithOp(op string) *AppError {
	if e == nil {
		return e
	}
	cp := *e
	cp.Op = op
	return &cp
}

// WithCause 附加原始错误（不序列化）。
func (e *AppError) WithCause(err error) *AppError {
	if e == nil {
		return e
	}
	cp := *e
	cp.Err = err
	return &cp
}

// NewAppError 构造错误；status<=0 时按 Code 自动映射。
func NewAppError(code, message string, status int) *AppError {
	if status <= 0 {
		status = StatusForCode(code)
	}
	return &AppError{Code: code, Message: message, Status: status}
}

// StatusForCode 集中错误码 → HTTP 状态映射（唯一权威来源）。
func StatusForCode(code string) int {
	switch code {
	case ErrCodeBadRequest, ErrCodeInvalidParam:
		return http.StatusBadRequest
	case ErrCodeNotFound:
		return http.StatusNotFound
	case ErrCodeNotConfigured:
		return http.StatusBadRequest
	case ErrCodeUnauthorized:
		return http.StatusUnauthorized
	case ErrCodeForbidden:
		return http.StatusForbidden
	case ErrCodeConflict:
		return http.StatusConflict
	case ErrCodeRateLimited:
		return http.StatusTooManyRequests
	case ErrCodeTimeout:
		return http.StatusGatewayTimeout
	case ErrCodeCanceled:
		return 499 // Client Closed Request（标准库无常数，用字面量）
	case ErrCodeLLM:
		return http.StatusBadGateway
	case ErrCodeExtract:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// AsAppError 将任意 error 归一为 *AppError：
//   - nil → nil
//   - 已是 *AppError → 原样返回
//   - 其他 → ErrCodeInternal（status=500，message 固定，不泄露内部细节），cause 保留
func AsAppError(err error) *AppError {
	if err == nil {
		return nil
	}
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	return NewAppError(ErrCodeInternal, "内部错误", http.StatusInternalServerError).WithCause(err)
}
