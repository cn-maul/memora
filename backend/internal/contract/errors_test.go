package contract

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

type customErr struct{ msg string }

func (customErr) Error() string { return "boom" }

func TestStatusForCode(t *testing.T) {
	cases := map[string]int{
		ErrCodeBadRequest:    http.StatusBadRequest,
		ErrCodeInvalidParam:  http.StatusBadRequest,
		ErrCodeNotFound:      http.StatusNotFound,
		ErrCodeNotConfigured: http.StatusBadRequest,
		ErrCodeUnauthorized:  http.StatusUnauthorized,
		ErrCodeForbidden:     http.StatusForbidden,
		ErrCodeConflict:      http.StatusConflict,
		ErrCodeRateLimited:   http.StatusTooManyRequests,
		ErrCodeTimeout:       http.StatusGatewayTimeout,
		ErrCodeCanceled:      499,
		ErrCodeLLM:           http.StatusBadGateway,
		ErrCodeExtract:       http.StatusUnprocessableEntity,
		ErrCodeInternal:      http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := StatusForCode(code); got != want {
			t.Errorf("StatusForCode(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestNewAppErrorAutoStatus(t *testing.T) {
	e := NewAppError(ErrCodeNotFound, "文件不存在", 0)
	if e.Status != http.StatusNotFound {
		t.Fatalf("auto status = %d, want 404", e.Status)
	}
	e2 := NewAppError(ErrCodeInternal, "x", 500)
	if e2.Status != 500 {
		t.Fatalf("explicit status = %d, want 500", e2.Status)
	}
}

func TestAsAppError(t *testing.T) {
	if AsAppError(nil) != nil {
		t.Fatal("nil → nil")
	}
	known := NewAppError(ErrCodeNotFound, "文件不存在", 0)
	if AsAppError(known) != known {
		t.Fatal("AppError 应原样返回")
	}
	wrapped := errors.Join(known, errors.New("inner")) // 包装链内包含 AppError 也能提取
	if ae := AsAppError(wrapped); ae != known {
		t.Fatalf("wrapped AppError 应提取到原对象, got %+v", ae)
	}
	plain := AsAppError(errors.New("sql: connection refused"))
	if plain == nil || plain.Code != ErrCodeInternal || plain.Status != 500 {
		t.Fatalf("未知错误应归一为 internal, got %+v", plain)
	}
	if !strings.Contains(plain.Message, "内部错误") {
		t.Fatalf("未知错误 message 应固定不泄露细节, got %q", plain.Message)
	}
}

func TestAsAppErrorPromotesOtherError(t *testing.T) {
	// 自定义类型实现 Error 且状态来自 StatusForCode 时,
	// 只有 *AppError 会被识别,其他类型归一为 internal（默认稳定行为）。
	ce := customErr{"boom"}
	ae := AsAppError(ce)
	if ae.Code != ErrCodeInternal {
		t.Fatalf("custom error → internal, got %q", ae.Code)
	}
}

func TestAppErrorChainHelpers(t *testing.T) {
	root := errors.New("root")
	e := NewAppError(ErrCodeLLM, "上游超时", 0).WithOp("qa.ask").WithCause(root)
	if e.Op != "qa.ask" {
		t.Fatalf("WithOp 未生效: %+v", e)
	}
	if !errors.Is(e, root) {
		t.Fatal("WithCause 应可被 errors.Is 穿透")
	}
	if e == nil {
		t.Fatal("chain 不能产生 nil")
	}
}
