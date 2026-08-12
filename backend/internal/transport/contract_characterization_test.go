package transport

// Characterization 测试（审计 Phase 0）：锁定当前 REST/SSE 契约与行为。
// 原则：不改生产代码；能用假实现注入的端点用假实现，能走真实模块的（git/storage/config/timeline）
// 用真实模块 + 临时目录，避免任何外部网络 / LLM / Python 依赖。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"memora/internal/browser"
	"memora/internal/config"
	"memora/internal/contract"
	"memora/internal/events"
	"memora/internal/taskqueue"
)

// ─────────── 假实现：仅满足 transport.APIHandler 各接口，无外部依赖 ───────────

type charStorage struct {
	file *contract.FileInfo
}

func (s *charStorage) FilesUpsert(f *contract.FileInfo) (int64, error) { return f.ID, nil }
func (s *charStorage) FilesFindByRelPath(relPath string) (*contract.FileInfo, error) {
	return s.file, nil
}
func (s *charStorage) FilesGet(id int64) (*contract.FileInfo, error) { return s.file, nil }
func (s *charStorage) FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*contract.FileInfo, int, error) {
	return []*contract.FileInfo{
		{ID: 1, RelPath: "docs/a.md", DocType: "md", IndexStatus: "indexed"},
		{ID: 2, RelPath: "docs/b.md", DocType: "md", IndexStatus: "pending"},
	}, 2, nil
}
func (s *charStorage) FilesMarkStatus(id int64, status, lastError string) error { return nil }
func (s *charStorage) FilesRetryStatus(id int64) error                          { return nil }
func (s *charStorage) ChunksByFile(fileID int64) ([]*contract.Chunk, error)     { return nil, nil }
func (s *charStorage) TagsList() ([]*contract.TagInfo, error)                   { return nil, nil }
func (s *charStorage) FileTagsListByFile(fileID int64) ([]contract.FileTag, error) {
	return []contract.FileTag{{Name: "docs", Origin: "manual"}}, nil
}
func (s *charStorage) FileTagsByFiles(fileIDs []int64) (map[int64][]contract.FileTag, error) {
	return map[int64][]contract.FileTag{}, nil
}
func (s *charStorage) SuggestionsListPending() ([]*contract.TagSuggestion, error) {
	return []*contract.TagSuggestion{}, nil
}
func (s *charStorage) SuggestionsSetStatus(id int64, status string) error { return nil }
func (s *charStorage) QASessionsList() ([]*contract.QASession, error)     { return nil, nil }
func (s *charStorage) QASessionsDelete(id int64) error                    { return nil }
func (s *charStorage) QAMessagesBySession(sessionID int64) ([]*contract.QAMessage, error) {
	return nil, nil
}
func (s *charStorage) FilesRecent(sinceMs int64, limit int) ([]*contract.FileInfo, error) {
	return nil, nil
}
func (s *charStorage) VectorCount() (int, error) { return 0, nil }
func (s *charStorage) Ping() error               { return nil }

type charGit struct {
	status map[string]string
	head   *contract.HeadInfo
}

func (g *charGit) EnsureRepo(path string) error       { return nil }
func (g *charGit) Status() (map[string]string, error) { return g.status, nil }
func (g *charGit) CommitAuto(files []string) (string, bool, error) {
	return "", true, nil
}
func (g *charGit) CommitManual(message string) (string, error) { return "", nil }
func (g *charGit) Log() ([]*contract.CommitInfo, error)        { return nil, nil }
func (g *charGit) DiffStats(hash string) (*contract.DiffStat, error) {
	return nil, nil
}
func (g *charGit) FileHistory(relPath string) ([]*contract.CommitInfo, error) {
	return nil, nil
}
func (g *charGit) ShowFileAt(relPath, hash string) (string, error) { return "", nil }
func (g *charGit) RestoreFile(relPath, hash string) error          { return nil }
func (g *charGit) Head() (*contract.HeadInfo, error)               { return g.head, nil }
func (g *charGit) CommitFiles(hash string) ([]*contract.CommitFile, error) {
	return nil, nil
}
func (g *charGit) ListTreeAt(hash string) ([]*contract.VersionFile, error) {
	return nil, nil
}

