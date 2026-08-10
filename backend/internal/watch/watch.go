// Package watch 文件监视模块
// fsnotify 递归监视 + 防抖汇总
package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"memora/internal/contract"
	"memora/internal/logx"

	"github.com/fsnotify/fsnotify"
)

// Module 文件监视模块
type Module struct {
	watcher   *fsnotify.Watcher
	workspace string
	changeCh  chan *contract.FileChange
	done      chan struct{}
	paused    bool
	mu        sync.Mutex

	// 防抖
	dirtyFiles map[string]bool // 记录防抖窗口内变动的文件
	dirtyTimer *time.Timer
	debounce   time.Duration

	running bool
}

// New 创建文件监视模块（watcher 延迟到 Start 创建，避免与 Start 重建重复/泄漏）
func New(workspace string, debounceSec int) (*Module, error) {
	return &Module{
		workspace:  workspace,
		changeCh:   make(chan *contract.FileChange, 32),
		done:       make(chan struct{}),
		dirtyFiles: make(map[string]bool),
		debounce:   time.Duration(debounceSec) * time.Second,
	}, nil
}

// addRecursiveWatch 递归添加目录监视
// watcher 作为参数：eventLoop 的 Create 分支用参数实例调用，避免无锁读 m.watcher（对齐参数化）
func (m *Module) addRecursiveWatch(watcher *fsnotify.Watcher, dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过不可读目录
		}
		if !info.IsDir() {
			return nil
		}

		// 忽略规则
		base := info.Name()
		if base == ".git" || base == ".memora" || strings.HasPrefix(base, ".") {
			return filepath.SkipDir
		}

		return watcher.Add(path)
	})
}

// Start 启动文件监视
func (m *Module) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return nil
	}

	// 支持 Stop 后重启：重建 watcher/changeCh/done（Stop 已 Close watcher 与 changeCh）
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("[watch] 创建监视器失败: %w", err)
	}
	m.watcher = watcher
	m.changeCh = make(chan *contract.FileChange, 32)
	m.done = make(chan struct{})

	if err := m.addRecursiveWatch(m.watcher, m.workspace); err != nil {
		// 失败时关闭新建的 watcher，避免 fd 泄漏（review warn）
		m.watcher.Close()
		m.watcher = nil
		return fmt.Errorf("[watch] 递归监视失败: %w", err)
	}

	m.running = true

	// goroutine 捕获当前 watcher 引用：Stop→Start 重建 watcher 后，旧 goroutine 仍读旧实例，
	// 避免无锁读 m.watcher 与 Start 持锁写的理论 race（review should-fix）
	go m.eventLoop(m.watcher)
	go m.debounceLoop()

	logx.Info("watch", "开始监视", "workspace", m.workspace)
	return nil
}

// isIgnored 检查文件是否应被忽略
func isIgnored(path string) bool {
	base := filepath.Base(path)
	ext := filepath.Ext(path)

	// 隐藏文件
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "~") {
		return true
	}

	// 临时文件后缀
	switch strings.ToLower(ext) {
	case ".tmp", ".~", ".lock":
		return true
	}

	// 忽略目录
	dir := filepath.Dir(path)
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		if part == ".git" || part == ".memora" {
			return true
		}
	}

	return false
}

// isSupportedDoc 判断相对路径是否为可索引文档类型（与 index.detectDocType 保持一致）。
// 目录、未知扩展名以及被标记为 ignored 的类型（如 .doc）均返回 false（修复 H-04）。
func isSupportedDoc(relPath string) bool {
	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
	case ".pdf", ".docx", ".pptx", ".xlsx", ".txt", ".md":
		return true
	default:
		return false
	}
}

// eventLoop 处理 fsnotify 事件
// watcher 作为参数传入：goroutine 持有旧实例引用，Stop→Start 重建后无 race
func (m *Module) eventLoop(watcher *fsnotify.Watcher) {
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			if isIgnored(event.Name) {
				continue
			}

			m.mu.Lock()
			if m.paused {
				m.mu.Unlock()
				continue
			}

			// 记录脏文件
			m.dirtyFiles[event.Name] = true

			// 重置防抖定时器：替换为新 timer（而非 Reset 复用）。
			// 修复竞态：flushChanges 通过 "m.dirtyTimer != timer" 判断本次触发是否仍有效；
			// 若复用同一 timer 指针则无法区分，防抖窗口内的新事件会被 flush 误清。
			if m.dirtyTimer != nil {
				m.dirtyTimer.Stop()
			}
			m.dirtyTimer = time.NewTimer(m.debounce)
			m.mu.Unlock()

			// 如果是新目录，递归添加监视（用参数 watcher，与 eventLoop 参数化一致）
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					m.addRecursiveWatch(watcher, event.Name)
				}
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logx.Warn("watch", "监视错误", "err", err.Error())

		case <-m.done:
			return
		}
	}
}

