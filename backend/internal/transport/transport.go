// Package transport 传输适配模块
// REST 路由、SSE 推送、参数校验、DTO 转换
// 仅监听 127.0.0.1
package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"memora/internal/browser"
	"memora/internal/contract"
	"memora/internal/events"
	"memora/internal/taskqueue"
	"memora/internal/timeline"
)

// EventBus 事件接口
type EventBus interface {
	Notify(topic string, data interface{})
	Subscribe(topic string, handler events.Handler) func()
}

// APIHandler 各模块接口聚合
type APIHandler struct {
	Storage  StorageAPI
	Git      GitAPI
	Watch    WatchAPI
	Extract  ExtractAPI
	Index    IndexAPI
	Tag      TagAPI
	Search   SearchAPI
	Timeline TimelineAPI
	QA       QAAPI
	Stats    StatsAPI
	Config   ConfigAPI
	LLM      LLMAPI
	Browser  BrowserAPI

	// RebuildWorkspace 工作区初始化后由处理器回调，用于原地重建工作区相关模块（修复 B-01）。
	// 由装配层注入。
	RebuildWorkspace func(workspace string) error

	// TaskQueue 任务队列（暂停/恢复/状态，修复 B-03）
	TaskQueue TaskQueueAPI
}

// TaskQueueAPI 任务队列接口
type TaskQueueAPI interface {
	Pause() error
	Resume() error
	Status() (running, pending int, paused bool)
	Submit(task *taskqueue.Task) error
}

// StorageAPI 存储模块接口
type StorageAPI interface {
	FilesUpsert(f *contract.FileInfo) (int64, error)
	FilesFindByRelPath(relPath string) (*contract.FileInfo, error)
	FilesGet(id int64) (*contract.FileInfo, error)
	FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*contract.FileInfo, int, error)
	FilesMarkStatus(id int64, status, lastError string) error
	FilesRetryStatus(id int64) error // 将 failed 重置为 pending
	ChunksByFile(fileID int64) ([]*contract.Chunk, error)
	TagsList() ([]*contract.TagInfo, error)
	FileTagsListByFile(fileID int64) ([]contract.FileTag, error)
	FileTagsByFiles(fileIDs []int64) (map[int64][]contract.FileTag, error)
	SuggestionsListPending() ([]*contract.TagSuggestion, error)
	SuggestionsSetStatus(id int64, status string) error
	QASessionsList() ([]*contract.QASession, error)
	QASessionsDelete(id int64) error
	QAMessagesBySession(sessionID int64) ([]*contract.QAMessage, error)
}

// GitAPI git 模块接口
type GitAPI interface {
	EnsureRepo(path string) error
	Status() (map[string]string, error)
	CommitAuto(files []string) (string, bool, error)
	CommitManual(message string) (string, error)
	Log() ([]*contract.CommitInfo, error)
	DiffStats(hash string) (*contract.DiffStat, error)
	FileHistory(relPath string) ([]*contract.CommitInfo, error)
	ShowFileAt(relPath, hash string) (string, error)
	RestoreFile(relPath, hash string) error
	Head() (*contract.HeadInfo, error)
	CommitFiles(hash string) ([]*contract.CommitFile, error)
}

// WatchAPI watch 模块接口
type WatchAPI interface {
	Pause() error
	Resume() error
}

// ExtractAPI extract 模块接口
type ExtractAPI interface {
	Probe(pythonPath, command string) (bool, string)
	ApplyConfig(pythonPath, command, markitdownCmd string)
}

// IndexAPI index 模块接口
type IndexAPI interface {
	FullReindex() error
}

// TagAPI tag 模块接口
type TagAPI interface {
	ListLibrary() ([]*contract.TagInfo, error)
	ManualOverride(fileID int64, add, remove []string) error
	AcceptSuggestion(id int64) error
	RejectSuggestion(id int64) error
}

// SearchAPI search 模块接口
type SearchAPI interface {
	Query(q string, tagFilter []string, page int) ([]*contract.SearchResult, int, error)
}

// TimelineAPI timeline 模块接口
type TimelineAPI interface {
	Get(q *contract.TimelineQuery) ([]*contract.TimelineNode, error)
	GenerateSummary(commitHash string) (string, error)
	SuggestCommitMessage() (string, error) // AI 根据未提交变动生成提交备注
	Restore(relPath, hash string) error
}

// QAAPI qa 模块接口
type QAAPI interface {
	Ask(req *contract.QARequest) (*contract.QAResponse, error)
	AskStream(req *contract.QARequest, cancel <-chan struct{}) (<-chan string, <-chan *contract.QAResponse)
	Sessions() ([]*contract.QASession, error)
	NewSesion(mode string, fileID int64) (int64, error)
	DeleteSession(id int64) error
}

// StatsAPI stats 模块接口
type StatsAPI interface {
	Enabled() bool
	SetEnabled(v bool) error
	Summary(r *contract.StatsRange) (*contract.StatsMetrics, error)
	Export(format string, r *contract.StatsRange) (string, error)
	Purge() error
}

// ConfigAPI config 模块接口
type ConfigAPI interface {
	Get(key string) (interface{}, error)
	Set(key string, value interface{}) error
	Snapshot() map[string]interface{}
	UpsertSecrets(llmKey, embedKey string) error
	Relocate(workspace string) error
}

// LLMAPI llm 模块接口
type LLMAPI interface {
	TestChat() error
	TestEmbed() error
	TestChatWith(baseURL, apiKey, model string, temperature float64) error
	TestEmbedWith(baseURL, apiKey, model string) error
}

// BrowserAPI 文件浏览模块接口
type BrowserAPI interface {
	ListDir(workspace, subPath string) ([]*browser.DirEntry, error)
	SearchByName(workspace, query string, limit int) ([]*browser.SearchResult, int, error)
	PickDirectory(initial string) (string, error)
	OpenFile(workspace, relPath string) error
}

// Module 传输模块
type Module struct {
	server  *http.Server
	mux     *http.ServeMux
	handler *APIHandler
	events  EventBus
	addr    string
	webDir  string
	webFS   fs.FS

	mu       sync.Mutex
	sseConns map[chan string]struct{}
}

// SetWebDir 设置前端静态资源磁盘目录（如 MEMORA_WEB）。为空则不托管磁盘静态资源。
func (m *Module) SetWebDir(dir string) {
	m.webDir = dir
}

// SetWebFS 设置内嵌的前端静态资源文件系统（go:embed 产物）。webDir 优先级更高。
func (m *Module) SetWebFS(f fs.FS) {
	m.webFS = f
}

// SSEEvent SSE 事件
type SSEEvent struct {
	Topic string      `json:"topic"`
	Data  interface{} `json:"data"`
}

// Response 统一响应
type Response struct {
	Code    string      `json:"code"`
	Data    interface{} `json:"data,omitempty"`
	Message string      `json:"message,omitempty"`
}

