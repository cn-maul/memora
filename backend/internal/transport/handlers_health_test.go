package transport

// Phase 5 可观测性契约测试：/health、/ready、/diagnostics。
// 用全假实现（newCharFakeHandler）锁定响应结构，不依赖外部 IO / LLM / Python。

import (
	"errors"
	"net/http"
	"testing"

	"memora/internal/events"
)

// charStoragePingErr 复用 charStorage，仅让 Ping 报错（模拟 DB 不可用）。
type charStoragePingErr struct {
	charStorage
}

func (charStoragePingErr) Ping() error { return errors.New("db down") }

// GET /health：永远 200 + status=ok（liveness 不依赖任何模块）。
func TestHealthLiveness(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodGet, "/health", "")
	assertStatus(t, rr, http.StatusOK)
	var body map[string]interface{}
	decodeResp(t, rr, &body)
	if body["status"] != "ok" {
		t.Fatalf("status = %v, want ok; body=%s", body["status"], rr.Body.String())
	}
}

// GET /health：即使 handler 完全为空也返回 200（证明不依赖任何模块）。
func TestHealthNoModules(t *testing.T) {
	m := New(&APIHandler{}, events.New())
	m.registerRoutes()
	rr := doReq(m, http.MethodGet, "/health", "")
	assertStatus(t, rr, http.StatusOK)
	var body map[string]interface{}
	decodeResp(t, rr, &body)
	if body["status"] != "ok" {
		t.Fatalf("status = %v, want ok; body=%s", body["status"], rr.Body.String())
	}
}

// GET /ready：DB 不可用 + 工作区未初始化 → 503 not_ready，含 reasons。
func TestReadyNotReady(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Storage = &charStoragePingErr{}
		h.GenerationFunc = func() string { return "" }
	})
	rr := doReq(m, http.MethodGet, "/ready", "")
	assertStatus(t, rr, http.StatusServiceUnavailable)

	var body struct {
		Status     string   `json:"status"`
		Generation string   `json:"generation"`
		Storage    bool     `json:"storage"`
		Reasons    []string `json:"reasons"`
	}
	decodeResp(t, rr, &body)
	if body.Status != "not_ready" {
		t.Fatalf("status = %q, want not_ready; body=%s", body.Status, rr.Body.String())
	}
	if body.Storage {
		t.Fatalf("storage 应为 false")
	}
	if len(body.Reasons) == 0 {
		t.Fatalf("未就绪应有 reasons")
	}
}

// GET /ready：DB ok + generation 非空 → 200 ready，且 generation 回显。
func TestReadyReady(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.GenerationFunc = func() string { return "w1" }
	})
	rr := doReq(m, http.MethodGet, "/ready", "")
	assertStatus(t, rr, http.StatusOK)

	var body struct {
		Status            string `json:"status"`
		Generation        string `json:"generation"`
		GenerationOk      bool   `json:"generationOk"`
		GenerationChecked bool   `json:"generationChecked"`
		Storage           bool   `json:"storage"`
	}
	decodeResp(t, rr, &body)
	if body.Status != "ready" {
		t.Fatalf("status = %q, want ready; body=%s", body.Status, rr.Body.String())
	}
	if body.Generation != "w1" || !body.GenerationOk || !body.GenerationChecked {
		t.Fatalf("generation 字段不符: %+v", body)
	}
	if !body.Storage {
		t.Fatalf("storage 应为 true")
	}
}

// GET /ready：GenerationFunc 未注入（装配层尚未接线）→ 跳过 generation 检查并标注
// （generationChecked=false），仅以 storage 判定就绪。
func TestReadyGenerationFuncNil(t *testing.T) {
	m := newCharFakeHandler(t, nil) // GenerationFunc 保持 nil
	rr := doReq(m, http.MethodGet, "/ready", "")
	assertStatus(t, rr, http.StatusOK)

	var body struct {
		Status            string `json:"status"`
		Generation        string `json:"generation"`
		GenerationChecked bool   `json:"generationChecked"`
	}
	decodeResp(t, rr, &body)
	if body.Status != "ready" {
		t.Fatalf("status = %q, want ready（generation 检查应被跳过）; body=%s", body.Status, rr.Body.String())
	}
	if body.GenerationChecked {
		t.Fatalf("generationChecked 应为 false（未注入时应标注跳过）")
	}
}

// GET /diagnostics：200 且字段齐全（version/generation/queue/storage/cache/uptimeSec/recentErrors）。
func TestDiagnosticsContract(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Version = "1.2.3"
		h.GenerationFunc = func() string { return "w1" }
		h.TaskQueue = charTaskQueue{}
	})
	rr := doReq(m, http.MethodGet, "/diagnostics", "")
	assertStatus(t, rr, http.StatusOK)

	var body map[string]interface{}
	decodeResp(t, rr, &body)
	if body["version"] != "1.2.3" {
		t.Fatalf("version = %v, want 1.2.3; body=%s", body["version"], rr.Body.String())
	}
	if body["generation"] != "w1" {
		t.Fatalf("generation = %v, want w1", body["generation"])
	}
	queue, ok := body["queue"].(map[string]interface{})
	if !ok {
		t.Fatalf("queue 缺失或类型不符: %v", body["queue"])
	}
	if queue["running"] == nil || queue["pending"] == nil {
		t.Fatalf("queue 字段不齐: %v", queue)
	}
	storageM, ok := body["storage"].(map[string]interface{})
	if !ok || storageM["ok"] != true {
		t.Fatalf("storage.ok 应为 true: %v", body["storage"])
	}
	cache, ok := body["cache"].(map[string]interface{})
	if !ok || cache["files"] == nil || cache["bytes"] == nil {
		t.Fatalf("cache 字段不齐: %v", body["cache"])
	}
	if body["uptimeSec"] == nil {
		t.Fatalf("uptimeSec 缺失")
	}
	if body["recentErrors"] == nil {
		t.Fatalf("recentErrors 缺失")
	}
}

// GET /diagnostics：未注入 version/generation 时 version 回退 "dev"。
func TestDiagnosticsDefaultVersion(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodGet, "/diagnostics", "")
	assertStatus(t, rr, http.StatusOK)

	var body struct {
		Version    string `json:"version"`
		Generation string `json:"generation"`
	}
	decodeResp(t, rr, &body)
	if body.Version != "dev" {
		t.Fatalf("version = %q, want dev", body.Version)
	}
	if body.Generation != "" {
		t.Fatalf("generation = %q, want 空串（未注入）", body.Generation)
	}
}
