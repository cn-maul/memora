// Package assembler 装配根：按顺序 new 并接线
package assembler

import (
	"fmt"
	"sync"

	"memora/internal/extract"
	"memora/internal/index"
	"memora/internal/qa"
	"memora/internal/search"
	"memora/internal/stats"
	"memora/internal/storage"
	"memora/internal/tag"
	"memora/internal/timeline"
	"memora/internal/watch"
)

// Runtime 某一代（generation）的工作区模块快照。
// 同一代内的模块引用集合在交换前保持不变；切换工作区时构建新代，
// 待旧代在途任务排空后再一次性原子交换，禁止逐字段原地替换（P0-02）。
type Runtime struct {
	Generation string
	Workspace  string
	DataDir    string

	Storage  *storage.Module
	Extract  *extract.Module
	Index    *index.Module
	Tag      *tag.Module
	Search   *search.Module
	Timeline *timeline.Module
	QA       *qa.Module
	Stats    *stats.Module
	Watch    *watch.Module
}

// Close 回收运行时持有的资源（关闭 storage 数据库句柄）。
func (rt *Runtime) Close() {
	if rt != nil && rt.Storage != nil {
		_ = rt.Storage.Close()
	}
}

// RuntimeManager 维护当前工作区 Runtime 并推进 generation。
// 读取侧通过 Current()/Generation() 获取当前快照；切换侧用 commit() 原子交换。
type RuntimeManager struct {
	mu      sync.RWMutex
	current *Runtime
	seq     uint64
}

func newRuntimeManager() *RuntimeManager {
	return &RuntimeManager{}
}

// Current 返回当前 Runtime（尚未初始化工作区时为 nil）。
func (m *RuntimeManager) Current() *Runtime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Generation 返回当前代标识（无工作区时为空串）。
func (m *RuntimeManager) Generation() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		return ""
	}
	return m.current.Generation
}

// beginBuild 预分配下一代标识，须在构建新 Runtime 之前调用。
func (m *RuntimeManager) beginBuild() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	return fmt.Sprintf("w%d", m.seq)
}

// commit 原子交换当前 Runtime 并返回旧代（调用方负责排空后关闭旧代）。
func (m *RuntimeManager) commit(next *Runtime) *Runtime {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.current
	m.current = next
	return old
}