// New 创建传输模块
func New(h *APIHandler, events EventBus) *Module {
	m := &Module{
		mux:      http.NewServeMux(),
		handler:  h,
		events:   events,
		sseConns: make(map[chan string]struct{}),
	}

	// 订阅 events，桥接到 SSE
	events.Subscribe("index_progress", func(data interface{}) {
		m.broadcastSSE("index_progress", data)
	})
	events.Subscribe("extract_failed", func(data interface{}) {
		m.broadcastSSE("extract_failed", data)
	})
	events.Subscribe("commit_done", func(data interface{}) {
		m.broadcastSSE("commit_done", data)
	})
	events.Subscribe("tag_done", func(data interface{}) {
		m.broadcastSSE("tag_done", data)
	})
	events.Subscribe("suggestion_new", func(data interface{}) {
		m.broadcastSSE("suggestion_new", data)
	})
	events.Subscribe("files_changed", func(data interface{}) {
		m.broadcastSSE("files_changed", data)
	})
	events.Subscribe("stats_updated", func(data interface{}) {
		m.broadcastSSE("stats_updated", data)
	})
	events.Subscribe("settings_changed", func(data interface{}) {
		m.broadcastSSE("settings_changed", data)
	})
	events.Subscribe("task_queue", func(data interface{}) {
		m.broadcastSSE("task_queue", data)
	})
	events.Subscribe("qa_ready", func(data interface{}) {
		m.broadcastSSE("qa_ready", data)
	})

	return m
}

// Handle 注册路由并启动 HTTP 服务
func (m *Module) Handle(routes map[string]interface{}) error {
	m.registerRoutes()

	// 静态资源（前端 dist）：非 /api 请求交给静态托管（内嵌 FS 或磁盘目录）
	if m.webDir != "" || m.webFS != nil {
		m.mux.HandleFunc("/", m.handleStatic)
	}

	// 查找可用端口
	addr, err := findAvailablePort()
	if err != nil {
		return fmt.Errorf("[transport] 找可用端口失败: %w", err)
	}
	m.addr = addr

	m.server = &http.Server{
		Addr:    addr,
		Handler: withCORS(m.mux),
	}

	go func() {
		fmt.Printf("[transport] HTTP 服务启动于 %s\n", addr)
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[transport] 服务异常: %v\n", err)
		}
	}()

	return nil
}

// findAvailablePort 找可用端口（127.0.0.1，随机端口，冲突自增）
func findAvailablePort() (string, error) {
	for port := 19000; port < 20000; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			listener.Close()
			return addr, nil
		}
	}
	return "", fmt.Errorf("[transport] 无法找到可用端口")
}

// withCORS CORS 中间件
// 服务仅监听 127.0.0.1，但 CORS `*` 会让任意外部网页通过浏览器 fetch 静默读取本地 API（文档全文等）。
// 修复：仅回显 localhost/127.0.0.1 来源的 Origin；无 Origin 的同源请求（Go 内嵌静态资源）直接放行；
// 外部来源不设置 CORS 头，浏览器将拦截响应。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if origin != "" {
			if !isLocalOrigin(origin) {
				// 非本地来源：拒绝所有跨域请求（CSRF 防护）。
				// 浏览器对带 Origin 的跨域 POST 均带此头；外部网页 form/no-cors 也会带，
				// 直接拒绝可阻断对本地 API 的副作用调用（commits/auto、queue/pause 等无 body 端点）。
				w.WriteHeader(http.StatusForbidden)
				return
			}
			// 回显式 ACAO：声明 Vary: Origin，防止缓存把带某 Origin 的响应复用于其他来源
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

// isLocalOrigin 判断 Origin 是否为本机来源（localhost / 127.0.0.1 / ::1，任意端口）
func isLocalOrigin(origin string) bool {
	if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// 拒绝带 userinfo 的构造（如 http://evil.com@localhost），浏览器 Origin 序列化从不含 userinfo
	if u.User != nil {
		return false
	}
	host := u.Hostname()
	// URL.Hostname() 对 IPv6 返回无括号形式（::1），不匹配 "[::1]"
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// registerRoutes 注册所有 API 路由
func (m *Module) registerRoutes() {
	// 工作区
	m.mux.HandleFunc("/api/workspace/info", m.handleWorkspaceInfo)
	m.mux.HandleFunc("/api/workspace/init", m.handleWorkspaceInit)

	// 文件
	m.mux.HandleFunc("/api/files", m.handleFiles)
	m.mux.HandleFunc("/api/files/", m.handleFileByID)

	// 搜索
	m.mux.HandleFunc("/api/search", m.handleSearch)

	// 索引
	m.mux.HandleFunc("/api/index/reindex", m.handleIndexReindex)

	// 文件浏览（资源管理器）
	m.mux.HandleFunc("/api/browse", m.handleBrowse)
	m.mux.HandleFunc("/api/browse/search", m.handleBrowseSearch)
	m.mux.HandleFunc("/api/browse/pickdir", m.handleBrowsePickDir)
	m.mux.HandleFunc("/api/browse/open", m.handleBrowseOpen)

	// 标签
	m.mux.HandleFunc("/api/tags", m.handleTags)
	m.mux.HandleFunc("/api/tag-suggestions", m.handleTagSuggestions)
	m.mux.HandleFunc("/api/tag-suggestions/", m.handleTagSuggestions)

	// 时间线
	m.mux.HandleFunc("/api/timeline", m.handleTimeline)

	// 文件历史版本下载
	m.mux.HandleFunc("/api/files/download-history", m.handleFileDownloadHistory)

	// 按相对路径解析文件 id（供详情弹窗关联版本历史用）
	m.mux.HandleFunc("/api/files/resolve", m.handleFileResolve)

	// 问答
	m.mux.HandleFunc("/api/qa/sessions", m.handleQASessions)
	m.mux.HandleFunc("/api/qa", m.handleQA)
	m.mux.HandleFunc("/api/qa/stream", m.handleQAStream)
	m.mux.HandleFunc("/api/qa/", m.handleQAByID)

	// 统计
	m.mux.HandleFunc("/api/stats", m.handleStats)
	m.mux.HandleFunc("/api/stats/export", m.handleStatsExport)

	// 提交
	m.mux.HandleFunc("/api/commits/auto", m.handleCommitAuto)       // 自动提交（文件变更驱动）
	m.mux.HandleFunc("/api/commits/manual", m.handleCommitManual)   // 手动提交：{message}
	m.mux.HandleFunc("/api/commits/suggest", m.handleCommitSuggest) // AI 建议：{prompt}
	m.mux.HandleFunc("/api/commits/status", m.handleCommitStatus)   // 状态：返回 [{relPath, code}]
	m.mux.HandleFunc("/api/commits/head", m.handleCommitHead)       // HEAD 概要
	m.mux.HandleFunc("/api/commits/list", m.handleCommitList)       // 提交列表（含文件明细）
	m.mux.HandleFunc("/api/commits/", m.handleCommitByHash)         // /{hash}/summary

	// 设置
	m.mux.HandleFunc("/api/settings", m.handleSettings)
	m.mux.HandleFunc("/api/settings/secrets", m.handleSettings)

	// Python 检测
	m.mux.HandleFunc("/api/python/detect", m.handleDetectPython)

	// 测试
	m.mux.HandleFunc("/api/test/", m.handleTest)

	// 任务队列
	m.mux.HandleFunc("/api/queue/status", m.handleQueueStatus)
	m.mux.HandleFunc("/api/queue/pause", m.handleQueuePause)
	m.mux.HandleFunc("/api/queue/resume", m.handleQueueResume)

	// SSE
	m.mux.HandleFunc("/api/events", m.handleSSE)
}

// handleStatic 托管前端静态资源 + SPA 回退到 index.html。
// 优先磁盘目录（webDir，如 MEMORA_WEB），否则用内嵌文件系统（webFS）。
func (m *Module) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// /api 未匹配到的路径视为 API 404，不落入静态目录
	if path == "/api" || strings.HasPrefix(path, "/api/") {
		http.NotFound(w, r)
		return
	}

	// 归一化 + 防目录穿越
	rel := strings.TrimPrefix(path, "/")
	if rel == "" {
		rel = "index.html"
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "/") {
		http.NotFound(w, r)
		return
	}

	// 磁盘目录（MEMORA_WEB 等外部目录）
	if m.webDir != "" {
		full := filepath.Join(m.webDir, cleaned)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			// SPA 回退
			http.ServeFile(w, r, filepath.Join(m.webDir, "index.html"))
			return
		}
		http.ServeFile(w, r, full)
		return
	}

	// 内嵌文件系统（go:embed 的 frontend/dist）
	if m.webFS != nil {
		if serveEmbedded(w, r, m.webFS, cleaned) {
			return
		}
		serveEmbedded(w, r, m.webFS, "index.html") // SPA 回退
		return
	}

	http.NotFound(w, r)
}

