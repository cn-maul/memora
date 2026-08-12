// Package assembler 装配根：按顺序 new 并接线
package assembler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"memora/internal/browser"
	"memora/internal/config"
	"memora/internal/contract"
	"memora/internal/credstore"
	"memora/internal/events"
	"memora/internal/extract"
	"memora/internal/git"
	"memora/internal/index"
	"memora/internal/llm"
	"memora/internal/logx"
	"memora/internal/qa"
	"memora/internal/search"
	"memora/internal/stats"
	"memora/internal/storage"
	"memora/internal/tag"
	"memora/internal/taskqueue"
	"memora/internal/timeline"
	"memora/internal/watch"
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
	Browser   Browser

	runtime   *RuntimeManager // 工作区运行时管理器（generation / 原子交换）
	credStore credstore.Store // 凭据存储（Windows DPAPI / 其他平台兜底）

	ctx    context.Context
	cancel context.CancelFunc // 关闭时取消后台轮询（reconcileLoop 等）

	// reconcile 低频 reconciliation 参数（默认 base=60s, max=300s, factor=2；测试可覆盖为毫秒级）
	reconcile reconcileSettings

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

	// 派生可取消 context，供关闭时停止 reconcileLoop 等后台轮询（P0-04）
	runCtx, runCancel := context.WithCancel(ctx)

	app := &App{
		Config: cfg,
		Events: evt,
		ctx:    runCtx,
		cancel: runCancel,
	}

	// 4. 创建基础网关模块（LLM / Git），供工作区模块引用
	app.LLM = llm.New(cfg)
	app.Git = git.New(cfg)

	// 5. 确定数据目录：优先工作区下的 .memora（若已配置），否则可执行文件旁的 .memora
	dataDir := filepath.Join(filepath.Dir(configPath))
	if cfg.Workspace() != "" {
		dataDir = filepath.Join(cfg.Workspace(), ".memora")
	}
	app.wsPath = cfg.Workspace()

	// 6. 凭据存储：Windows 用 DPAPI 加密落盘，其他平台用文件兜底；
	//    启动时迁移配置中的旧明文 api_key（迁移失败保留原明文，不破坏可用性）。
	//    llm 模块后续优先从凭据存储读取密钥（见 llm.SetCredStore）。
	if cs, cerr := credstore.New(dataDir); cerr == nil {
		app.credStore = cs
		app.LLM.SetCredStore(cs)
		if cs.HasPlaintextMigration() {
			if merr := cfg.MigrateSecretsToCredStore(cs); merr != nil {
				logx.Warn("app", "凭据迁移失败，密钥继续使用配置明文", "err", merr.Error())
			}
		}
	} else {
		logx.Warn("app", "创建凭据存储失败，密钥沿用配置明文", "err", cerr.Error())
	}

	// 7. 创建运行时管理器并构建首个工作区 Runtime
	app.runtime = newRuntimeManager()
	if err := app.buildWorkspaceRuntime(dataDir, cfg.Workspace()); err != nil {
		return nil, err
	}

	// 15. 创建任务队列
	taskHandler := app.createTaskHandler()
	app.TaskQueue = taskqueue.New(taskHandler, evt)

	return app, nil
}

// buildWorkspaceRuntime 构建并提交当前工作区 Runtime（仅用于首次装配）。
func (a *App) buildWorkspaceRuntime(dataDir, workspace string) error {
	gen := a.runtime.beginBuild()
	rt, err := a.buildRuntime(gen, dataDir, workspace)
	if err != nil {
		return err
	}
	a.runtime.commit(rt)
	a.applyRuntimeModules(rt)
	return nil
}

// applyRuntimeModules 将 Runtime 的模块引用一次性同步到 App 字段。
// 异步普通字段用于与既有代码（Run/poll/consume/task handler）保持兼容。
func (a *App) applyRuntimeModules(rt *Runtime) {
	a.Storage = rt.Storage
	a.Extract = rt.Extract
	a.Index = rt.Index
	a.Tag = rt.Tag
	a.Search = rt.Search
	a.Timeline = rt.Timeline
	a.QA = rt.QA
	a.Stats = rt.Stats
	a.Watch = rt.Watch
}

