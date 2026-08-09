// Package taskqueue 单队列单顺序处理器
// 去重/暂停/恢复/断点恢复
package taskqueue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Task 任务
type Task struct {
	Type    string // extract|tag|summarize|reindex|delete_index
	Payload interface{}
}

// TaskHandler 任务处理函数
type TaskHandler func(task *Task) error

// Module 任务队列
type Module struct {
	mu      sync.Mutex
	queue   []*Task
	pending map[string]bool // rel_path → 是否在队列或处理中（去重）
	paused  bool
	cond    *sync.Cond // 暂停/恢复唤醒（修复暂停竞态：Resume 不再依赖非阻塞通道发送）
	running int32      // 1=正在运行
	stopCh  chan struct{}

	handler TaskHandler
	events  EventBus

	// 连续失败计数（按任务类型分组，避免一个顽疾任务阻塞整个队列）
	consecutiveFails map[string]int
	// 单类型连续失败达阈值后临时封禁该类型（跳过后续该类型任务），成功一次即解封
	failedTypes map[string]bool
	// 封禁时间戳（该类型最近一次被封禁的时刻），用于定时自动解封
	failedSince map[string]time.Time
}

// typeBanCooldown 类型被封禁后的冷却时间：冷却期结束后自动解封并允许该类型任务重试，
// 避免"封禁后任务被跳过 → 永远没有成功机会 → 永久封禁"的死锁（修复 B-03 遗留缺陷）。
const typeBanCooldown = 60 * time.Second

// EventBus 事件接口
type EventBus interface {
	Notify(topic string, data interface{})
}

// New 创建任务队列
func New(handler TaskHandler, events EventBus) *Module {
	m := &Module{
		pending:          make(map[string]bool),
		stopCh:           make(chan struct{}),
		handler:          handler,
		events:           events,
		consecutiveFails: make(map[string]int),
		failedTypes:      make(map[string]bool),
		failedSince:      make(map[string]time.Time),
	}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// Submit 提交任务（同 rel_path 去重）
func (m *Module) Submit(task *Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 提取去重键
	key := taskKey(task)
	if key != "" {
		if m.pending[key] {
			// fmt.Printf("[taskqueue] 丢弃重复任务: %s/%s\n", task.Type, key)
			return nil
		}
		m.pending[key] = true
	}

	m.queue = append(m.queue, task)

	// 如果未在运行，启动处理器
	if atomic.CompareAndSwapInt32(&m.running, 0, 1) {
		go m.processLoop()
	}

	return nil
}

// taskKey 提取任务的去重键
func taskKey(task *Task) string {
	if task.Type == "delete_index" {
		return ""
	}
	if m, ok := task.Payload.(map[string]interface{}); ok {
		if p, ok := m["relPath"]; ok {
			if s, ok := p.(string); ok {
				return task.Type + ":" + s
			}
		}
	}
	return ""
}

// processLoop 单顺序处理器循环
func (m *Module) processLoop() {
	for {
		m.mu.Lock()
		// 暂停等待：用 Cond 等待，Resume 时 Broadcast 唤醒（修复暂停/恢复竞态：
		// 原实现 Resume 的非阻塞通道发送可能因 processLoop 尚未进入接收而丢失，导致队列永久停摆）
		for m.paused {
			m.cond.Wait()
		}

		if len(m.queue) == 0 {
			// 持锁时将 running 置 0 再解锁退出，避免与 Submit 的 CAS 竞态：
			// 若先解锁再 return（defer 才置 0），窗口期内 Submit 会 CAS 失败，
			// 新任务滞留队列却无人启动处理器（修复 review 发现的竞态）。
			// 注意不能用 defer 置 0：退出后立刻启动的新 loop 可能已被 Submit 拉起，
			// defer 会错误覆盖新 loop 的 running=1。
			atomic.StoreInt32(&m.running, 0)
			m.mu.Unlock()
			return
		}

		task := m.queue[0]
		// 封禁类型：若已过冷却期则自动解封并执行；否则跳过该任务（不执行），
		// 继续处理其他类型（修复 should-fix：单类型失败不阻塞队列）。
		if m.failedTypes[task.Type] {
			if since, ok := m.failedSince[task.Type]; ok && time.Since(since) >= typeBanCooldown {
				// 冷却期结束：自动解封，允许该类型任务重试（修复永久封禁死锁）
				delete(m.failedTypes, task.Type)
				delete(m.failedSince, task.Type)
				m.consecutiveFails[task.Type] = 0
			} else {
				m.queue = m.queue[1:]
				if key := taskKey(task); key != "" {
					delete(m.pending, key)
				}
				m.mu.Unlock()
				continue
			}
		}

		m.queue = m.queue[1:]
		key := taskKey(task)
		m.mu.Unlock()

		// 通知队列状态
		m.notifyStatus()

		// 执行：handler 可能 panic，用 recover 防止 processLoop goroutine 被杀
		// 导致 running 卡在 1、队列永久停摆、Wait 永远超时（修复 review 发现）
		err := m.safeHandle(task)
		if err != nil {
			fmt.Printf("[taskqueue] 任务失败 [%s]: %v\n", task.Type, err)
			m.mu.Lock()
			m.consecutiveFails[task.Type]++
			if m.consecutiveFails[task.Type] >= 5 {
				// 达到阈值前先清理当前任务的去重键，保证恢复后能重新提交同任务（修复 B-03）
				if key != "" {
					delete(m.pending, key)
				}
				fmt.Printf("[taskqueue] 类型 %s 连续 %d 次失败，临时封禁该类型 %v\n", task.Type, m.consecutiveFails[task.Type], typeBanCooldown)
				// 临时封禁该类型（跳过后续该类型任务），不再暂停整个队列（修复 should-fix）
				m.failedTypes[task.Type] = true
				m.failedSince[task.Type] = time.Now()
				m.consecutiveFails[task.Type] = 0
				m.mu.Unlock()
				// 通知 UI
				if m.events != nil {
					m.events.Notify("task_queue", map[string]interface{}{
						"running":    0,
						"pending":    0,
						"paused":     false,
						"error":      err.Error(),
						"failedType": task.Type,
					})
				}
				continue
			}
			m.mu.Unlock()
		} else {
			m.mu.Lock()
			// 成功：解封该类型（如有封禁）
			m.consecutiveFails[task.Type] = 0
			delete(m.failedTypes, task.Type)
			m.mu.Unlock()
		}

		// 清理去重记录
		if key != "" {
			m.mu.Lock()
			delete(m.pending, key)
			m.mu.Unlock()
		}

		// 通知状态
		m.notifyStatus()
	}
}

// notifyStatus 发送队列状态事件
func (m *Module) notifyStatus() {
	if m.events == nil {
		return
	}
	m.mu.Lock()
	pending := len(m.queue)
	running := 0
	if atomic.LoadInt32(&m.running) == 1 {
		running = 1
	}
	paused := m.paused
	m.mu.Unlock()

	m.events.Notify("task_queue", map[string]interface{}{
		"running": running,
		"pending": pending,
		"paused":  paused,
	})
}

// safeHandle 执行任务 handler 并捕获 panic（修复 review 发现：
// handler panic 会杀死 processLoop goroutine，导致 running 卡在 1、队列永久停摆）。
func (m *Module) safeHandle(task *Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
			fmt.Printf("[taskqueue] 任务 %s panic: %v\n", task.Type, r)
		}
	}()
	return m.handler(task)
}