// charGitHeadErr 复用 charGit 实现，仅让 Head 报错（模拟仓库未初始化）。
type charGitHeadErr struct {
	charGit
}

func (charGitHeadErr) Head() (*contract.HeadInfo, error) {
	return nil, errors.New("仓库未初始化")
}

type charWatch struct{}

func (charWatch) Pause() error  { return nil }
func (charWatch) Resume() error { return nil }

type charExtract struct{}

func (charExtract) Probe(pythonPath, command string) (bool, string)       { return false, "" }
func (charExtract) ApplyConfig(pythonPath, command, markitdownCmd string) {}

type charIndex struct{}

func (charIndex) FullReindex() error    { return nil }
func (charIndex) SetEmbedDim(dim int64) {}

type charTag struct{}

func (charTag) ListLibrary() ([]*contract.TagInfo, error)               { return []*contract.TagInfo{}, nil }
func (charTag) ManualOverride(fileID int64, add, remove []string) error { return nil }
func (charTag) AcceptSuggestion(id int64) error                         { return nil }
func (charTag) RejectSuggestion(id int64) error                         { return nil }

type charSearch struct{}

func (charSearch) Query(q string, tagFilter []string, page int) ([]*contract.SearchResult, int, error) {
	return []*contract.SearchResult{
		{FileID: 1, RelPath: "docs/a.md", HitText: "片段", Score: 0.9},
	}, 1, nil
}

type charTimeline struct{}

func (charTimeline) GenerateSummary(commitHash string) (string, error) { return "", nil }
func (charTimeline) SuggestCommitMessage() (string, error)             { return "", nil }
func (charTimeline) Restore(relPath, hash string) error                { return nil }

type charTimelineErr struct{}

func (charTimelineErr) GenerateSummary(commitHash string) (string, error) { return "", nil }
func (charTimelineErr) SuggestCommitMessage() (string, error) {
	return "", errors.New("LLM 未配置")
}
func (charTimelineErr) Restore(relPath, hash string) error { return nil }

type charQA struct{}

func (charQA) Ask(req *contract.QARequest) (*contract.QAResponse, error) { return nil, nil }
func (charQA) AskStream(req *contract.QARequest, cancel <-chan struct{}) (<-chan string, <-chan *contract.QAResponse) {
	return nil, nil
}
func (charQA) Sessions() ([]*contract.QASession, error) { return nil, nil }
func (charQA) DeleteSession(id int64) error             { return nil }

type charStats struct {
	enabled bool
}

func (s *charStats) Enabled() bool { return s.enabled }
func (s *charStats) SetEnabled(v bool) error {
	s.enabled = v
	return nil
}
func (s *charStats) Summary(r *contract.StatsRange) (*contract.StatsMetrics, error) {
	return &contract.StatsMetrics{}, nil
}
func (s *charStats) Export(format string, r *contract.StatsRange) (string, error) {
	return "", nil
}
func (s *charStats) Purge() error { return nil }

type charLLM struct{}

func (charLLM) TestChat() error { return nil }
func (charLLM) TestEmbed() error {
	return nil
}
func (charLLM) TestChatWith(baseURL, apiKey, model string, temperature float64) error {
	return nil
}
func (charLLM) TestEmbedWith(baseURL, apiKey, model string) error { return nil }
func (charLLM) TestRerankWith(baseURL, apiKey, model string) error {
	return nil
}
func (charLLM) ListModels(kind, baseURL, apiKey string) ([]string, error) {
	return []string{"model-a"}, nil
}

type charBrowser struct{}