// serveEmbedded 从内嵌文件系统提供单个文件，返回是否成功写出。
func serveEmbedded(w http.ResponseWriter, r *http.Request, webFS fs.FS, name string) bool {
	// 先确认存在且是文件（ServeFileFS 对目录会 302 重定向，干扰 SPA 回退判断）
	info, err := fs.Stat(webFS, name)
	if err != nil || info.IsDir() {
		return false
	}
	http.ServeFileFS(w, r, webFS, name)
	return true
}

// ──────────────────── SSE ────────────────────

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

	// 断开清理
	notify := r.Context().Done()
	go func() {
		<-notify
		m.mu.Lock()
		delete(m.sseConns, ch)
		m.mu.Unlock()
	}()

	// 心跳
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-notify:
			return
		}
	}
}

// ──────────────────── 辅助 ────────────────────

// writeJSON 写入 JSON 响应
func writeJSON(w http.ResponseWriter, code int, resp *Response) {
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

// readBody 读取请求体
func readBody(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("读取请求体失败: %w", err)
	}
	return json.Unmarshal(data, v)
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

// ──────────────────── 处理器 ────────────────────

// handleWorkspaceInit POST /api/workspace/init
func (m *Module) handleWorkspaceInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}

	var req struct {
		WorkspacePath string `json:"workspacePath"`
		Markitdown    struct {
			PythonPath string `json:"pythonPath"`
			Command    string `json:"command"`
		} `json:"markitdown"`
		LLM *struct {
			BaseURL     string  `json:"baseUrl"`
			APIKey      string  `json:"apiKey"`
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
		} `json:"llm"`
		Embed struct {
			BaseURL    string `json:"baseUrl"`
			APIKey     string `json:"apiKey"`
			Model      string `json:"model"`
			Dimensions int    `json:"dimensions"`
		} `json:"embed"`
	}
	if err := readBody(r, &req); err != nil {
		writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
		return
	}

	if req.WorkspacePath == "" {
		writeError(w, "bad_request", "workspacePath 不能为空", http.StatusBadRequest)
		return
	}

	// 校验工作区路径存在且为目录（M-01）
	wsInfo, err := os.Stat(req.WorkspacePath)
	if err != nil || !wsInfo.IsDir() {
		writeError(w, "bad_request", "工作区路径不存在或不是目录", http.StatusBadRequest)
		return
	}

	// ── M-01：提交配置前先做探测与模型测试，失败不得留下半初始化状态 ──
	// 1) 提取（MarkItDown）探测
	if req.Markitdown.PythonPath != "" || req.Markitdown.Command != "" {
		probePython := req.Markitdown.PythonPath
		probeCmd := req.Markitdown.Command
		if probeCmd == "" {
			probeCmd = "python -m markitdown \"{file}\""
		}
		ok, msg := m.handler.Extract.Probe(probePython, probeCmd)
		if !ok {
			writeError(w, "bad_request", "MarkItDown 探测失败: "+msg, http.StatusBadRequest)
			return
		}
	}
	// 2) 嵌入端点测试（若本次提供了嵌入配置）
	if req.Embed.BaseURL != "" || req.Embed.Model != "" {
		if err := m.handler.LLM.TestEmbedWith(req.Embed.BaseURL, req.Embed.APIKey, req.Embed.Model); err != nil {
			writeError(w, "bad_request", "嵌入端点测试失败: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	// 3) 聊天端点测试（若本次提供了 LLM 配置）
	if req.LLM != nil && (req.LLM.BaseURL != "" || req.LLM.Model != "") {
		if err := m.handler.LLM.TestChatWith(req.LLM.BaseURL, req.LLM.APIKey, req.LLM.Model, req.LLM.Temperature); err != nil {
			writeError(w, "bad_request", "聊天端点测试失败: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// 1. 保存工作区路径（错误须检查，M-02）
	if err := m.handler.Config.Set("workspace.path", req.WorkspacePath); err != nil {
		writeError(w, "internal", fmt.Sprintf("保存工作区路径失败: %v", err), http.StatusInternalServerError)
		return
	}

	// 1.5 将 config.json 迁移到工作区 .memora/（D13）
	if err := m.handler.Config.Relocate(req.WorkspacePath); err != nil {
		writeError(w, "internal", fmt.Sprintf("迁移配置文件失败: %v", err), http.StatusInternalServerError)
		return
	}

	// 2. 保存 markitdown 配置（错误须检查，M-02）
	if req.Markitdown.PythonPath != "" {
		if err := m.handler.Config.Set("markitdown.pythonPath", req.Markitdown.PythonPath); err != nil {
			writeError(w, "internal", fmt.Sprintf("保存 pythonPath 失败: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if req.Markitdown.Command != "" {
		if err := m.handler.Config.Set("markitdown.command", req.Markitdown.Command); err != nil {
			writeError(w, "internal", fmt.Sprintf("保存 command 失败: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// 3. 保存 LLM 配置（错误须检查，M-02）
	if req.LLM != nil {
		if req.LLM.BaseURL != "" {
			if err := m.handler.Config.Set("llm.baseUrl", req.LLM.BaseURL); err != nil {
				writeError(w, "internal", fmt.Sprintf("保存 LLM 接口失败: %v", err), http.StatusInternalServerError)
				return
			}
		}
		if req.LLM.APIKey != "" {
			if err := m.handler.Config.UpsertSecrets(req.LLM.APIKey, ""); err != nil {
				writeError(w, "internal", fmt.Sprintf("保存 LLM 密钥失败: %v", err), http.StatusInternalServerError)
				return
			}
		}
		if req.LLM.Model != "" {
			if err := m.handler.Config.Set("llm.model", req.LLM.Model); err != nil {
				writeError(w, "internal", fmt.Sprintf("保存 LLM 模型失败: %v", err), http.StatusInternalServerError)
				return
			}
		}
		if req.LLM != nil && (req.LLM.BaseURL != "" || req.LLM.Model != "") {
			// init 请求携带完整 LLM 配置时保存 temperature（含 0，确定性输出）。
			// 仅当 LLM 有实质配置时才保存，避免用户未配置 LLM 时误覆盖现有值（review warn）
			if err := m.handler.Config.Set("llm.temperature", req.LLM.Temperature); err != nil {
				writeError(w, "internal", fmt.Sprintf("保存 LLM 温度失败: %v", err), http.StatusInternalServerError)
				return
			}
		}
	}

	// 4. 保存 Embed 配置（错误须检查，M-02）
	if req.Embed.BaseURL != "" {
		if err := m.handler.Config.Set("embed.baseUrl", req.Embed.BaseURL); err != nil {
			writeError(w, "internal", fmt.Sprintf("保存 Embed 接口失败: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if req.Embed.APIKey != "" {
		if err := m.handler.Config.UpsertSecrets("", req.Embed.APIKey); err != nil {
			writeError(w, "internal", fmt.Sprintf("保存 Embed 密钥失败: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if req.Embed.Model != "" {
		if err := m.handler.Config.Set("embed.model", req.Embed.Model); err != nil {
			writeError(w, "internal", fmt.Sprintf("保存 Embed 模型失败: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if req.Embed.Dimensions != 0 {
		if err := m.handler.Config.Set("embed.dimensions", float64(req.Embed.Dimensions)); err != nil {
			writeError(w, "internal", fmt.Sprintf("保存 Embed 维度失败: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// 5. 原地重建工作区相关模块（停止旧监视、重建存储/索引/时间线/监视、
	//    更新传输层引用、确保 Git 仓库、加载向量索引并触发全量重建）。
	//    若未注入重建回调，则退化为仅初始化 Git 并触发重建（修复 B-01）。
	if m.handler.RebuildWorkspace != nil {
		if err := m.handler.RebuildWorkspace(req.WorkspacePath); err != nil {
			writeError(w, "internal", fmt.Sprintf("应用工作区失败: %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		// 确保 Git 仓库已初始化
		if err := m.handler.Git.EnsureRepo(req.WorkspacePath); err != nil {
			writeError(w, "internal", fmt.Sprintf("Git 初始化失败: %v", err), http.StatusInternalServerError)
			return
		}
		// 异步触发全量重建索引
		go func() {
			if err := m.handler.Index.FullReindex(); err != nil {
				fmt.Printf("[transport] 全量重建索引警告: %v\n", err)
			}
		}()
	}

	writeOK(w, map[string]bool{"ok": true})
}

// handleWorkspaceInfo GET /api/workspace/info
func (m *Module) handleWorkspaceInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	snapshot := m.handler.Config.Snapshot()
	workspacePath, _ := snapshot["workspacePath"].(string)
	initialized := workspacePath != ""

	var dirtyCounts map[string]int
	if initialized {
		status, err := m.handler.Git.Status()
		if err == nil {
			dirtyCounts = map[string]int{"modified": 0, "untracked": 0, "deleted": 0}
			for _, code := range status {
				switch code {
				case "M":
					dirtyCounts["modified"]++
				case "?", "A":
					dirtyCounts["untracked"]++
				case "D":
					dirtyCounts["deleted"]++
				}
			}
		}
	}

	// HEAD 概要
	var headInfo *contract.HeadInfo
	if initialized {
		if hi, err := m.handler.Git.Head(); err == nil {
			headInfo = hi
		}
	}

	llmCfg, _ := snapshot["llm"].(map[string]interface{})
	embedCfg, _ := snapshot["embed"].(map[string]interface{})
	// markitdown 已配置：pythonPath / markitdownCmd 任一显式配置即视为已配置。
	// command 有默认值 `python -m markitdown "{file}"`，不能作为判断依据，否则恒为 true。
	mdConfigured := false
	if md, ok := snapshot["markitdown"].(map[string]interface{}); ok {
		mdConfigured = md["pythonPath"] != "" || md["markitdownCmd"] != ""
	}

	writeOK(w, map[string]interface{}{
		"initialized":          initialized,
		"workspacePath":        workspacePath,
		"dirtyCounts":          dirtyCounts,
		"head":                 headInfo,
		"markitdownConfigured": mdConfigured,
		"llmConfigured":        llmCfg != nil && llmCfg["baseUrl"] != "",
		"embedConfigured":      embedCfg != nil && embedCfg["baseUrl"] != "",
	})
}

// handleFiles GET /api/files
func (m *Module) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	status := getQueryParam(r, "status")
	tag := getQueryParam(r, "tag")
	page := getQueryInt(r, "page", 0)
	pageSize := getQueryInt(r, "pageSize", 50)
	// 钳制 pageSize 上限：批量标签查询的 IN 占位符受 SQLite 变量上限（32766）约束，
	// 且超大 pageSize 无意义（security_review low 观察）
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	sortOrder := getQueryParam(r, "sort") // 格式：field:asc / field:desc

	files, total, err := m.handler.Storage.FilesList(status, tag, page, pageSize, sortOrder)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}

	// 构造 items（含标签），批量查询避免逐文件 N+1（修复审计发现）
	type FileItem struct {
		contract.FileInfo
		Tags []contract.FileTag `json:"tags"`
	}
	ids := make([]int64, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	tagMap, err := m.handler.Storage.FileTagsByFiles(ids)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	items := make([]FileItem, 0, len(files))
	for _, f := range files {
		items = append(items, FileItem{FileInfo: *f, Tags: tagMap[f.ID]})
	}

	writeOK(w, map[string]interface{}{
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
		"items":    items,
	})
}

// handleFileByID GET /api/files/{id} 等
func (m *Module) handleFileByID(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	idStr := getPathParam(path, "/api/files/")

	if idStr == "" || idStr == "search" {
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, "bad_request", "无效文件 ID", http.StatusBadRequest)
		return
	}

	// GET /api/files/{id}
	if r.Method == http.MethodGet && !strings.Contains(path, "/text") && !strings.Contains(path, "/history") && !strings.Contains(path, "/tags") && !strings.Contains(path, "/restore") && !strings.Contains(path, "/open") {
		file, err := m.handler.Storage.FilesGet(id)
		if err != nil || file == nil {
			writeError(w, "not_found", "文件不存在", http.StatusNotFound)
			return
		}
		tags, _ := m.handler.Storage.FileTagsListByFile(file.ID)
		// 返回扁平结构：前端期望 FileItem = FileInfo + tags[]
		result := map[string]interface{}{
			"id":            file.ID,
			"relPath":       file.RelPath,
			"size":          file.Size,
			"mtime":         file.Mtime,
			"contentHash":   file.ContentHash,
			"docType":       file.DocType,
			"indexStatus":   file.IndexStatus,
			"lastError":     file.LastError,
			"firstSeenAt":   file.FirstSeenAt,
			"lastIndexedAt": file.LastIndexedAt,
			"tags":          tags,
		}
		writeOK(w, result)
		return
	}

	// GET /api/files/{id}/text
	if strings.HasSuffix(path, "/text") && r.Method == http.MethodGet {
		chunks, err := m.handler.Storage.ChunksByFile(id)
		if err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		var text string
		for _, c := range chunks {
			text += c.Text + "\n"
		}
		writeOK(w, map[string]string{"text": text})
		return
	}

	// POST /api/files/{id}/open
	if strings.HasSuffix(path, "/open") && r.Method == http.MethodPost {
		file, err := m.handler.Storage.FilesGet(id)
		if err != nil || file == nil {
			writeError(w, "not_found", "文件不存在", http.StatusNotFound)
			return
		}
		// 使用系统默认程序打开文件（修复 H-06）
		if err := m.handler.Browser.OpenFile(m.workspacePath(), file.RelPath); err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w, map[string]bool{"ok": true})
		return
	}

	// GET /api/files/{id}/history
	if strings.HasSuffix(path, "/history") && r.Method == http.MethodGet {
		file, err := m.handler.Storage.FilesGet(id)
		if err != nil || file == nil {
			writeError(w, "not_found", "文件不存在", http.StatusNotFound)
			return
		}
		commits, err := m.handler.Git.FileHistory(file.RelPath)
		if err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w, map[string]interface{}{
			"fileId":  id,
			"relPath": file.RelPath,
			"commits": commits,
		})
		return
	}

	// POST /api/files/{id}/tags
	if strings.HasSuffix(path, "/tags") && r.Method == http.MethodPost {
		var req struct {
			Add    []string `json:"add"`
			Remove []string `json:"remove"`
		}
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}
		if err := m.handler.Tag.ManualOverride(id, req.Add, req.Remove); err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		tags, _ := m.handler.Storage.FileTagsListByFile(id)
		writeOK(w, map[string]interface{}{"tags": tags})
		return
	}

	// POST /api/files/{id}/retry —— 将 failed 文件重置为 pending 并重新入队
	if strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost {
		if err := m.handler.Storage.FilesRetryStatus(id); err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		// 重新入队索引任务
		file, err := m.handler.Storage.FilesGet(id)
		if err == nil && file != nil {
			m.handler.TaskQueue.Submit(&taskqueue.Task{
				Type:    "extract",
				Payload: map[string]interface{}{"relPath": file.RelPath},
			})
		}
		writeOK(w, map[string]interface{}{"ok": true})
		return
	}

	// POST /api/files/{id}/restore
	if strings.HasSuffix(path, "/restore") && r.Method == http.MethodPost {
		file, err := m.handler.Storage.FilesGet(id)
		if err != nil || file == nil {
			writeError(w, "not_found", "文件不存在", http.StatusNotFound)
			return
		}

		var req struct {
			CommitHash string `json:"commitHash"`
		}
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}
		if err := m.handler.Timeline.Restore(file.RelPath, req.CommitHash); err != nil {
			if de, ok := err.(*timeline.WorkspaceDirtyError); ok {
				writeError(w, "workspace_dirty", de.Error(), http.StatusConflict, map[string]interface{}{
					"modified": de.Files,
				})
			} else {
				writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			}
			return
		}
		writeOK(w, map[string]interface{}{"ok": true, "modified": []string{file.RelPath}})
		return
	}

	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleSearch GET /api/search
func (m *Module) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	q := getQueryParam(r, "q")
	tag := getQueryParam(r, "tag")
	page := getQueryInt(r, "page", 0)

	var tagFilter []string
	if tag != "" {
		tagFilter = []string{tag}
	}

	results, total, err := m.handler.Search.Query(q, tagFilter, page)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}

	writeOK(w, map[string]interface{}{
		"page":  page,
		"items": results,
		"total": total,
	})
}

// handleIndexReindex POST /api/index/reindex
// 触发全量重建索引（异步执行，返回立即）。
func (m *Module) handleIndexReindex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	ws := m.workspacePath()
	if ws == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}
	go func() {
		if err := m.handler.Index.FullReindex(); err != nil {
			fmt.Printf("[transport] 全量重建索引警告: %v\n", err)
		}
	}()
	writeOK(w, map[string]bool{"ok": true})
}

// workspacePath 读取当前工作区路径
func (m *Module) workspacePath() string {
	snapshot := m.handler.Config.Snapshot()
	p, _ := snapshot["workspacePath"].(string)
	return p
}

// handleBrowse GET /api/browse?path=subPath
// 资源管理器式浏览工作区目录。
func (m *Module) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	ws := m.workspacePath()
	if ws == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}
	sub := getQueryParam(r, "path")
	entries, err := m.handler.Browser.ListDir(ws, sub)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	// 为可索引的文件补充实际索引状态（indexed/pending/failed 等），不支持的保持空
	for _, e := range entries {
		if e.IsDir || !e.Indexable {
			continue
		}
		// 数据库 rel_path 用系统分隔符（Windows 为反斜杠），浏览器返回正斜杠，需归一化
		dbPath := filepath.FromSlash(e.RelPath)
		rec, ferr := m.handler.Storage.FilesFindByRelPath(dbPath)
		if ferr == nil && rec != nil {
			e.IndexStatus = rec.IndexStatus
		}
	}
	writeOK(w, map[string]interface{}{
		"path":    sub,
		"entries": entries,
	})
}

// handleBrowseSearch GET /api/browse/search?q=xxx
// 按文件名/相对路径模糊搜索（不依赖索引，实时扫描磁盘）。
func (m *Module) handleBrowseSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	ws := m.workspacePath()
	if ws == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}
	q := getQueryParam(r, "q")
	limit := getQueryInt(r, "limit", 100)
	results, total, err := m.handler.Browser.SearchByName(ws, q, limit)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	writeOK(w, map[string]interface{}{
		"query": q,
		"items": results,
		"total": total,
	})
}

// handleBrowseOpen POST /api/browse/open
// 用系统默认应用打开指定相对路径的文件（资源管理器/搜索结果可操作，修复 H-05）。
func (m *Module) handleBrowseOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	ws := m.workspacePath()
	if ws == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}
	var req struct {
		RelPath string `json:"relPath"`
	}
	if err := readBody(r, &req); err != nil {
		writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
		return
	}
	if req.RelPath == "" {
		writeError(w, "bad_request", "缺少 relPath", http.StatusBadRequest)
		return
	}
	if err := m.handler.Browser.OpenFile(ws, req.RelPath); err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// handleBrowsePickDir POST /api/browse/pickdir
// 弹出系统原生目录选择对话框，返回所选路径。
func (m *Module) handleBrowsePickDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	// 可选：body 传 initial 起始目录
	initial := ""
	var body map[string]string
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body != nil {
			initial = body["initial"]
		}
	}
	path, err := m.handler.Browser.PickDirectory(initial)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	writeOK(w, map[string]interface{}{
		"path":      path,
		"cancelled": path == "",
	})
}

// handleTags GET /api/tags
func (m *Module) handleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	tags, err := m.handler.Tag.ListLibrary()
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}

	writeOK(w, map[string]interface{}{"tags": tags})
}

// handleTagSuggestions GET/POST /api/tag-suggestions
func (m *Module) handleTagSuggestions(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// GET /api/tag-suggestions
	if path == "/api/tag-suggestions" && r.Method == http.MethodGet {
		suggestions, err := m.handler.Storage.SuggestionsListPending()
		if err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w, map[string]interface{}{"suggestions": suggestions})
		return
	}

	// POST /api/tag-suggestions/{id}/accept
	if strings.Contains(path, "/accept") && r.Method == http.MethodPost {
		idStr := getPathParam(path, "/api/tag-suggestions/")
		idStr = strings.TrimSuffix(idStr, "/accept")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, "bad_request", "无效建议 ID", http.StatusBadRequest)
			return
		}
		if err := m.handler.Tag.AcceptSuggestion(id); err != nil {
			writeError(w, "not_found", err.Error(), http.StatusNotFound)
			return
		}
		writeOK(w, map[string]bool{"ok": true})
		return
	}

	// POST /api/tag-suggestions/{id}/reject
	if strings.Contains(path, "/reject") && r.Method == http.MethodPost {
		idStr := getPathParam(path, "/api/tag-suggestions/")
		idStr = strings.TrimSuffix(idStr, "/reject")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, "bad_request", "无效建议 ID", http.StatusBadRequest)
			return
		}
		if err := m.handler.Tag.RejectSuggestion(id); err != nil {
			writeError(w, "not_found", err.Error(), http.StatusNotFound)
			return
		}
		writeOK(w, map[string]bool{"ok": true})
		return
	}

	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleFileDownloadHistory GET /api/files/download-history?relPath=...&hash=...
// 下载文件在指定 git 提交时的历史版本内容，返回文件字节流。
func (m *Module) handleFileDownloadHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	relPath := getQueryParam(r, "relPath")
	hash := getQueryParam(r, "hash")
	if relPath == "" || hash == "" {
		writeError(w, "bad_request", "缺少 relPath 或 hash 参数", http.StatusBadRequest)
		return
	}
	// 校验 hash 为 40 位 hex（git 完整 SHA-1），非法输入返回 400 而非 500
	if !isHexSHA1(hash) {
		writeError(w, "bad_request", "无效版本哈希", http.StatusBadRequest)
		return
	}

	content, err := m.handler.Git.ShowFileAt(relPath, hash)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}

	// 用 mime.FormatMediaType 生成 Content-Disposition，自动转义引号/反斜杠并剥离 CR/LF
	name := filepath.Base(relPath)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disposition)
	_, _ = io.WriteString(w, content)
}

// isHexSHA1 判断字符串是否为 40 位十六进制（git 完整哈希）
func isHexSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// handleFileResolve GET /api/files/resolve?relPath=...
// 返回对应文件 id（供详情弹窗关联版本历史）
func (m *Module) handleFileResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	relPath := getQueryParam(r, "relPath")
	if relPath == "" {
		writeError(w, "bad_request", "缺少 relPath 参数", http.StatusBadRequest)
		return
	}
	file, err := m.handler.Storage.FilesFindByRelPath(relPath)
	if err != nil || file == nil {
		writeError(w, "not_found", "文件未索引", http.StatusNotFound)
		return
	}
	writeOK(w, map[string]interface{}{"fileId": file.ID})
}

// handleTimeline GET /api/timeline
func (m *Module) handleTimeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	// 工作区未初始化时 Git 日志无意义，明确返回 not_configured 而非 500
	if m.workspacePath() == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}

	granularity := getQueryParam(r, "granularity")
	if granularity == "" {
		granularity = "day"
	}
	tag := getQueryParam(r, "tag")
	from := getQueryInt(r, "from", 0)
	to := getQueryInt(r, "to", 0)

	var tagFilter []string
	if tag != "" {
		tagFilter = []string{tag}
	}

	q := &contract.TimelineQuery{
		Granularity: granularity,
		TagFilter:   tagFilter,
		From:        int64(from),
		To:          int64(to),
	}

	nodes, err := m.handler.Timeline.Get(q)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}

	writeOK(w, map[string]interface{}{"nodes": nodes})
}