// Pause 全局暂停
func (m *Module) Pause() error {
	m.mu.Lock()
	m.paused = true
	m.mu.Unlock()
	fmt.Println("[taskqueue] 已暂停")
	return nil
}

// Resume 全局恢复
func (m *Module) Resume() error {
	m.mu.Lock()
	m.paused = false
	m.cond.Broadcast() // 唤醒暂停中的处理器（Cond 保证唤醒不丢失，修复暂停/恢复竞态）
	m.mu.Unlock()

	// 如果处理器未在运行，启动
	if atomic.CompareAndSwapInt32(&m.running, 0, 1) {
		go m.processLoop()
	}

	fmt.Println("[taskqueue] 已恢复")
	return nil
}

// Status 返回队列状态（running, pending, paused）
func (m *Module) Status() (running int, pending int, paused bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if atomic.LoadInt32(&m.running) == 1 {
		running = 1
	}
	return running, len(m.queue), m.paused
}

// CancelAll 清空未处理队列
func (m *Module) CancelAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.queue = nil
	m.pending = make(map[string]bool)
	m.consecutiveFails = make(map[string]int)
	m.failedTypes = make(map[string]bool)
	m.failedSince = make(map[string]time.Time)
	// 若处于暂停状态则复位，否则 processLoop 可能在暂停中永久等待，
	// 关闭时 Wait 排水必然超时（修复 review 发现：暂停中 Shutdown 每次打超时警告）
	m.paused = false
	m.cond.Broadcast()

	fmt.Println("[taskqueue] 已清空所有待处理任务")
	return nil
}

// Wait 等待当前正在执行的任务完成（关闭前排水，修复 M-08）。
// 单顺序处理器在任务完成后将 running 置 0；在超时内返回 true，否则返回 false。
func (m *Module) Wait(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for atomic.LoadInt32(&m.running) == 1 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}