func (charBrowser) ListDir(workspace, subPath string) ([]*browser.DirEntry, error) {
	return []*browser.DirEntry{}, nil
}
func (charBrowser) SearchByName(workspace, query string, limit int) ([]*browser.SearchResult, int, error) {
	return nil, 0, nil
}
func (charBrowser) PickDirectory(initial string) (string, error) { return "", nil }
func (charBrowser) OpenFile(workspace, relPath string) error     { return nil }

type charTaskQueue struct{}

func (charTaskQueue) Pause() error                                { return nil }
func (charTaskQueue) Resume() error                               { return nil }
func (charTaskQueue) Status() (running, pending int, paused bool) { return 0, 0, false }
func (charTaskQueue) Submit(task *taskqueue.Task) error           { return nil }

// ─────────── 测试基建：请求 / 解码辅助 ───────────

func doReq(m *Module, method, target, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	rr := httptest.NewRecorder()
	m.mux.ServeHTTP(rr, req)
	return rr
}

func decodeResp(t *testing.T, rr *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("解析 JSON 失败: %v; body=%s", err, rr.Body.String())
	}
}

func assertStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("HTTP 状态码 = %d, want %d; body=%s", rr.Code, want, rr.Body.String())
	}
}

// newCharFakeHandler 构建全假实现的 handler（无真实 IO，锁定协议形状/校验行为）。
func newCharFakeHandler(t *testing.T, mutate func(h *APIHandler)) *Module {
	t.Helper()
	evt := events.New()
	h := &APIHandler{
		Config:   newInMemoryConfig(t, ""),
		Storage:  &charStorage{},
		Git:      &charGit{},
		Watch:    charWatch{},
		Extract:  charExtract{},
		Index:    charIndex{},
		Tag:      charTag{},
		Search:   charSearch{},
		Timeline: charTimeline{},
		QA:       charQA{},
		Stats:    &charStats{},
		LLM:      charLLM{},
		Browser:  charBrowser{},
	}
	if mutate != nil {
		mutate(h)
	}
	m := New(h, evt)
	m.registerRoutes()
	return m
}

// newInMemoryConfig 在临时目录创建真实 config 模块；workspace 非空时同步写入 workspace.path。
func newInMemoryConfig(t *testing.T, workspace string) *config.Module {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".memora", "config.json")
	evt := events.New()
	cfg, err := config.New(path, evt)
	if err != nil {
		t.Fatalf("创建 config 失败: %v", err)
	}
	if workspace != "" {
		if err := cfg.Set("workspace.path", workspace); err != nil {
			t.Fatalf("设置 workspace.path 失败: %v", err)
		}
	}
	return cfg
}

// ─────────── 1. REST 契约 ───────────