// handleQASessions GET /api/qa/sessions
func (m *Module) handleQASessions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		sessions, err := m.handler.QA.Sessions()
		if err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w, map[string]interface{}{"sessions": sessions})
		return
	}
	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleQA POST /api/qa
func (m *Module) handleQA(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req contract.QARequest
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}
		if req.Question == "" {
			writeError(w, "bad_request", "问题不能为空", http.StatusBadRequest)
			return
		}
		// 文件问答必须指定文件（修复 B-05：避免静默退化为全局问答）
		if req.Mode == "file" && req.FileID <= 0 {
			writeError(w, "bad_request", "文件问答需要先选择文件", http.StatusBadRequest)
			return
		}
		resp, err := m.handler.QA.Ask(&req)
		if err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w, resp)
		return
	}

	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleQAStream POST /api/qa/stream —— 流式问答，返回 SSE
func (m *Module) handleQAStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}

	var req contract.QARequest
	if err := readBody(r, &req); err != nil {
		writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
		return
	}
	if req.Question == "" {
		writeError(w, "bad_request", "问题不能为空", http.StatusBadRequest)
		return
	}
	if req.Mode == "file" && req.FileID <= 0 {
		writeError(w, "bad_request", "文件问答需要先选择文件", http.StatusBadRequest)
		return
	}

	// 设置 SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, "internal", "不支持流式", http.StatusInternalServerError)
		return
	}

	// 通过 r.Context().Done() 检测客户端断开
	cancel := make(chan struct{})
	go func() {
		<-r.Context().Done()
		close(cancel)
	}()

	chunks, done := m.handler.QA.AskStream(&req, cancel)

	for chunk := range chunks {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		// chunk 可能含真实换行（\n\n），直接写 SSE 会破坏帧结构导致前端丢内容。
		// 用 JSON 字符串编码传输，前端解码还原。
		chunkJSON, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", string(chunkJSON))
		flusher.Flush()
	}

	// 等待最终结果
	result := <-done
	if result == nil {
		// 防御：goroutine 异常退出时 done 可能关闭无值，避免 nil 解引用
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", `"问答中断"`)
		flusher.Flush()
		return
	}
	if result.Error != "" {
		// error 数据同样可能含换行，JSON 编码传输
		errJSON, _ := json.Marshal(result.Error)
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", string(errJSON))
		flusher.Flush()
		return
	}

	// 发送结束事件（含 sessionId 和 sources）
	type finalEvent struct {
		Done      bool                `json:"done"`
		SessionID int64               `json:"sessionId,omitempty"`
		Sources   []contract.QASource `json:"sources,omitempty"`
	}
	final := finalEvent{
		Done:      true,
		SessionID: result.SessionID,
		Sources:   result.Sources,
	}
	finalJSON, _ := json.Marshal(final)
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", string(finalJSON))
	flusher.Flush()
}

