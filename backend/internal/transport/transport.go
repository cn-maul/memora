// Package transport 传输适配模块
// REST 路由、SSE 推送、参数校验、DTO 转换
// 仅监听 127.0.0.1
package transport

import (
	"fmt"
	"io/fs"
	"memora/internal/events"
	"memora/internal/logx"
	"memora/internal/taskqueue"
	"net"
	"net/http"
	"sync"
	"time"
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

	// RebuildWorkspace 工作区初始化后由处理器回调,用于原地重建工作区相关模块（修复 B-01）。
	// 由装配层注入。
	RebuildWorkspace func(workspace string) error

	// TriggerReindex 触发全量重建索引的回调,由装配层注入（P0-03）：
	// 经任务队列合并执行,避免 transport 各处独立 goroutine 并发 FullReindex 破坏索引。
	// 为空时退化直连 Index.FullReindex（测试/独立装配场景）。
	TriggerReindex func() error

	// TaskQueue 任务队列（暂停/恢复/状态,修复 B-03）
	TaskQueue TaskQueueAPI

	// GenerationFunc 返回当前工作区代标识（如 "w1"），由装配层注入
	// （assembler.RuntimeManager.Generation()）。为 nil 时 /ready 跳过 generation
	// 检查并标注（generationChecked=false），/diagnostics 的 generation 置空。
	GenerationFunc func() string

	// Version 构建/发布版本标识，供诊断输出（/diagnostics）。为空时按 "dev" 处理。
	Version string
}

// TaskQueueAPI 任务队列接口
type TaskQueueAPI interface {
	Pause() error
	Resume() error
	Status() (running, pending int, paused bool)
	Submit(task *taskqueue.Task) error
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

	// startedAt 模块启动时刻，供 /diagnostics 计算 uptimeSec。
	startedAt time.Time

	// maxBodyBytes 请求体大小上限（字节）。默认 32MB,可通过注入的 Module 覆写,
	// 但不对外提供 setter（避免扩大改动面）。
	maxBodyBytes int64

	mu       sync.Mutex
	sseConns map[chan string]struct{}
}

// ctxKey context 键类型,避免与其他包的值冲突
type ctxKey int

const (
	// ctxKeyRequestID 请求 ID 在 context 中的键
	ctxKeyRequestID ctxKey = iota
)

// SetWebDir 设置前端静态资源磁盘目录（如 MEMORA_WEB）。为空则不托管磁盘静态资源。
func (m *Module) SetWebDir(dir string) {
	m.webDir = dir
}

// SetWebFS 设置内嵌的前端静态资源文件系统（go:embed 产物）。webDir 优先级更高。
func (m *Module) SetWebFS(f fs.FS) {
	m.webFS = f
}

// New 创建传输模块
func New(h *APIHandler, events EventBus) *Module {
	m := &Module{
		mux:          http.NewServeMux(),
		handler:      h,
		events:       events,
		startedAt:    time.Now(),
		maxBodyBytes: 32 << 20, // 默认 32MB 请求体上限
		sseConns:     make(map[chan string]struct{}),
	}

	// 订阅 events,桥接到 SSE
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

	// http.Server 安全默认值（P1-10）：
	//   ReadHeaderTimeout/ReadTimeout 限制慢速/慢烤请求；IdleTimeout 回收空闲 keep-alive 连接。
	//   不设 WriteTimeout：流式问答（handleQAStream）与 SSE 需长时间写出,
	//   由 handler 内 http.NewResponseController(w).SetWriteDeadline 做空闲写超时。
	m.server = &http.Server{
		Addr:              addr,
		Handler:           m.withProtection(withCORS(m.mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		logx.Info("transport", "HTTP 服务启动", "addr", addr)
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logx.Error("transport", "服务异常", "err", err.Error())
		}
	}()

	return nil
}

// findAvailablePort 找可用端口（127.0.0.1,随机端口,冲突自增）
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

// registerRoutes 注册所有 API 路由
func (m *Module) registerRoutes() {
	// 可观测性（Phase 5）：存活 / 就绪 / 诊断摘要
	m.mux.HandleFunc("/health", m.handleHealth)
	m.mux.HandleFunc("/ready", m.handleReady)
	m.mux.HandleFunc("/diagnostics", m.handleDiagnostics)

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

	// 文件历史版本下载
	m.mux.HandleFunc("/api/files/download-history", m.handleFileDownloadHistory)

	// 最近文件（时间窗筛选,按 mtime 倒序）
	m.mux.HandleFunc("/api/files/recent", m.handleFilesRecent)

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
