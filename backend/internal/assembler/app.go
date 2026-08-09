// Package assembler 装配根：按顺序 new 并接线
package assembler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"memora/internal/browser"
	"memora/internal/config"
	"memora/internal/events"
	"memora/internal/extract"
	"memora/internal/git"
	"memora/internal/index"
	"memora/internal/llm"
	"memora/internal/qa"
	"memora/internal/search"
	"memora/internal/stats"
	"memora/internal/storage"
	"memora/internal/tag"
	"memora/internal/taskqueue"
	"memora/internal/timeline"
	"memora/internal/transport"
	"memora/internal/watch"
	"memora/internal/web"
)

// Browser 文件浏览适配器（实现 transport.BrowserAPI）
type Browser struct{}

// asInt 将 config.Get 的返回值转为 int（兼容 int/float64/json.Number），
// 修复 config 返回 int 而旧代码用 .(float64) 断言导致用户配置恒不生效的问题。
func asInt(v interface{}) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

func (Browser) ListDir(workspace, subPath string) ([]*browser.DirEntry, error) {
	return browser.ListDir(workspace, subPath)
}
func (Browser) SearchByName(workspace, query string, limit int) ([]*browser.SearchResult, int, error) {
	return browser.SearchByName(workspace, query, limit)
}
func (Browser) PickDirectory(initial string) (string, error) {
	return browser.PickDirectory(initial)
}
func (Browser) OpenFile(workspace, relPath string) error {
	return browser.OpenFile(workspace, relPath)
}

// App 应用实例
type App struct {
	Config    *config.Module
	Events    *events.Module
	Storage   *storage.Module
	LLM       *llm.Module
	Git       *git.Module
	Watch     *watch.Module
	Extract   *extract.Module
	Index     *index.Module
	Tag       *tag.Module
	Search    *search.Module
	Timeline  *timeline.Module
	QA        *qa.Module
	Stats     *stats.Module
	TaskQueue *taskqueue.Module
	Transport *transport.Module
	Browser   Browser
	handler   *transport.APIHandler // 传输层模块引用，工作区重建时原地更新

	ctx    context.Context
	wsPath string
}

// NewApp 装配所有模块
func NewApp(ctx context.Context, configPath string) (*App, error) {
	// 1. 先确定 config 路径
	// 未显式传入时，优先取可执行文件同目录的 .memora/config.json（自包含，随 exe 走），
	// 这样无论从哪个目录启动，都读取同一份配置。
	if configPath == "" {
		if exe, err := os.Executable(); err == nil {
			configPath = filepath.Join(filepath.Dir(exe), ".memora", "config.json")
		} else {
			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				return nil, fmt.Errorf("[assembler] 获取当前目录失败: %w", cwdErr)
			}
			configPath = filepath.Join(cwd, ".memora", "config.json")
		}
	}

	// 2. 创建事件模块（基础层）
	evt := events.New()

	// 3. 创建配置模块（基础层）
	cfg, err := config.New(configPath, evt)
	if err != nil {
		return nil, fmt.Errorf("[assembler] 创建配置模块失败: %w", err)
	}

	app := &App{
		Config: cfg,
		Events: evt,
		ctx:    ctx,
	}

	// 4. 创建基础网关模块（LLM / Git），供工作区模块引用
	app.LLM = llm.New(cfg)
	app.Git = git.New(cfg)

	// 5. 创建存储模块（基础层）
	// 优先使用工作区下的 .memora（若已配置），否则使用可执行文件旁的 .memora
	dataDir := filepath.Join(filepath.Dir(configPath))
	if cfg.Workspace() != "" {
		dataDir = filepath.Join(cfg.Workspace(), ".memora")
	}
	app.wsPath = cfg.Workspace()

	if err := app.buildWorkspaceModules(dataDir, cfg.Workspace()); err != nil {
		return nil, err
	}

	// 15. 创建任务队列
	taskHandler := app.createTaskHandler()
	app.TaskQueue = taskqueue.New(taskHandler, evt)

	// 16. 创建传输模块
	tr := app.createTransport(evt)
	app.Transport = tr

	return app, nil
}