// handleQAByID GET/DELETE /api/qa/sessions/{id}
func (m *Module) handleQAByID(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// GET /api/qa/sessions/{id}/messages
	if strings.Contains(path, "/messages") && r.Method == http.MethodGet {
		idStr := getPathParam(path, "/api/qa/sessions/")
		idStr = strings.TrimSuffix(idStr, "/messages")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, "bad_request", "无效会话 ID", http.StatusBadRequest)
			return
		}
		messages, err := m.handler.Storage.QAMessagesBySession(id)
		if err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w, map[string]interface{}{"messages": messages})
		return
	}

	// DELETE /api/qa/sessions/{id}
	if r.Method == http.MethodDelete {
		idStr := getPathParam(path, "/api/qa/sessions/")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, "bad_request", "无效会话 ID", http.StatusBadRequest)
			return
		}
		if err := m.handler.QA.DeleteSession(id); err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w, map[string]bool{"ok": true})
		return
	}

	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleStats GET /api/stats
func (m *Module) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	if !m.handler.Stats.Enabled() {
		writeJSON(w, http.StatusOK, &Response{Code: "stats_disabled", Message: "统计已关闭", Data: map[string]bool{"enabled": false}})
		return
	}
	// 工作区未初始化时统计无数据，明确返回 not_configured 而非 500
	if m.workspacePath() == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}

	rng := getQueryParam(r, "range")
	from := getQueryInt(r, "from", 0)
	to := getQueryInt(r, "to", 0)

	metrics, err := m.handler.Stats.Summary(&contract.StatsRange{
		Range: rng,
		From:  int64(from),
		To:    int64(to),
	})
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}

	writeOK(w, map[string]interface{}{
		"enabled": true,
		"metrics": metrics,
	})
}