// debounceLoop 防抖到期后发送变更
func (m *Module) debounceLoop() {
	for {
		m.mu.Lock()
		timer := m.dirtyTimer
		m.mu.Unlock()

		if timer == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		select {
		case <-timer.C:
			m.flushChanges(timer)
		case <-m.done:
			return
		default:
			// 循环重新取当前 timer：eventLoop 可能已 Stop 旧 timer 并替换为新 timer，
			// 若 select 固定在旧 timer 上会永久阻塞（旧 timer 的 C 永不触发）。
			// 通过短轮询 + default 分支，每次循环都拿到最新 timer。
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// flushChanges 防抖到期，汇总变更并发送
func (m *Module) flushChanges(timer *time.Timer) {
	m.mu.Lock()
	// 触发我的 timer 已不是当前有效 timer（期间新事件替换了新 timer 重新计时），
	// 说明防抖窗口被延长，本次不消费，等新 timer 到期（修复竞态丢事件）
	if m.dirtyTimer != timer {
		// 非阻塞消费已触发的 C：否则 C 永久就绪，debounceLoop 的 select 每次必然选中它导致忙循环
		select {
		case <-timer.C:
		default:
		}
		m.mu.Unlock()
		return
	}
	if len(m.dirtyFiles) == 0 {
		m.mu.Unlock()
		return
	}

	files := make(map[string]bool)
	for f := range m.dirtyFiles {
		relPath, err := filepath.Rel(m.workspace, f)
		if err != nil {
			continue
		}
		files[relPath] = true
	}
	m.dirtyFiles = make(map[string]bool)
	m.dirtyTimer = nil
	m.mu.Unlock()

	// 分类变更
	change := &contract.FileChange{}
	for relPath := range files {
		fullPath := filepath.Join(m.workspace, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			change.Removed = append(change.Removed, relPath)
			continue
		}
		// 跳过目录和不支持的文件类型，避免送入索引（修复 H-04）
		if info, err := os.Stat(fullPath); err == nil {
			if info.IsDir() {
				continue
			}
		}
		if !isSupportedDoc(relPath) {
			continue
		}
		// 简化处理：统一作为 modified，由下游 content_hash 幂等处理
		change.Modified = append(change.Modified, relPath)
	}

	if len(change.Added) == 0 && len(change.Modified) == 0 && len(change.Removed) == 0 {
		return
	}

	// 非阻塞发送（锁内检查 running：Stop 持锁 close(changeCh)，互斥保证发送与关闭不同时发生，
	// 避免 send on closed channel panic——修复 review 发现的竞态）
	m.mu.Lock()
	if m.running {
		select {
		case m.changeCh <- change:
		default:
			logx.Warn("watch", "变更通道已满，丢弃事件", "count", len(files))
		}
	}
	m.mu.Unlock()
}

// Changes 返回变更通道
func (m *Module) Changes() <-chan *contract.FileChange {
	return m.changeCh
}

// Stop 停止监视
func (m *Module) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return nil
	}

	close(m.done)
	m.watcher.Close()
	m.running = false

	if m.dirtyTimer != nil {
		m.dirtyTimer.Stop()
	}

	// 关闭变更通道：消费方（consumeWatchChanges 的 range）随之退出，
	// 避免 RebuildWorkspace 每次重建泄漏一个 goroutine（修复审计低危）
	// done 已关闭，eventLoop/debounceLoop/flushChanges 不再向 changeCh 发送，无发送 panic 风险
	close(m.changeCh)

	logx.Info("watch", "监视已停止")
	return nil
}

// Pause 暂停监视
func (m *Module) Pause() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paused = true
	logx.Info("watch", "监视已暂停")
	return nil
}

// Resume 恢复监视
func (m *Module) Resume() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paused = false
	logx.Info("watch", "监视已恢复")
	return nil
}