// buildWorkspaceModules 构造（或重建）所有依赖工作区/数据目录的模块。
// 用于首次装配以及工作区初始化后的原地重建（修复 B-01）。
func (a *App) buildWorkspaceModules(dataDir, workspace string) error {
	cfg := a.Config

	_, _, _, dim := cfg.GetEmbedConfig()

	st, err := storage.New(dataDir, dim)
	if err != nil {
		return fmt.Errorf("[assembler] 创建存储模块失败: %w", err)
	}
	// 若已存在旧存储，先关闭，避免泄漏数据库句柄
	if a.Storage != nil && a.Storage != st {
		_ = a.Storage.Close()
	}
	a.Storage = st

	// 提取模块
	pythonPathVal, _ := cfg.Get("markitdown.pythonPath")
	commandVal, _ := cfg.Get("markitdown.command")
	markitdownCmdVal, _ := cfg.Get("markitdown.markitdownCmd")
	pythonPathStr, _ := pythonPathVal.(string)
	commandStr, _ := commandVal.(string)
	markitdownCmdStr, _ := markitdownCmdVal.(string)
	if commandStr == "" {
		commandStr = `python -m markitdown "{file}"`
	}
	extractMod, err := extract.New(dataDir, pythonPathStr, commandStr, markitdownCmdStr)
	if err != nil {
		return fmt.Errorf("[assembler] 创建提取模块失败: %w", err)
	}
	a.Extract = extractMod

	// 索引模块
	chunkSize, _ := cfg.Get("index.chunkSize")
	chunkOverlap, _ := cfg.Get("index.chunkOverlap")
	cs := asInt(chunkSize)
	co := asInt(chunkOverlap)
	if cs == 0 {
		cs = 2000
	}
	if co == 0 {
		co = 256
	}
	a.Index = index.New(st, extractMod, a.LLM, a.Events, workspace, cs, co, dim)

	// 标签模块
	a.Tag = tag.New(st, a.LLM, a.Events)

	// 搜索模块
	a.Search = search.New(a.Index, a.LLM, st)

	// 时间线模块
	a.Timeline = timeline.New(a.Git, st, a.LLM, a.Events, workspace)

	// 问答模块
	maxCtxVal, _ := cfg.Get("qa.maxContextChars")
	mcc := asInt(maxCtxVal)
	if mcc == 0 {
		mcc = 30000
	}
	a.QA = qa.New(st, a.LLM, a.Events, mcc)

	// 统计模块
	a.Stats = stats.New(a.Git, st, cfg)

	// 监视模块（需工作目录）
	a.Watch = nil
	if workspace != "" {
		_, debounceSec := cfg.GetAutoCommitConfig()
		w, err := watch.New(workspace, debounceSec)
		if err != nil {
			return fmt.Errorf("[assembler] 创建监视模块失败: %w", err)
		}
		a.Watch = w
	}

	return nil
}