// handleStatsExport GET /api/stats/export
func (m *Module) handleStatsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	// 统计关闭或工作区未初始化时明确提示，避免 500（修复审计发现）
	if !m.handler.Stats.Enabled() {
		writeJSON(w, http.StatusOK, &Response{Code: "stats_disabled", Message: "统计已关闭", Data: map[string]bool{"enabled": false}})
		return
	}
	if m.workspacePath() == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}

	format := getQueryParam(r, "format")
	rng := getQueryParam(r, "range")

	content, err := m.handler.Stats.Export(format, &contract.StatsRange{Range: rng})
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
	} else {
		w.Header().Set("Content-Type", "text/markdown")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=report_%d.%s", time.Now().Unix(), format))
	w.Write([]byte(content))
}

// handleQueueStatus GET /api/queue/status 任务队列状态
func (m *Module) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	if m.handler.TaskQueue == nil {
		writeError(w, "not_ready", "任务队列未就绪", http.StatusServiceUnavailable)
		return
	}
	running, pending, paused := m.handler.TaskQueue.Status()
	writeOK(w, map[string]interface{}{
		"running": running,
		"pending": pending,
		"paused":  paused,
	})
}

// handleQueuePause POST /api/queue/pause 暂停任务队列
func (m *Module) handleQueuePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	if m.handler.TaskQueue == nil {
		writeError(w, "not_ready", "任务队列未就绪", http.StatusServiceUnavailable)
		return
	}
	if err := m.handler.TaskQueue.Pause(); err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// handleQueueResume POST /api/queue/resume 恢复任务队列