// GET /api/workspace/info：未初始化时返回 initialized=false，dirtyCounts 为 null（当前行为）。
func TestWorkspaceInfoNotInitialized(t *testing.T) {
	m := newCharFakeHandler(t, nil)

	rr := doReq(m, http.MethodGet, "/api/workspace/info", "")
	assertStatus(t, rr, http.StatusOK)

	var resp struct {
		Code string `json:"code"`
		Data struct {
			Initialized   bool                   `json:"initialized"`
			WorkspacePath string                 `json:"workspacePath"`
			DirtyCounts   map[string]interface{} `json:"dirtyCounts"`
			Head          *contract.HeadInfo     `json:"head"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "ok" {
		t.Fatalf("code = %q, want ok", resp.Code)
	}
	if resp.Data.Initialized {
		t.Fatalf("未初始化时应 initialized=false")
	}
	if resp.Data.WorkspacePath != "" {
		t.Fatalf("workspacePath = %q, want 空串", resp.Data.WorkspacePath)
	}
	// 当前实现：未初始化时 dirtyCounts 未被赋值，序列化为 null
	if resp.Data.DirtyCounts != nil {
		t.Fatalf("未初始化时 dirtyCounts 应为 null，got %v", resp.Data.DirtyCounts)
	}
	if resp.Data.Head != nil {
		t.Fatalf("未初始化时 head 应为 null，got %+v", resp.Data.Head)
	}
}

// GET /api/workspace/info：已初始化 + 有脏文件时，dirtyCounts 按 git status 计数。
func TestWorkspaceInfoInitializedDirtyCounts(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Config = newInMemoryConfig(t, "C:/fake/ws")
		h.Git = &charGit{
			status: map[string]string{"a.txt": "M", "b.txt": "?", "c.txt": "D"},
			head:   &contract.HeadInfo{Hash: "abc123", HasCommits: true, CountFiles: 3},
		}
	})

	rr := doReq(m, http.MethodGet, "/api/workspace/info", "")
	assertStatus(t, rr, http.StatusOK)

	var resp struct {
		Code string `json:"code"`
		Data struct {
			Initialized   bool              `json:"initialized"`
			WorkspacePath string            `json:"workspacePath"`
			DirtyCounts   map[string]int    `json:"dirtyCounts"`
			Head          contract.HeadInfo `json:"head"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if !resp.Data.Initialized {
		t.Fatalf("已初始化应 initialized=true")
	}
	if resp.Data.DirtyCounts["modified"] != 1 || resp.Data.DirtyCounts["untracked"] != 1 || resp.Data.DirtyCounts["deleted"] != 1 {
		t.Fatalf("dirtyCounts = %v, want {modified:1 untracked:1 deleted:1}", resp.Data.DirtyCounts)
	}
	if resp.Data.Head.Hash != "abc123" || !resp.Data.Head.HasCommits {
		t.Fatalf("head = %+v, want hash=abc123 hasCommits=true", resp.Data.Head)
	}
}

// GET /api/files：分页形状 {page,pageSize,total,items[]}，items 含 tags 数组。
func TestFilesListContract(t *testing.T) {
	m := newCharFakeHandler(t, nil)

	rr := doReq(m, http.MethodGet, "/api/files?page=1&pageSize=20", "")
	assertStatus(t, rr, http.StatusOK)

	var resp struct {
		Code string `json:"code"`
		Data struct {
			Page     int `json:"page"`
			PageSize int `json:"pageSize"`
			Total    int `json:"total"`
			Items    []struct {
				ID      int64              `json:"id"`
				RelPath string             `json:"relPath"`
				Tags    []contract.FileTag `json:"tags"`
			} `json:"items"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Data.Page != 1 || resp.Data.PageSize != 20 || resp.Data.Total != 2 || len(resp.Data.Items) != 2 {
		t.Fatalf("分页元数据不符: page=%d pageSize=%d total=%d items=%d", resp.Data.Page, resp.Data.PageSize, resp.Data.Total, len(resp.Data.Items))
	}
	if resp.Data.Items[0].RelPath != "docs/a.md" {
		t.Fatalf("items[0].relPath = %q, want docs/a.md", resp.Data.Items[0].RelPath)
	}
}

// GET /api/files/{id}：返回扁平 FileItem（含 tags）。
func TestFileDetailContract(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Storage = &charStorage{file: &contract.FileInfo{ID: 1, RelPath: "docs/note.md", DocType: "md", IndexStatus: "indexed"}}
	})

	rr := doReq(m, http.MethodGet, "/api/files/1", "")
	assertStatus(t, rr, http.StatusOK)

	var resp struct {
		Code string `json:"code"`
		Data struct {
			ID          int64              `json:"id"`
			RelPath     string             `json:"relPath"`
			IndexStatus string             `json:"indexStatus"`
			Tags        []contract.FileTag `json:"tags"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Data.ID != 1 || resp.Data.RelPath != "docs/note.md" || resp.Data.IndexStatus != "indexed" {
		t.Fatalf("文件详情不符: %+v", resp.Data)
	}
	if len(resp.Data.Tags) != 1 || resp.Data.Tags[0].Name != "docs" {
		t.Fatalf("tags 不符: %+v", resp.Data.Tags)
	}
}

// GET /api/files/{id}：不存在的文件 → 404 not_found。
func TestFileDetailNotFound(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Storage = &charStorage{file: nil}
	})
	rr := doReq(m, http.MethodGet, "/api/files/999", "")
	assertStatus(t, rr, http.StatusNotFound)
	var resp struct {
		Code string `json:"code"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "not_found" {
		t.Fatalf("code = %q, want not_found", resp.Code)
	}
}

// GET /api/files/recent：工作区未初始化时返回 400 not_configured。
func TestFilesRecentNotConfigured(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodGet, "/api/files/recent", "")
	assertStatus(t, rr, http.StatusBadRequest)
	var resp struct {
		Code string `json:"code"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "not_configured" {
		t.Fatalf("code = %q, want not_configured", resp.Code)
	}
}

// GET /api/search：返回 {page,items,total}。
func TestSearchContract(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodGet, "/api/search?q=预算&page=0", "")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Page  int `json:"page"`
			Total int `json:"total"`
			Items []struct {
				RelPath string  `json:"relPath"`
				Score   float64 `json:"score"`
			} `json:"items"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Data.Page != 0 || resp.Data.Total != 1 || len(resp.Data.Items) != 1 {
		t.Fatalf("搜索响应不符: %+v", resp.Data)
	}
	if resp.Data.Items[0].RelPath != "docs/a.md" {
		t.Fatalf("搜索项不符: %+v", resp.Data.Items[0])
	}
}

// GET /api/tags：返回 {tags: []}（空切片而非 null）。
func TestTagsListContract(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodGet, "/api/tags", "")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Tags []contract.TagInfo `json:"tags"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Data.Tags == nil {
		t.Fatalf("tags 应为空切片而非 null")
	}
}

// GET /api/tag-suggestions：返回 {suggestions: []}（空切片而非 null）。
func TestTagSuggestionsListContract(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodGet, "/api/tag-suggestions", "")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Suggestions []*contract.TagSuggestion `json:"suggestions"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Data.Suggestions == nil {
		t.Fatalf("suggestions 应为空切片而非 null")
	}
}

// POST /api/tag-suggestions/{id}/accept：走 Tag.AcceptSuggestion。
func TestTagSuggestionAccept(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodPost, "/api/tag-suggestions/3/accept", "")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string          `json:"code"`
		Data map[string]bool `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if !resp.Data["ok"] {
		t.Fatalf("accept 应返回 ok=true")
	}
}

// GET /api/commits/status：code 为 "?"/"M" 的条目均列出（当前行为：'?' 不跳过）。
func TestCommitStatusContract(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Git = &charGit{status: map[string]string{"a.txt": "M", "b.txt": "?"}}
	})
	rr := doReq(m, http.MethodGet, "/api/commits/status", "")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Files []struct {
				RelPath string `json:"relPath"`
				Code    string `json:"code"`
			} `json:"files"`
			Count int `json:"count"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Data.Count != 2 || len(resp.Data.Files) != 2 {
		t.Fatalf("count = %d, files = %d, want 2/2（当前行为 '?' 也计入）", resp.Data.Count, len(resp.Data.Files))
	}
}

// POST /api/commits/suggest：LLM 不可用时返回 422 ai_unavailable。
func TestCommitSuggestUnavailable(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Timeline = charTimelineErr{}
	})
	rr := doReq(m, http.MethodPost, "/api/commits/suggest", "{}")
	assertStatus(t, rr, http.StatusUnprocessableEntity)
	var resp struct {
		Code string `json:"code"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "ai_unavailable" {
		t.Fatalf("code = %q, want ai_unavailable", resp.Code)
	}
}