// RebuildWorkspace 工作区初始化后原地重建所有工作区相关模块（修复 B-01）。
// 停止旧监视器 → 按新工作区重建存储/提取/索引/标签/搜索/时间线/问答/统计/监视
// → 原地更新传输层模块引用 → 启动新监视并触发全量重建。
// 注意：本方法不应在 app.Run() 之前调用（此时 watch/consume 尚未启动）。
func (a *App) RebuildWorkspace(workspace string) error {
	// 1. 停止旧监视器（若在运行）
	if a.Watch != nil {
		_ = a.Watch.Stop()
	}

	// 2. 更新工作区路径
	a.wsPath = workspace
	a.Config.Set("workspace.path", workspace)

	// 3. 重建工作区模块（内部会关闭旧 Storage）
	dataDir := filepath.Join(workspace, ".memora")
	if err := a.buildWorkspaceModules(dataDir, workspace); err != nil {
		return err
	}

	// 4. 原地更新传输层模块引用
	if a.handler != nil {
		a.handler.Storage = a.Storage
		a.handler.Extract = a.Extract
		a.handler.Index = a.Index
		a.handler.Tag = a.Tag
		a.handler.Search = a.Search
		a.handler.Timeline = a.Timeline
		a.handler.QA = a.QA
		a.handler.Stats = a.Stats
		a.handler.Watch = a.Watch
	}

	// 5. 确保 Git 仓库并恢复待处理任务
	if err := a.Git.EnsureRepo(workspace); err != nil {
		fmt.Printf("[app] Git 初始化警告: %v\n", err)
	}
	if err := a.Storage.RecoverPending(); err != nil {
		fmt.Printf("[app] 恢复待处理任务警告: %v\n", err)
	}
	if _, err := a.Storage.VectorsLoadAll(); err != nil {
		fmt.Printf("[app] 加载向量索引警告: %v\n", err)
	}

	// 6. 启动新监视器
	if a.Watch != nil {
		if err := a.Watch.Start(); err != nil {
			fmt.Printf("[app] 文件监视启动警告: %v\n", err)
		}
		go a.consumeWatchChanges()
	}

	// 7. 触发全量重建索引
	go func() {
		if err := a.Index.FullReindex(); err != nil {
			fmt.Printf("[app] 全量重建索引警告: %v\n", err)
		}
	}()

	return nil
}
func (a *App) createTaskHandler() taskqueue.TaskHandler {
	return func(task *taskqueue.Task) error {
		switch task.Type {
		case "extract":
			if payload, ok := task.Payload.(map[string]interface{}); ok {
				if relPath, ok := payload["relPath"].(string); ok {
					return a.Index.Incremental([]string{relPath}, nil)
				}
			}
			fmt.Printf("[app] 提取任务: %v\n", task.Payload)
		case "tag":
			if payload, ok := task.Payload.(map[string]interface{}); ok {
				if fileID, ok := asInt64(payload["fileId"]); ok && fileID > 0 {
					file, err := a.Storage.FilesGet(fileID)
					if err == nil && file != nil {
						return a.Tag.ProcessFile(file)
					}
				}
			}
		case "summarize":
			if payload, ok := task.Payload.(map[string]interface{}); ok {
				if hash, ok := payload["commitHash"].(string); ok {
					_, err := a.Timeline.GenerateSummary(hash)
					return err
				}
			}
		case "reindex":
			return a.Index.FullReindex()
		case "delete_index":
			if relPath, ok := task.Payload.(string); ok {
				return a.Index.DeleteFile(relPath)
			}
		case "auto_commit":
			if payload, ok := task.Payload.(map[string]interface{}); ok {
				if files, ok := payload["files"].([]string); ok {
					hash, skipped, err := a.Git.CommitAuto(files)
					if err != nil {
						return err
					}
					if !skipped {
						// 广播提交完成事件
						a.Events.Notify("commit_done", map[string]interface{}{
							"hash":  hash,
							"files": files,
						})
					}
					return nil
				}
			}
			hash, skipped, err := a.Git.CommitAuto(nil)
			if err != nil {
				return err
			}
			if !skipped {
				a.Events.Notify("commit_done", map[string]interface{}{
					"hash": hash,
				})
			}
			return err
		default:
			return fmt.Errorf("[app] 未知任务类型: %s", task.Type)
		}
		return nil
	}
}

// createTransport 创建传输模块
func (a *App) createTransport(evt *events.Module) *transport.Module {
	handler := &transport.APIHandler{
		Config:   a.Config,
		Storage:  a.Storage,
		Git:      a.Git,
		Extract:  a.Extract,
		LLM:      a.LLM,
		Search:   a.Search,
		Tag:      a.Tag,
		Timeline: a.Timeline,
		QA:       a.QA,
		Stats:    a.Stats,
		Index:    a.Index,
		Watch:    a.Watch,
		Browser:  a.Browser,
	}
	// 保存 handler 引用，供工作区重建时原地更新各模块
	a.handler = handler
	// 注入工作区重建回调，供 /workspace/init 处理器调用（修复 B-01）
	handler.RebuildWorkspace = func(workspace string) error {
		return a.RebuildWorkspace(workspace)
	}
	// 注入任务队列，暴露暂停/恢复/状态接口（修复 B-03）
	handler.TaskQueue = a.TaskQueue

	tr := transport.New(handler, evt)

	// 前端静态资源：优先级 MEMORA_WEB（磁盘目录，开发/覆盖） > 内嵌文件系统（go:embed）。
	// 内嵌为空（dist 未构建）且未设置 MEMORA_WEB 时，仅提供 API，不托管页面。
	if dir := os.Getenv("MEMORA_WEB"); dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			tr.SetWebDir(dir)
		}
	} else if web.HasIndex() {
		tr.SetWebFS(web.FS())
	}

	return tr
}

