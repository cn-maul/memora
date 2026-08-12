package transport

// 工作区初始化/重建 characterization 集成测试（审计 Phase 0）：
// 用真实 config/git/storage/timeline 模块 + 临时目录，经 /api/workspace/init 触发
// RebuildWorkspace 回调，验证 .memora/ 生成、config 迁移、Git 仓库初始化的副作用。
// 不依赖外部 LLM / Python / 网络。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"memora/internal/config"
	"memora/internal/events"
	"memora/internal/git"
	"memora/internal/storage"
	"memora/internal/timeline"
)

// mustJSON 将 v JSON 编码为字节（路径含 Windows 反斜杠，不能手拼 JSON 字符串）。
func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("JSON 编码失败: %v", err)
	}
	return b
}

// newWSHandler 构建"真实模块"handler：config 落在 cfgPath 目录，storage 数据目录 = config 所在目录。
func newWSHandler(t *testing.T, cfgPath string) (*Module, *config.Module, *git.Module, *storage.Module) {
	t.Helper()
	evt := events.New()
	cfg, err := config.New(cfgPath, evt)
	if err != nil {
		t.Fatalf("创建 config 失败: %v", err)
	}
	gm := git.New(cfg)
	st, err := storage.New(filepath.Dir(cfgPath), 1024)
	if err != nil {
		t.Fatalf("创建 storage 失败: %v", err)
	}
	tm := timeline.New(gm, st, nil, evt, cfg.Workspace())

	h := &APIHandler{
		Config:   cfg,
		Storage:  st,
		Git:      gm,
		Timeline: tm,
		Watch:    charWatch{},
		Extract:  charExtract{},
		Index:    charIndex{},
		Tag:      charTag{},
		Search:   charSearch{},
		QA:       charQA{},
		Stats:    &charStats{},
		LLM:      charLLM{},
		Browser:  charBrowser{},
	}
	m := New(h, evt)
	m.registerRoutes()
	return m, cfg, gm, st
}

// POST /api/workspace/init：初始化后应触发 RebuildWorkspace 回调、迁移 config 到工作区 .memora/、
// 初始化 Git 仓库，且 /api/workspace/info 反映新工作区路径。
func TestWorkspaceInitHappyPath(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(base, ".memora", "config.json")
	m, _, gm, st := newWSHandler(t, cfgPath)
	defer st.Close()

	rebuildCalled := ""
	// 模拟装配层回调（真实装配中调用 App.RebuildWorkspace，此处验证回调可调用性与 Git 初始化副作用）
	m.handler.RebuildWorkspace = func(wsPath string) error {
		rebuildCalled = wsPath
		return gm.EnsureRepo(wsPath)
	}

	body := string(mustJSON(t, map[string]string{"workspacePath": ws}))
	rr := doReq(m, http.MethodPost, "/api/workspace/init", body)
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string `json:"code"`
		Data struct {
			OK bool `json:"ok"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "ok" || !resp.Data.OK {
		t.Fatalf("init 响应不符: code=%s ok=%v body=%s", resp.Code, resp.Data.OK, rr.Body.String())
	}

	// 1) RebuildWorkspace 回调应被调用，且收到工作区路径
	if rebuildCalled != ws {
		t.Fatalf("RebuildWorkspace 收到 %q, want %q", rebuildCalled, ws)
	}

	// 2) Git 仓库应已初始化（.git 存在，且生成初始版本提交）
	gitDir := filepath.Join(ws, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		t.Fatalf("init 后应初始化 Git 仓库: %v", err)
	}
	head, err := gm.Head()
	if err != nil {
		t.Fatalf("读取 HEAD 失败: %v", err)
	}
	if !head.HasCommits {
		t.Fatalf("非空工作区 init 后应有初始提交")
	}

	// 3) config 应迁移到工作区 .memora/config.json 且含 workspace_path
	wsCfgPath := filepath.Join(ws, ".memora", "config.json")
	raw, err := os.ReadFile(wsCfgPath)
	if err != nil {
		t.Fatalf("工作区 config.json 未生成: %v", err)
	}
	var disk struct {
		WorkspacePath string `json:"workspace_path"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("解析工作区 config.json 失败: %v", err)
	}
	if disk.WorkspacePath != ws {
		t.Fatalf("config.workspace_path = %q, want %q", disk.WorkspacePath, ws)
	}

	// 4) GET /api/workspace/info 反映新工作区
	rr = doReq(m, http.MethodGet, "/api/workspace/info", "")
	assertStatus(t, rr, http.StatusOK)
	var info struct {
		Code string `json:"code"`
		Data struct {
			Initialized   bool   `json:"initialized"`
			WorkspacePath string `json:"workspacePath"`
		} `json:"data"`
	}
	decodeResp(t, rr, &info)
	if !info.Data.Initialized || info.Data.WorkspacePath != ws {
		t.Fatalf("workspace/info 未反映新工作区: initialized=%v path=%q", info.Data.Initialized, info.Data.WorkspacePath)
	}
}

// POST /api/workspace/init：工作区路径不存在 → 400 bad_request，且不应触发 RebuildWorkspace。
func TestWorkspaceInitInvalidPath(t *testing.T) {
	base := t.TempDir()
	cfgPath := filepath.Join(base, ".memora", "config.json")
	m, _, _, st := newWSHandler(t, cfgPath)
	defer st.Close()

	called := false
	m.handler.RebuildWorkspace = func(string) error {
		called = true
		return nil
	}

	body := string(mustJSON(t, map[string]string{"workspacePath": filepath.Join(base, "does-not-exist")}))
	rr := doReq(m, http.MethodPost, "/api/workspace/init", body)
	assertStatus(t, rr, http.StatusBadRequest)
	var resp struct {
		Code string `json:"code"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "bad_request" {
		t.Fatalf("code = %q, want bad_request", resp.Code)
	}
	if called {
		t.Fatalf("路径无效时不应调用 RebuildWorkspace")
	}
}

// POST /api/workspace/init：workspacePath 为空 → 400 bad_request。
func TestWorkspaceInitEmptyPath(t *testing.T) {
	base := t.TempDir()
	cfgPath := filepath.Join(base, ".memora", "config.json")
	m, _, _, st := newWSHandler(t, cfgPath)
	defer st.Close()

	rr := doReq(m, http.MethodPost, "/api/workspace/init", `{"workspacePath":""}`)
	assertStatus(t, rr, http.StatusBadRequest)
	var resp struct {
		Code string `json:"code"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "bad_request" {
		t.Fatalf("code = %q, want bad_request", resp.Code)
	}
}

// POST /api/workspace/init：请求体非法 JSON → 400 bad_request。
func TestWorkspaceInitMalformedBody(t *testing.T) {
	base := t.TempDir()
	cfgPath := filepath.Join(base, ".memora", "config.json")
	m, _, _, st := newWSHandler(t, cfgPath)
	defer st.Close()

	rr := doReq(m, http.MethodPost, "/api/workspace/init", `{not-json`)
	assertStatus(t, rr, http.StatusBadRequest)
}