// GET /api/queue/status：TaskQueue 为 nil 时返回 503 not_ready。
func TestQueueStatusNotReady(t *testing.T) {
	m := newCharFakeHandler(t, nil) // TaskQueue 保持 nil
	rr := doReq(m, http.MethodGet, "/api/queue/status", "")
	assertStatus(t, rr, http.StatusServiceUnavailable)
}

// /api/timeline 已下线：路由不再注册,请求应命中 404。
func TestTimelineRouteRemoved(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodGet, "/api/timeline", "")
	assertStatus(t, rr, http.StatusNotFound)
}

// POST-only 端点对 GET 应返回 400 bad_request。
func TestPOSTEndpointsRejectGET(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	for _, path := range []string{
		"/api/workspace/init",
		"/api/commits/auto",
		"/api/commits/manual",
		"/api/index/reindex",
	} {
		rr := doReq(m, http.MethodGet, path, "")
		assertStatus(t, rr, http.StatusBadRequest)
		var resp struct {
			Code string `json:"code"`
		}
		decodeResp(t, rr, &resp)
		if resp.Code != "bad_request" {
			t.Fatalf("%s GET code = %q, want bad_request", path, resp.Code)
		}
	}
}

// GET /api/commits/list?withFiles=true：withFiles 时逐提交携带 files 明细。
func TestCommitListWithFilesContract(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Git = &charGitWithFiles{}
	})
	rr := doReq(m, http.MethodGet, "/api/commits/list?withFiles=true", "")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Commits []struct {
				Hash  string                `json:"hash"`
				Time  int64                 `json:"time"`
				Files []contract.CommitFile `json:"files"`
			} `json:"commits"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if len(resp.Data.Commits) != 1 {
		t.Fatalf("commits = %d, want 1", len(resp.Data.Commits))
	}
	if len(resp.Data.Commits[0].Files) != 1 || resp.Data.Commits[0].Files[0].Path != "a.md" {
		t.Fatalf("files 明细不符: %+v", resp.Data.Commits[0].Files)
	}
}

type charGitWithFiles struct{}

func (g *charGitWithFiles) EnsureRepo(path string) error { return nil }
func (g *charGitWithFiles) Status() (map[string]string, error) {
	return map[string]string{}, nil
}
func (g *charGitWithFiles) CommitAuto(files []string) (string, bool, error) {
	return "", true, nil
}
func (g *charGitWithFiles) CommitManual(message string) (string, error) { return "", nil }
func (g *charGitWithFiles) Log() ([]*contract.CommitInfo, error) {
	return []*contract.CommitInfo{{Hash: "abc123", Time: 1700000000000, Message: "首次", Author: "T"}}, nil
}
func (g *charGitWithFiles) DiffStats(hash string) (*contract.DiffStat, error) {
	return nil, nil
}
func (g *charGitWithFiles) FileHistory(relPath string) ([]*contract.CommitInfo, error) {
	return nil, nil
}
func (g *charGitWithFiles) ShowFileAt(relPath, hash string) (string, error) { return "", nil }
func (g *charGitWithFiles) RestoreFile(relPath, hash string) error          { return nil }
func (g *charGitWithFiles) Head() (*contract.HeadInfo, error)               { return nil, nil }
func (g *charGitWithFiles) CommitFiles(hash string) ([]*contract.CommitFile, error) {
	return []*contract.CommitFile{{Path: "a.md", Status: "modified"}}, nil
}
func (g *charGitWithFiles) ListTreeAt(hash string) ([]*contract.VersionFile, error) {
	return nil, nil
}

// GET /api/files/download-history：非法 hash 返回 400。
func TestFileDownloadHistoryInvalidHash(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodGet, "/api/files/download-history?relPath=a.md&hash=not-a-hash", "")
	assertStatus(t, rr, http.StatusBadRequest)
}

// GET /api/files/resolve：relPath 归一化后查 storage。
func TestFileResolveContract(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Storage = &charStorage{file: &contract.FileInfo{ID: 7, RelPath: "docs/x.md"}}
	})
	rr := doReq(m, http.MethodGet, "/api/files/resolve?relPath=docs/x.md", "")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string `json:"code"`
		Data struct {
			FileID int64 `json:"fileId"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Data.FileID != 7 {
		t.Fatalf("fileId = %d, want 7", resp.Data.FileID)
	}
}