// Run 启动应用
func (a *App) Run() error {
	fmt.Println("[app] Memora 启动中...")

	// 1. 注册路由
	if err := a.Transport.Handle(nil); err != nil {
		return fmt.Errorf("[app] 启动 HTTP 服务失败: %w", err)
	}

	// 2. 初始化 Git 仓库
	if a.wsPath != "" {
		if err := a.Git.EnsureRepo(a.wsPath); err != nil {
			fmt.Printf("[app] Git 初始化警告: %v\n", err)
		}

		// 3. 启动文件监视
		if a.Watch != nil {
			if err := a.Watch.Start(); err != nil {
				fmt.Printf("[app] 文件监视启动警告: %v\n", err)
			}
		}

		// 4. 恢复待处理任务并重新入队
		if err := a.Storage.RecoverPending(); err != nil {
			fmt.Printf("[app] 恢复待处理任务警告: %v\n", err)
		}
		// 将所有 pending 文件重新入队
		pendingFiles, _, err := a.Storage.FilesList("pending", "", 0, 5000, "")
		if err == nil {
			for _, f := range pendingFiles {
				a.TaskQueue.Submit(&taskqueue.Task{
					Type:    "extract",
					Payload: map[string]interface{}{"relPath": f.RelPath, "fileId": float64(f.ID)},
				})
			}
			if len(pendingFiles) > 0 {
				fmt.Printf("[app] 已重新入队 %d 个待处理文件\n", len(pendingFiles))
			}
		}

		// 5. 加载内存向量索引
		if _, err := a.Storage.VectorsLoadAll(); err != nil {
			fmt.Printf("[app] 加载向量索引警告: %v\n", err)
		}
	}

	// 5. 消费文件变更通道
	if a.Watch != nil {
		go a.consumeWatchChanges()
	}

	// 6. 订阅 index_done 事件，自动派发打标任务
	a.subscribeIndexDone()

	// 7. 订阅 commit_done 事件，自动派发摘要任务
	a.subscribeCommitDone()

	// 8. 定期扫描 pending 文件，自动入队索引（修复 watch 漏检或防抖未触发的情况）
	go a.pollPendingFiles()

	// 9. 打开浏览器进入前端界面（自包含模式，纯网页形态）
	if addr := a.Transport.Addr(); addr != "" {
		url := "http://" + addr
		fmt.Printf("[app] 前端地址: %s\n", url)
		if err := openBrowser(url); err != nil {
			fmt.Printf("[app] 打开浏览器失败: %v\n", err)
		}
	}

	fmt.Println("[app] Memora 已就绪")
	return nil
}

// openBrowser 用系统默认浏览器打开 url。失败不影响服务运行。
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// subscribeIndexDone 订阅索引完成事件（index_progress with done=true），自动派发打标任务
func (a *App) subscribeIndexDone() {
	a.Events.Subscribe("index_progress", func(data interface{}) {
		if payload, ok := data.(map[string]interface{}); ok {
			done, _ := payload["done"].(bool)
			if !done {
				return
			}
			// fileId 由 index 模块以 Go int64 直接写入（非 JSON 编解码），
			// 需兼容 int64/int/float64（修复 H-02）。
			if fileID, ok := asInt64(payload["fileId"]); ok && fileID > 0 {
				a.TaskQueue.Submit(&taskqueue.Task{
					Type:    "tag",
					Payload: map[string]interface{}{"fileId": fileID},
				})
			}
		}
	})
}

// asInt64 将事件负载中的数值字段统一转为 int64，兼容 int64/int/float64/json.Number。
func asInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

// subscribeCommitDone 订阅提交完成事件，自动派发摘要任务
func (a *App) subscribeCommitDone() {
	a.Events.Subscribe("commit_done", func(data interface{}) {
		if payload, ok := data.(map[string]interface{}); ok {
			if hash, ok := payload["hash"].(string); ok {
				a.TaskQueue.Submit(&taskqueue.Task{
					Type:    "summarize",
					Payload: map[string]interface{}{"commitHash": hash},
				})
			}
		}
	})
}

// isHeavyDir 判断是否为应跳过的重型/非文档目录（避免每轮全量遍历扫描 node_modules 等）
func isHeavyDir(name string) bool {
	switch name {
	case "node_modules", "bin", "obj", "dist", "build", "out", "target", "vendor",
		"__pycache__", ".venv", "venv", ".idea", ".vscode", ".mvn", ".gradle":
		return true
	}
	return false
}