// buildRuntime 构造某一代（generation）的全部工作区相关模块。
// 仅做构造，不做交换；交换由调用方（buildWorkspaceRuntime / RebuildWorkspace）执行。
func (a *App) buildRuntime(gen, dataDir, workspace string) (*Runtime, error) {
	cfg := a.Config

	_, _, _, dim := cfg.GetEmbedConfig()

	st, err := storage.New(dataDir, dim)
	if err != nil {
		return nil, fmt.Errorf("[assembler] 创建存储模块失败: %w", err)
	}

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
		return nil, fmt.Errorf("[assembler] 创建提取模块失败: %w", err)
	}
	// 提供工作区根，供提取路径 containment 校验（P1-08 统一最终路径）
	if workspace != "" {
		extractMod.SetWorkspaceRoot(workspace)
	}

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
	idx := index.New(st, extractMod, a.LLM, a.Events, workspace, cs, co, dim)

	// 标签模块
	tg := tag.New(st, a.LLM, a.Events)

	// 搜索模块（检索编排置于 search：向量检索 + 标签过滤 + 结果组装）
	sr := search.New(a.LLM, st)

	// 时间线模块
	tl := timeline.New(a.Git, st, a.LLM, a.Events, workspace)

	// 问答模块
	maxCtxVal, _ := cfg.Get("qa.maxContextChars")
	mcc := asInt(maxCtxVal)
	if mcc == 0 {
		mcc = 30000
	}
	qm := qa.New(st, a.LLM, a.Events, mcc)

	// 统计模块
	sm := stats.New(a.Git, st, cfg)

	// 监视模块（需工作目录）
	var wm *watch.Module
	if workspace != "" {
		_, debounceSec := cfg.GetAutoCommitConfig()
		w, err := watch.New(workspace, debounceSec)
		if err != nil {
			return nil, fmt.Errorf("[assembler] 创建监视模块失败: %w", err)
		}
		wm = w
	}

	return &Runtime{
		Generation: gen,
		Workspace:  workspace,
		DataDir:    dataDir,
		Storage:    st,
		Extract:    extractMod,
		Index:      idx,
		Tag:        tg,
		Search:     sr,
		Timeline:   tl,
		QA:         qm,
		Stats:      sm,
		Watch:      wm,
	}, nil
}

// RebuildWorkspace 工作区初始化后重建工作区运行时（修复 B-01）。
// 流程（P0-02/P0-03）：冻结队列排空旧代在途任务 → 构建新代 Runtime →
// 一次性原子交换（含传输层引用）→ 关闭旧代 storage → 恢复队列并触发全量重建。
// 注意：本方法不应在 app.Run() 之前调用（此时 watch/consume 尚未启动）。
func (a *App) RebuildWorkspace(workspace string) error {
	gen := a.runtime.beginBuild()

	// 1. 停止接收新任务，并排水旧代在途任务（队列随后保持暂停，杜绝跨代写入）
	a.freezeQueue()

	// 2. 停止旧监视器（其消费 goroutine 随 Changes 通道关闭退出）
	if old := a.runtime.Current(); old != nil && old.Watch != nil {
		_ = old.Watch.Stop()
	}

	// 3. 更新工作区路径并保存配置
	a.wsPath = workspace
	a.Config.Set("workspace.path", workspace)

	// 4. 构建新代 Runtime（旧 storage 此时与新的并存，但已无任务访问旧代）
	dataDir := filepath.Join(workspace, ".memora")
	rt, err := a.buildRuntime(gen, dataDir, workspace)
	if err != nil {
		// 构建失败：保持原状态可用——恢复队列，并重启旧监视器
		a.TaskQueue.Resume()
		if old := a.runtime.Current(); old != nil && old.Watch != nil {
			_ = old.Watch.Start()
			go a.consumeWatchChanges()
		}
		return err
	}

	// 5. 原子交换：manager 指针 + 模块引用一次性更新，返回旧代
	old := a.runtime.commit(rt)
	a.applyRuntimeModules(rt)

	// 6. 关闭旧代 storage（此刻队列已排水、HTTP 已切到新代）
	old.Close()

	// 7. 启动新监视器
	if rt.Watch != nil {
		if err := rt.Watch.Start(); err != nil {
			logx.Warn("app", "文件监视启动警告", "err", err.Error())
		}
		go a.consumeWatchChanges()
	}

	// 8. 确保 Git 仓库并恢复待处理任务
	if err := a.Git.EnsureRepo(workspace); err != nil {
		logx.Warn("app", "Git 初始化警告", "err", err.Error())
	}
	if err := rt.Storage.RecoverPending(); err != nil {
		logx.Warn("app", "恢复待处理任务警告", "err", err.Error())
	}
	if _, err := rt.Storage.VectorsLoadAll(); err != nil {
		logx.Warn("app", "加载向量索引警告", "err", err.Error())
	}

	// 9. 恢复队列，触发全量重建（经队列合并执行）
	a.TaskQueue.Resume()
	a.TriggerReindex()

	return nil
}