// GET /api/settings：返回 config 快照。
func TestSettingsGetContract(t *testing.T) {
	m := newCharFakeHandler(t, nil)
	rr := doReq(m, http.MethodGet, "/api/settings", "")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string                 `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if _, ok := resp.Data["workspacePath"]; !ok {
		t.Fatalf("settings 快照缺少 workspacePath 键")
	}
}

// GET /api/stats：默认统计关闭时返回 200 + code=stats_disabled。
func TestStatsDisabledContract(t *testing.T) {
	m := newCharFakeHandler(t, nil) // charStats.enabled=false
	rr := doReq(m, http.MethodGet, "/api/stats", "")
	assertStatus(t, rr, http.StatusOK)
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Enabled bool `json:"enabled"`
		} `json:"data"`
	}
	decodeResp(t, rr, &resp)
	if resp.Code != "stats_disabled" || resp.Data.Enabled {
		t.Fatalf("统计关闭响应不符: code=%s enabled=%v", resp.Code, resp.Data.Enabled)
	}
}

// GET /api/commits/head：git 未初始化（Head 报错）时返回 500 internal（当前行为）。
func TestCommitHeadUninitialized(t *testing.T) {
	m := newCharFakeHandler(t, func(h *APIHandler) {
		h.Git = &charGitHeadErr{}
	})
	rr := doReq(m, http.MethodGet, "/api/commits/head", "")
	assertStatus(t, rr, http.StatusInternalServerError)
}

// ─────────── 2. SSE 契约 ───────────

// GET /api/events：订阅后 Notify 一个事件，SSE 流应收到对应 topic 的 JSON 帧。
func TestSSEEventBroadcast(t *testing.T) {
	evt := events.New()
	m := New(&APIHandler{}, evt)
	m.registerRoutes()

	srv := httptest.NewServer(m.mux)
	defer srv.Close()

	// handleSSE 在读循环前不写初始字节，Do() 会阻塞到首个事件到达，
	// 故必须在独立 goroutine 中发起请求并饥饿读取，主流程只负责 Notify。
	type frameResult struct {
		resp *http.Response
		line string
		err  error
	}
	frameCh := make(chan frameResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
		if err != nil {
			frameCh <- frameResult{err: err}
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			frameCh <- frameResult{err: err}
			return
		}
		defer resp.Body.Close()
		reader := bufio.NewReader(resp.Body)
		line, err := reader.ReadString('\n')
		frameCh <- frameResult{resp: resp, line: line, err: err}
	}()

	// 连接注册到 m.sseConns 有竞态：循环 Notify，直到收到帧或超时。
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		evt.Notify("files_changed", map[string]interface{}{"added": []string{"a.md"}})
		select {
		case fr := <-frameCh:
			if fr.err != nil {
				t.Fatalf("SSE 请求/首帧失败: %v", fr.err)
			}
			if ct := fr.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
				t.Fatalf("Content-Type = %q, want text/event-stream", ct)
			}
			line := strings.TrimSpace(fr.line)
			if !strings.HasPrefix(line, "data: ") {
				t.Fatalf("帧格式不符: %q", line)
			}
			var payload SSEEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
				t.Fatalf("解析 SSE 载荷失败: %v; line=%q", err, line)
			}
			if payload.Topic != "files_changed" {
				t.Fatalf("topic = %q, want files_changed", payload.Topic)
			}
			if payload.Data == nil {
				t.Fatalf("SSE data 不应为 null")
			}
			return
		case <-time.After(150 * time.Millisecond):
			// 连接尚未就绪，重试 Notify
		}
	}
	t.Fatalf("8s 内未收到 SSE 事件帧")
}