func (m *Module) handleQueueResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	if m.handler.TaskQueue == nil {
		writeError(w, "not_ready", "任务队列未就绪", http.StatusServiceUnavailable)
		return
	}
	if err := m.handler.TaskQueue.Resume(); err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// handleCommitAuto POST /api/commits/auto
func (m *Module) handleCommitAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}

	// 先尝试用 AI 生成提交备注
	aiMsg, aiErr := m.handler.Timeline.SuggestCommitMessage()

	if aiErr == nil && aiMsg != "" {
		// AI 成功，用 AI 备注手动提交
		hash, err := m.handler.Git.CommitManual(aiMsg)
		if err != nil {
			writeError(w, "internal", err.Error(), http.StatusInternalServerError)
			return
		}
		writeOK(w, map[string]string{"hash": hash, "message": aiMsg, "ai": "true"})
		return
	}

	// AI 不可用，回退到默认自动提交
	hash, skipped, err := m.handler.Git.CommitAuto(nil)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}

	if skipped {
		writeOK(w, map[string]bool{"skipped": true})
		return
	}
	writeOK(w, map[string]string{"hash": hash})
}

// handleCommitHead GET /api/commits/head —— 当前版本（HEAD）概要
func (m *Module) handleCommitHead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	head, err := m.handler.Git.Head()
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	writeOK(w, head)
}

// handleCommitList GET /api/commits/list —— 提交列表（每个提交含备注、id、改动文件明细）
func (m *Module) handleCommitList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	commits, err := m.handler.Git.Log()
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}

	type commitItem struct {
		Hash    string                 `json:"hash"`
		Time    int64                  `json:"time"`
		Message string                 `json:"message"`
		Author  string                 `json:"author"`
		Files   []*contract.CommitFile `json:"files"`
	}

	items := make([]*commitItem, 0, len(commits))
	for _, c := range commits {
		files, err := m.handler.Git.CommitFiles(c.Hash)
		if err != nil {
			files = nil
		}
		items = append(items, &commitItem{
			Hash:    c.Hash,
			Time:    c.Time,
			Message: c.Message,
			Author:  c.Author,
			Files:   files,
		})
	}

	writeOK(w, map[string]interface{}{"commits": items})
}

// handleCommitByHash POST /api/commits/{hash}/summary
func (m *Module) handleCommitByHash(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "/summary") && r.Method == http.MethodPost {
		hash := getPathParam(path, "/api/commits/")
		hash = strings.TrimSuffix(hash, "/summary")
		if hash == "" {
			writeError(w, "bad_request", "缺少提交哈希", http.StatusBadRequest)
			return
		}
		summary, err := m.handler.Timeline.GenerateSummary(hash)
		if err != nil {
			writeError(w, "not_found", err.Error(), http.StatusNotFound)
			return
		}
		writeOK(w, map[string]string{"summary": summary})
		return
	}
	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleCommitStatus GET /api/commits/status —— 列出当前未提交的变动
func (m *Module) handleCommitStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	status, err := m.handler.Git.Status()
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	type fileStatus struct {
		RelPath string `json:"relPath"`
		Code    string `json:"code"` // M/D/A/??
	}
	files := make([]fileStatus, 0, len(status))
	for rel, code := range status {
		if code == "" || code == " " {
			continue
		}
		files = append(files, fileStatus{RelPath: rel, Code: code})
	}
	writeOK(w, map[string]any{"files": files, "count": len(files)})
}

// handleCommitManual POST /api/commits/manual —— 用户自己写备注后提交全部变动
func (m *Module) handleCommitManual(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := readBody(r, &req); err != nil {
		writeError(w, "bad_request", err.Error(), http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		writeError(w, "bad_request", "提交备注不能为空", http.StatusBadRequest)
		return
	}

	// 无变更时返回 skipped，不报错（前端据此提示"无变更"）
	status, err := m.handler.Git.Status()
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	hasChanges := false
	for _, code := range status {
		if code != "" && code != " " {
			hasChanges = true
			break
		}
	}
	if !hasChanges {
		writeOK(w, map[string]interface{}{"skipped": true, "hash": ""})
		return
	}

	hash, err := m.handler.Git.CommitManual(msg)
	if err != nil {
		writeError(w, "internal", err.Error(), http.StatusInternalServerError)
		return
	}
	writeOK(w, map[string]string{"hash": hash, "message": msg})
}

// handleCommitSuggest POST /api/commits/suggest —— AI 根据未提交变动生成备注建议
func (m *Module) handleCommitSuggest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	suggestion, err := m.handler.Timeline.SuggestCommitMessage()
	if err != nil {
		writeError(w, "ai_unavailable", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeOK(w, map[string]string{"suggestion": suggestion})
}

// handleSettings GET/PUT /api/settings
func (m *Module) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, m.handler.Config.Snapshot())
	case http.MethodPut:
		// PUT /api/settings/secrets
		if strings.HasSuffix(r.URL.Path, "/secrets") {
			var req struct {
				LLMApiKey   string `json:"llmApiKey"`
				EmbedApiKey string `json:"embedApiKey"`
			}
			if err := readBody(r, &req); err != nil {
				writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
				return
			}
			if err := m.handler.Config.UpsertSecrets(req.LLMApiKey, req.EmbedApiKey); err != nil {
				writeError(w, "internal", err.Error(), http.StatusInternalServerError)
				return
			}
			writeOK(w, map[string]bool{"ok": true})
			return
		}

		// PUT /api/settings
		var req map[string]interface{}
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}

		// 热更新收集（修复 H-09：明确区分热更新项与需重启项）
		var newPythonPath, newCommand, newMarkitdownCmd string
		hasMarkitdown := false
		restartKeys := make(map[string]bool)

		for key, value := range req {
			if err := m.handler.Config.Set(key, value); err != nil {
				writeError(w, "bad_request", err.Error(), http.StatusBadRequest)
				return
			}
			// 同步 stats.enabled 到 stats 模块（运行时开关）
			if key == "stats.enabled" {
				if b, ok := value.(bool); ok {
					if err := m.handler.Stats.SetEnabled(b); err != nil {
						writeError(w, "internal", err.Error(), http.StatusInternalServerError)
						return
					}
				}
			}
			// 标记 MarkItDown 热更新项
			switch key {
			case "markitdown.pythonPath":
				if s, ok := value.(string); ok {
					newPythonPath = s
					hasMarkitdown = true
				}
			case "markitdown.command":
				if s, ok := value.(string); ok {
					newCommand = s
					hasMarkitdown = true
				}
			case "markitdown.markitdownCmd":
				if s, ok := value.(string); ok {
					newMarkitdownCmd = s
					hasMarkitdown = true
				}
			default:
				// 其余配置项属于需重启生效项（或已由特定模块热更新）
				if key != "stats.enabled" {
					restartKeys[key] = true
				}
			}
		}

		// 热更新 Extract（MarkItDown）运行参数
		if hasMarkitdown && m.handler.Extract != nil {
			m.handler.Extract.ApplyConfig(newPythonPath, newCommand, newMarkitdownCmd)
		}

		// 返回需重启生效的配置项提示，避免“假成功”
		restartList := make([]string, 0, len(restartKeys))
		for k := range restartKeys {
			restartList = append(restartList, k)
		}
		writeOK(w, map[string]interface{}{
			"ok":              true,
			"restartRequired": restartList,
		})
	default:
		writeError(w, "bad_request", "不支持的请求方法", http.StatusBadRequest)
	}
}