// freezeQueue 冻结任务队列：挂起执行、清空未处理任务、等待在途任务结束。
// 返回后队列保持暂停，调用方须在资源就绪后 Resume()。
func (a *App) freezeQueue() {
	a.TaskQueue.Pause()
	a.TaskQueue.CancelAll()
	// CancelAll 会解除暂停，需重新挂起，保证交换期间没有任何任务开始执行
	a.TaskQueue.Pause()
	if !a.TaskQueue.WaitActive(5 * time.Second) {
		logx.Warn("app", "任务排水超时，仍有任务在执行")
	}
}

// TriggerReindex 触发一次全量重建（经任务队列执行，同 generation 合并，P0-03）。
func (a *App) TriggerReindex() error {
	if a.TaskQueue == nil {
		return nil
	}
	return a.TaskQueue.TriggerReindex(a.runtime.Generation())
}

func (a *App) createTaskHandler() taskqueue.TaskHandler {
	return func(task *taskqueue.Task) error {
		// 双重复核：auto_commit 任务入口再次检查开关，防止通过其他入口绕过，
		// 确保 autoCommit.enabled=false 时即使任务已入队也不执行提交（审计 P1-03）。
		if task.Type == "auto_commit" {
			enabled, _ := a.Config.GetAutoCommitConfig()
			if !enabled {
				return nil // 开关关闭：不执行提交、不广播 commit_done
			}
		}
		switch task.Type {
		case "extract":
			if payload, ok := task.Payload.(map[string]interface{}); ok {
				if relPath, ok := payload["relPath"].(string); ok {
					return a.Index.Incremental([]string{relPath}, nil)
				}
			}
			logx.Warn("app", "提取任务无法解析", "payload", fmt.Sprintf("%v", task.Payload))
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

// RunNative 启动应用（Wails 模式）：启动文件监视、任务队列、后台轮询等。
// HTTP 服务器与浏览器打开由 Wails 框架负责，本方法不再启动 net/http 服务器。
func (a *App) RunNative() error {
	logx.Info("app", "Memora 启动中")

	// 0. 初始化事件桥接：将内部 events.Module 广播转发到 Wails 全局事件
	// （前端通过 Events.On("memora:<topic>") 订阅）
	_ = NewWailsEventBridge(a)

	// 1. 初始化 Git 仓库
	if a.wsPath != "" {
		if err := a.Git.EnsureRepo(a.wsPath); err != nil {
			logx.Warn("app", "Git 初始化警告", "err", err.Error())
		}

		// 2. 启动文件监视
		if a.Watch != nil {
			if err := a.Watch.Start(); err != nil {
				logx.Warn("app", "文件监视启动警告", "err", err.Error())
			}
		}

		// 3. 恢复待处理任务并重新入队
		if err := a.Storage.RecoverPending(); err != nil {
			logx.Warn("app", "恢复待处理任务警告", "err", err.Error())
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
				logx.Info("app", "已重新入队待处理文件", "count", len(pendingFiles))
			}
		}

		// 4. 加载内存向量索引
		if _, err := a.Storage.VectorsLoadAll(); err != nil {
			logx.Warn("app", "加载向量索引警告", "err", err.Error())
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

	// 8. 低频 reconcile 兜底循环
	go a.reconcileLoop()

	logx.Info("app", "Memora 已就绪")
	return nil
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

// ──────────────────── 低频 reconciliation ────────────────────

// defaultReconcileInterval 默认 reconcile 基础间隔（60s）。
// 原 8 秒全盘扫描改为低频兜底（P2-16）：实时变更由 watcher 驱动，本循环只做低频 reconcile。
const defaultReconcileInterval = 60 * time.Second

// defaultReconcileMax 空闲退避上限（300s）：无变更时指数退避到此封顶。
const defaultReconcileMax = 300 * time.Second

// reconcileSettings reconcile 循环参数（base 默认 60s；测试可覆盖为毫秒级）。
type reconcileSettings struct {
	base   time.Duration // 基础间隔
	max    time.Duration // 退避上限
	factor float64       // 退避倍率（默认 2）
}

// withDefaults 补齐未设置的参数为默认值。
func (s reconcileSettings) withDefaults() reconcileSettings {
	if s.base <= 0 {
		s.base = defaultReconcileInterval
	}
	if s.max <= 0 {
		s.max = defaultReconcileMax
	}
	if s.factor <= 1 {
		s.factor = 2
	}
	return s
}

// fileMeta 磁盘文件快照元数据
type fileMeta struct {
	size  int64 // 字节
	mtime int64 // 毫秒
}

// reconcileDiff 一次 reconcile 的磁盘/DB 差异
type reconcileDiff struct {
	added   []string // 磁盘有而 DB 无（或 DB 已标记 ignored 后重新出现）
	changed []string // DB 有且 size/mtime 变化
	missing []string // DB 有（非 ignored）而磁盘无 → 标记删除
}

// reconcileBaseInterval 返回本次循环的基础间隔：
// 优先取 config.index.scanIntervalSec（运行时修改可立即生效），否则用默认 60s。
func (a *App) reconcileBaseInterval() time.Duration {
	if a.Config != nil {
		if v, err := a.Config.Get("index.scanIntervalSec"); err == nil {
			if n := asInt(v); n > 0 {
				return time.Duration(n) * time.Second
			}
		}
	}
	return a.reconcile.withDefaults().base
}

// reconcileLoop 低频 reconciliation 兜底循环（替换原 8 秒全盘扫描 pollPendingFiles）。
// watcher（Changes 通道）在 Run 中已被 consumeWatchChanges 消费，负责实时增量；
// 本循环仅作低频兜底，覆盖 watcher 遗漏的场景（启动前已存在/重启间隙变更/防抖遗漏等）。
// 每轮一次 reconcileOnce（一次磁盘遍历 + 一次批量 DB 加载 + 内存比较，无逐文件 N+1），
// 无变更时指数退避至 max（60→120→240→300s），有变更复位为基础间隔。
func (a *App) reconcileLoop() {
	reconcileLoopFunc(a.ctx, a.reconcileBaseInterval, a.reconcile, a.reconcileOnce)
}

// reconcileLoopFunc reconcile 主循环逻辑（独立函数便于测试）。
// base 返回基础间隔（循环中可随 config 变化）；run 执行一次 reconcile 并返回是否发现变更。
func reconcileLoopFunc(ctx context.Context, base func() time.Duration, settings reconcileSettings, run func() bool) {
	next := base()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}

		b := base()
		s := settings.withDefaults()
		s.base = b
		if run() {
			next = b // 有变更：复位为基础间隔
		} else {
			next = backoffInterval(next, s) // 无变更：指数退避
		}
	}
}

// backoffInterval 计算下一次 reconcile 间隔：
// 无变更时将 current 指数放大（×factor），封顶于 max；且不低于 base。
func backoffInterval(current time.Duration, s reconcileSettings) time.Duration {
	s = s.withDefaults()
	next := time.Duration(float64(current) * s.factor)
	if next > s.max {
		next = s.max
	}
	if next < s.base {
		next = s.base
	}
	return next
}

// reconcileOnce 执行一次 reconcile 并返回是否发现磁盘差异。
//  1. 遍历磁盘收集受支持文件 → relPath → (size, mtime)；
//  2. 批量加载 DB 全部文件 → relPath → FileInfo（分页遍历现有 FilesList，无新增 storage 方法）；
//  3. 批量比较差异：磁盘有而 DB 无/曾删除 → extract；size/mtime 变化 → extract；
//     DB 有而磁盘无 → delete_index（标记缺失）；
//  4. 保持原有 pending 处理：DB 中 pending 状态（未完成/中断/失败）文件重新入队 extract。
//
// 注：不再逐文件 FilesFindByRelPath（消除 N+1）。
func (a *App) reconcileOnce() bool {
	if a.wsPath == "" || a.Storage == nil || a.TaskQueue == nil {
		return false
	}

	disk, err := scanDiskSnapshot(a.wsPath)
	if err != nil {
		logx.Warn("app", "reconcile 扫描磁盘失败", "err", err.Error())
		return false
	}
	dbFiles, err := loadDBFileMap(a.Storage)
	if err != nil {
		logx.Warn("app", "reconcile 加载数据库文件失败", "err", err.Error())
		return false
	}

	diff := computeReconcileDiff(disk, dbFiles)
	changed := false
	for _, relPath := range append(diff.added, diff.changed...) {
		a.TaskQueue.Submit(&taskqueue.Task{
			Type:    "extract",
			Payload: map[string]interface{}{"relPath": relPath},
		})
		changed = true
	}
	for _, relPath := range diff.missing {
		a.TaskQueue.Submit(&taskqueue.Task{Type: "delete_index", Payload: relPath})
		changed = true
	}

	// 保持原有 pending 处理：DB 中 pending 状态文件重新入队 extract
	if files, _, err := a.Storage.FilesList("pending", "", 0, 100, ""); err == nil {
		for _, f := range files {
			a.TaskQueue.Submit(&taskqueue.Task{
				Type:    "extract",
				Payload: map[string]interface{}{"relPath": f.RelPath, "fileId": float64(f.ID)},
			})
		}
	}

	return changed
}

// scanDiskSnapshot 遍历工作区，收集受支持文档的 (size, mtime) 快照。
// 遍历规则与旧 pollPendingFiles 一致：跳过隐藏目录与重型/非文档目录（node_modules 等）。
func scanDiskSnapshot(workspace string) (map[string]fileMeta, error) {
	snap := make(map[string]fileMeta)
	err := filepath.Walk(workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") || isHeavyDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		relPath, rerr := filepath.Rel(workspace, path)
		if rerr != nil || relPath == "" {
			return nil
		}
		switch strings.ToLower(filepath.Ext(relPath)) {
		case ".pdf", ".docx", ".pptx", ".xlsx", ".txt", ".md":
		default:
			return nil
		}
		snap[relPath] = fileMeta{size: info.Size(), mtime: info.ModTime().UnixMilli()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// loadDBFileMap 批量加载数据库全部文件 → rel_path 索引。
// 分页遍历 FilesList（现有 API，不新增 storage 方法），排序固定 name:asc（rel_path 唯一，分页稳定）。
func loadDBFileMap(st *storage.Module) (map[string]*contract.FileInfo, error) {
	result := make(map[string]*contract.FileInfo)
	const pageSize = 1000
	page := 0
	for {
		files, total, err := st.FilesList("", "", page, pageSize, "name:asc")
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			result[f.RelPath] = f
		}
		page++
		if page*pageSize >= total {
			break
		}
	}
	return result, nil
}

// computeReconcileDiff 比较磁盘快照与 DB 快照，返回差异。
// - added: 磁盘有而 DB 无，或 DB 已标记 ignored（曾被删除，现又出现）→ 入队 extract；
// - changed: DB 有且 size/mtime 变化 → 入队 extract；
// - missing: DB 有（非 ignored）而磁盘无 → 入队 delete_index（标记缺失）。
func computeReconcileDiff(disk map[string]fileMeta, db map[string]*contract.FileInfo) reconcileDiff {
	var diff reconcileDiff
	for relPath, meta := range disk {
		existing, ok := db[relPath]
		switch {
		case !ok, existing.IndexStatus == "ignored":
			diff.added = append(diff.added, relPath)
		case existing.Size != meta.size || existing.Mtime != meta.mtime:
			diff.changed = append(diff.changed, relPath)
		}
	}
	for relPath, f := range db {
		if _, ok := disk[relPath]; ok {
			continue
		}
		if f.IndexStatus == "ignored" {
			continue // 已标记删除，无需重复处理
		}
		diff.missing = append(diff.missing, relPath)
	}
	return diff
}

// Shutdown 优雅关闭
// 顺序（P0-04）：取消后台轮询 → 停监视 →
// 冻结并排水任务队列 → 关闭当前运行的 storage。
func (a *App) Shutdown() {
	logx.Info("app", "正在关闭")

	// 1. 取消根 context，停掉 reconcileLoop 等后台轮询
	if a.cancel != nil {
		a.cancel()
	}

	// 2. 停止监视器（其消费 goroutine 随 Changes 通道关闭退出）
	if a.Watch != nil {
		_ = a.Watch.Stop()
	}

	// 3. 冻结并排水任务队列：之后队列保持暂停，不会有任务再启动
	a.freezeQueue()
	a.TaskQueue.WaitReindex(2 * time.Second)

	// 4. 关闭当前运行的 storage（此刻已无活动请求/任务访问它）
	if a.runtime != nil {
		if rt := a.runtime.Current(); rt != nil {
			rt.Close()
		}
	}

	logx.Info("app", "已关闭")
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
		// 触发自动提交：仅当 autoCommit.enabled 为 true 时才入队，否则只索引、不自动提交（审计 P1-03）
		if len(change.Modified) > 0 || len(change.Removed) > 0 {
			allFiles := append(change.Modified, change.Removed...)
			if enabled, _ := a.Config.GetAutoCommitConfig(); enabled {
				a.TaskQueue.Submit(&taskqueue.Task{
					Type:    "auto_commit",
					Payload: map[string]interface{}{"files": allFiles},
				})
			}
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