// pollPendingFiles 定期扫描磁盘新文件 + 数据库中 pending 文件，自动入队索引。
// 扫描间隔从 config.index.scanIntervalSec 读取，运行时修改可立即生效。
func (a *App) pollPendingFiles() {
	for {
		interval := 8
		if v, err := a.Config.Get("index.scanIntervalSec"); err == nil {
			if n, ok := v.(int); ok && n > 0 {
				interval = n
			}
		}
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(time.Duration(interval) * time.Second):
		}

		// 1. 扫描磁盘新文件（不在数据库中的）
		if a.wsPath != "" {
			filepath.Walk(a.wsPath, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					if info != nil && info.IsDir() {
						if strings.HasPrefix(info.Name(), ".") || isHeavyDir(info.Name()) {
							return filepath.SkipDir
						}
					}
					return nil
				}
				relPath, _ := filepath.Rel(a.wsPath, path)
				if relPath == "" {
					return nil
				}
				// 只处理支持的文件类型
				ext := strings.ToLower(filepath.Ext(relPath))
				switch ext {
				case ".pdf", ".docx", ".txt", ".md":
				default:
					return nil
				}
				// 检查是否已在数据库中
				existing, _ := a.Storage.FilesFindByRelPath(relPath)
				if existing != nil {
					return nil
				}
				// 新文件，提交提取任务
				a.TaskQueue.Submit(&taskqueue.Task{
					Type:    "extract",
					Payload: map[string]interface{}{"relPath": relPath},
				})
				return nil
			})
		}

		// 2. 扫描数据库中 pending 状态的文件（已入库但未处理或处理中断）
		files, _, err := a.Storage.FilesList("pending", "", 0, 100, "")
		if err != nil {
			continue
		}
		for _, f := range files {
			a.TaskQueue.Submit(&taskqueue.Task{
				Type:    "extract",
				Payload: map[string]interface{}{"relPath": f.RelPath, "fileId": float64(f.ID)},
			})
		}
	}
}

// Shutdown 优雅关闭
func (a *App) Shutdown() {
	fmt.Println("[app] 正在关闭...")

	if a.Watch != nil {
		a.Watch.Stop()
	}

	a.TaskQueue.CancelAll()

	// 等待当前正在执行的任务完成，避免运行中的任务访问已关闭的 SQLite（修复 M-08）
	drained := a.TaskQueue.Wait(3 * time.Second)
	if !drained {
		fmt.Println("[app] 等待任务结束超时，仍有任务在执行")
	}

	if a.Storage != nil {
		a.Storage.Close()
	}

	if a.Transport != nil {
		a.Transport.Stop()
	}

	fmt.Println("[app] 已关闭")
}

// Quit 退出
func (a *App) Quit() {
	a.Shutdown()
	os.Exit(0)
}

// consumeWatchChanges 消费文件变更通道，将变更提交到任务队列
func (a *App) consumeWatchChanges() {
	for change := range a.Watch.Changes() {
		// 新增文件入队索引任务
		for _, f := range change.Added {
			a.TaskQueue.Submit(&taskqueue.Task{
				Type:    "extract",
				Payload: map[string]interface{}{"relPath": f},
			})
		}
		// 修改文件入队索引任务
		for _, f := range change.Modified {
			a.TaskQueue.Submit(&taskqueue.Task{
				Type:    "extract",
				Payload: map[string]interface{}{"relPath": f},
			})
		}
		for _, f := range change.Removed {
			a.TaskQueue.Submit(&taskqueue.Task{
				Type:    "delete_index",
				Payload: f,
			})
		}
		// 触发自动提交
		if len(change.Modified) > 0 || len(change.Removed) > 0 {
			allFiles := append(change.Modified, change.Removed...)
			a.TaskQueue.Submit(&taskqueue.Task{
				Type:    "auto_commit",
				Payload: map[string]interface{}{"files": allFiles},
			})
		}

		// 广播文件变更事件
		a.Events.Notify("files_changed", map[string]interface{}{
			"added":    change.Added,
			"modified": change.Modified,
			"removed":  change.Removed,
		})
	}
}

// ShowWindow 显示窗口（空实现，纯网页形态由浏览器负责）
func (a *App) ShowWindow() {}