// handleTest POST /api/test/{type}
func (m *Module) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}

	path := r.URL.Path

	// POST /api/test/markitdown
	if strings.HasSuffix(path, "/markitdown") {
		var req struct {
			PythonPath string `json:"pythonPath"`
			Command    string `json:"command"`
		}
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}
		ok, msg := m.handler.Extract.Probe(req.PythonPath, req.Command)
		writeOK(w, map[string]interface{}{"ok": ok, "message": msg})
		return
	}

	// POST /api/test/llm
	if strings.HasSuffix(path, "/llm") {
		var req struct {
			Type        string  `json:"type"` // chat|embed
			BaseURL     string  `json:"baseUrl"`
			Model       string  `json:"model"`
			ApiKey      string  `json:"apiKey"`
			Temperature float64 `json:"temperature"`
		}
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}
		var err error
		if req.Type == "embed" {
			err = m.handler.LLM.TestEmbedWith(req.BaseURL, req.ApiKey, req.Model)
		} else {
			err = m.handler.LLM.TestChatWith(req.BaseURL, req.ApiKey, req.Model, req.Temperature)
		}
		if err != nil {
			writeOK(w, map[string]interface{}{"ok": false, "message": err.Error()})
			return
		}
		writeOK(w, map[string]interface{}{"ok": true, "message": "测试通过"})
		return
	}

	writeError(w, "bad_request", "不支持的测试类型", http.StatusBadRequest)
}

// handleDetectPython GET /api/python/detect —— 尝试检测系统中 Python 解释器并回显版本
// 同时检测 markitdown 可执行文件路径。
func (m *Module) handleDetectPython(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	type pyResult struct {
		Path          string `json:"path"`
		Ok            bool   `json:"ok"`
		Version       string `json:"version,omitempty"`
		MarkitdownCmd string `json:"markitdownCmd,omitempty"`
		Error         string `json:"error,omitempty"`
	}
	found := pyResult{}

	// 所有候选：优先真实 Python 安装目录，跳过 WindowsApps Store 壳
	candidates := []string{}

	// Windows 常见路径
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Python", "bin", "python.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Python", "pythoncore-3.14-64", "python.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Python", "pythoncore-3.13-64", "python.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Python", "pythoncore-3.12-64", "python.exe"),
			`C:\Python314\python.exe`,
			`C:\Python313\python.exe`,
			`C:\Python312\python.exe`,
			`C:\Python311\python.exe`,
			`C:\Python310\python.exe`,
			`C:\Python39\python.exe`,
		)
	}
	// PATH 里的可执行名（最后兜底，因为可能找到 WindowsApps 壳）
	candidates = append(candidates, "python", "python3", "py")

	for _, c := range candidates {
		var pyExe string
		if filepath.IsAbs(c) {
			pyExe = c
		} else {
			p, err := exec.LookPath(c)
			if err != nil {
				continue
			}
			// 跳过 WindowsApps 下的 Store 壳
			if runtime.GOOS == "windows" && strings.Contains(strings.ToLower(p), "windowsapps") {
				continue
			}
			pyExe = p
		}

		if _, err := os.Stat(pyExe); err != nil {
			continue
		}
		ver := probeVersion(pyExe)
		if ver == "" {
			continue
		}
		found.Path = pyExe
		found.Ok = true
		found.Version = ver

		// 按 python.exe 路径推导 markitdown 路径
		pyDir := filepath.Dir(pyExe)
		scriptsDir := pyDir
		// 如果 python.exe 在 bin/ 下，Python 根目录在上一级
		if filepath.Base(pyDir) == "bin" {
			scriptsDir = filepath.Dir(pyDir)
		}
		// 如果 Scripts 不在 python.exe 同目录，尝试 pythoncore 子目录
		var mdCandidates []string
		if filepath.Base(pyDir) == "bin" {
			// bin/ 下无 Scripts，尝试 pythoncore-<ver>-64/Scripts
			mdCandidates = append(mdCandidates, filepath.Join(scriptsDir, "pythoncore-3.14-64", "Scripts", "markitdown.exe"))
			mdCandidates = append(mdCandidates, filepath.Join(scriptsDir, "pythoncore-3.13-64", "Scripts", "markitdown.exe"))
			mdCandidates = append(mdCandidates, filepath.Join(scriptsDir, "pythoncore-3.12-64", "Scripts", "markitdown.exe"))
		}
		mdCandidates = append(mdCandidates,
			filepath.Join(scriptsDir, "Scripts", "markitdown.exe"),
			filepath.Join(pyDir, "Scripts", "markitdown.exe"),
			filepath.Join(scriptsDir, "markitdown.exe"),
		)
		for _, md := range mdCandidates {
			if _, err := os.Stat(md); err == nil {
				found.MarkitdownCmd = md
				break
			}
		}
		break
	}

	if !found.Ok {
		found.Error = "未找到可用的 Python 解释器"
	}

	writeOK(w, map[string]interface{}{"results": []pyResult{found}})
}

// probeVersion 运行 --version 返回简化版本号（如 "3.12.2"）或空字符串
func probeVersion(pythonExe string) string {
	cmd := exec.Command(pythonExe, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	ver := strings.TrimSpace(string(out))
	ver = strings.TrimPrefix(ver, "Python ")
	ver = strings.TrimPrefix(ver, "python ")
	return ver
}

func runtimeGOOS() string {
	return runtime.GOOS
}

// Addr 返回监听地址
func (m *Module) Addr() string {
	return m.addr
}

// Stop 停止服务
func (m *Module) Stop() error {
	if m.server != nil {
		return m.server.Close()
	}
	return nil
}
