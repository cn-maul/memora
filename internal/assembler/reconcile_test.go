package assembler

// 低频 reconciliation（P2-16）测试：
// 用临时工作区 + 真实 SQLite（storage.New）+ 记录型任务队列，验证：
//   - 新建文件被 reconcile 发现并入队 extract
//   - mtime/size 变化被识别并入队 extract
//   - DB 有而磁盘无的文件被标记（入队 delete_index），ignored 文件不重复处理
//   - pending 文件重新入队（保持原有行为）
//   - 空闲时退避间隔增长、有变更时复位（间隔参数化到毫秒级便于测试）
// 不依赖 LLM / Python / 网络。

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"memora/internal/contract"
	"memora/internal/events"
	"memora/internal/storage"
	"memora/internal/taskqueue"
)

// taskRecorder 记录任务队列实际执行过的任务（测试用假 handler）
type taskRecorder struct {
	mu    sync.Mutex
	tasks []*taskqueue.Task
}

func (r *taskRecorder) add(t *taskqueue.Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, t)
}

func (r *taskRecorder) all() []*taskqueue.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*taskqueue.Task, len(r.tasks))
	copy(out, r.tasks)
	return out
}

// waitForTasks 轮询等待任务条件满足（任务由队列 goroutine 异步执行）。
func waitForTasks(t *testing.T, r *taskRecorder, pred func([]*taskqueue.Task) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pred(r.all()) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待任务条件超时, 已记录任务: %+v", r.all())
}

// findTask 在任务列表中查找指定类型与 relPath 的任务。
// 兼容两种 Payload 形态：map（extract）与 string（delete_index）。
func findTask(tasks []*taskqueue.Task, typ, relPath string) *taskqueue.Task {
	for _, task := range tasks {
		if task.Type != typ {
			continue
		}
		switch p := task.Payload.(type) {
		case map[string]interface{}:
			if rp, ok := p["relPath"].(string); ok && rp == relPath {
				return task
			}
		case string:
			if p == relPath {
				return task
			}
		}
	}
	return nil
}

func countTasks(tasks []*taskqueue.Task, typ, relPath string) int {
	count := 0
	for _, task := range tasks {
		if task.Type != typ {
			continue
		}
		switch p := task.Payload.(type) {
		case map[string]interface{}:
			if rp, ok := p["relPath"].(string); ok && rp == relPath {
				count++
			}
		case string:
			if p == relPath {
				count++
			}
		}
	}
	return count
}

// newReconcileTestApp 构建最小 App：真实临时 SQLite storage + 记录型任务队列。
func newReconcileTestApp(t *testing.T, workspace string) (*App, *taskRecorder, *storage.Module) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	dataDir := filepath.Join(workspace, ".memora")
	st, err := storage.New(dataDir, 4)
	if err != nil {
		t.Fatalf("创建临时 storage 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	rec := &taskRecorder{}
	tq := taskqueue.New(func(task *taskqueue.Task) error {
		rec.add(task)
		return nil
	}, events.New())

	app := &App{
		Storage:   st,
		TaskQueue: tq,
		wsPath:    workspace,
		ctx:       ctx,
		cancel:    cancel,
	}
	return app, rec, st
}

// ──────────────────── 退避逻辑 ────────────────────

func TestBackoffInterval(t *testing.T) {
	s := reconcileSettings{base: 60 * time.Second, max: 300 * time.Second, factor: 2}
	cases := []struct {
		cur  time.Duration
		want time.Duration
	}{
		{60 * time.Second, 120 * time.Second},
		{120 * time.Second, 240 * time.Second},
		{240 * time.Second, 300 * time.Second}, // 封顶
		{300 * time.Second, 300 * time.Second}, // 持续封顶
	}
	for _, c := range cases {
		if got := backoffInterval(c.cur, s); got != c.want {
			t.Fatalf("backoffInterval(%v) = %v, want %v", c.cur, got, c.want)
		}
	}

	// 默认参数：base=60s, max=300s, factor=2
	if got := backoffInterval(60*time.Second, reconcileSettings{}); got != 120*time.Second {
		t.Fatalf("默认退避应 120s, got %v", got)
	}
	// 不低于 base（回弹）
	if got := backoffInterval(30*time.Second, reconcileSettings{base: 60 * time.Second, max: 300 * time.Second, factor: 2}); got != 60*time.Second {
		t.Fatalf("低于 base 应回弹, got %v", got)
	}
}

// TestReconcileLoopBackoffOnIdle 无变更时循环应指数退避（间隔出现接近上限的大间隔）。
func TestReconcileLoopBackoffOnIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	var runs []time.Time
	settings := reconcileSettings{base: 20 * time.Millisecond, max: 80 * time.Millisecond, factor: 2}

	done := make(chan struct{})
	go func() {
		defer close(done)
		reconcileLoopFunc(ctx, func() time.Duration { return settings.base }, settings, func() bool {
			mu.Lock()
			runs = append(runs, time.Now())
			mu.Unlock()
			return false // 始终无变更 → 应指数退避
		})
	}()

	time.Sleep(400 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(runs) < 5 {
		t.Fatalf("退避循环运行次数过少: %d", len(runs))
	}
	var maxGap time.Duration
	for i := 1; i < len(runs); i++ {
		if gap := runs[i].Sub(runs[i-1]); gap > maxGap {
			maxGap = gap
		}
	}
	if maxGap < 60*time.Millisecond {
		t.Fatalf("无变更退避后应出现接近上限的大间隔（>=60ms），实际最大间隔 %v", maxGap)
	}
}

// TestReconcileLoopResetsOnChange 有变更时循环应复位为基础间隔（快速连续执行）。
func TestReconcileLoopResetsOnChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var runs []time.Time
	var changedFlag int32
	settings := reconcileSettings{base: 20 * time.Millisecond, max: 80 * time.Millisecond, factor: 2}

	done := make(chan struct{})
	go func() {
		defer close(done)
		reconcileLoopFunc(ctx, func() time.Duration { return settings.base }, settings, func() bool {
			mu.Lock()
			runs = append(runs, time.Now())
			mu.Unlock()
			return atomic.LoadInt32(&changedFlag) == 1
		})
	}()

	// 先进入退避状态（无变更约 150ms，间隔已超过 base）
	time.Sleep(150 * time.Millisecond)
	atomic.StoreInt32(&changedFlag, 1) // 模拟发现变更
	time.Sleep(200 * time.Millisecond) // 变更后应复位为基础间隔，快速执行多轮

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(runs) < 6 {
		t.Fatalf("运行次数过少: %d", len(runs))
	}
	// 变更后的轮次间隔应接近 base（20ms）
	lastN := runs[len(runs)-4:]
	for i := 1; i < len(lastN); i++ {
		if gap := lastN[i].Sub(lastN[i-1]); gap > 40*time.Millisecond {
			t.Fatalf("变更后应复位到基础间隔（20ms），实际间隔 %v", gap)
		}
	}
}

// ──────────────────── 差异计算 ────────────────────

func TestComputeReconcileDiff(t *testing.T) {
	disk := map[string]fileMeta{
		"new.txt":      {size: 10, mtime: 100},
		"same.txt":     {size: 20, mtime: 200},
		"changed.txt":  {size: 30, mtime: 300},
		"reappear.txt": {size: 40, mtime: 400},
	}
	db := map[string]*contract.FileInfo{
		"same.txt":     {RelPath: "same.txt", Size: 20, Mtime: 200, IndexStatus: "indexed"},
		"changed.txt":  {RelPath: "changed.txt", Size: 31, Mtime: 300, IndexStatus: "indexed"},
		"reappear.txt": {RelPath: "reappear.txt", Size: 40, Mtime: 400, IndexStatus: "ignored"},
		"gone.txt":     {RelPath: "gone.txt", Size: 50, Mtime: 500, IndexStatus: "indexed"},
		"gone2.txt":    {RelPath: "gone2.txt", Size: 60, Mtime: 600, IndexStatus: "ignored"},
	}

	diff := computeReconcileDiff(disk, db)
	has := func(list []string, p string) bool {
		for _, x := range list {
			if x == p {
				return true
			}
		}
		return false
	}
	if !has(diff.added, "new.txt") {
		t.Fatal("new.txt 应为 added")
	}
	if !has(diff.added, "reappear.txt") {
		t.Fatal("ignored 状态重新出现应为 added")
	}
	if !has(diff.changed, "changed.txt") {
		t.Fatal("changed.txt 应为 changed")
	}
	if has(diff.changed, "same.txt") {
		t.Fatal("same.txt 不应为 changed")
	}
	if !has(diff.missing, "gone.txt") {
		t.Fatal("gone.txt 应为 missing")
	}
	if has(diff.missing, "gone2.txt") {
		t.Fatal("ignored 的 gone2.txt 不应为 missing")
	}
	if len(diff.added) != 2 || len(diff.changed) != 1 || len(diff.missing) != 1 {
		t.Fatalf("diff 数量不符: %+v", diff)
	}
}

func TestScanDiskSnapshot(t *testing.T) {
	workspace := t.TempDir()
	mustWrite := func(rel string) {
		t.Helper()
		p := filepath.Join(workspace, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.txt")
	mustWrite("sub/b.md")
	mustWrite("c.bin")              // 不支持扩展名
	mustWrite("d.doc")              // 不支持扩展名（ignored 类型）
	mustWrite(".hidden/e.txt")      // 隐藏目录
	mustWrite("node_modules/f.txt") // 重型目录
	mustWrite(".memora/g.txt")      // 数据目录

	snap, err := scanDiskSnapshot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) != 2 {
		t.Fatalf("应只发现 a.txt 与 sub/b.md, got %d: %v", len(snap), snap)
	}
	if _, ok := snap["a.txt"]; !ok {
		t.Fatal("缺少 a.txt")
	}
	// Windows 下 filepath.Rel 返回反斜杠分隔
	if _, ok := snap[filepath.Join("sub", "b.md")]; !ok {
		t.Fatal("缺少 sub/b.md")
	}
	if m := snap["a.txt"]; m.size != 1 || m.mtime <= 0 {
		t.Fatalf("a.txt 元数据异常: %+v", m)
	}
}

// ──────────────────── 集成（真实 SQLite + 记录队列） ────────────────────

func TestReconcileDiscoversNewFiles(t *testing.T) {
	workspace := t.TempDir()
	app, rec, _ := newReconcileTestApp(t, workspace)

	// 空工作区：无变更
	if changed := app.reconcileOnce(); changed {
		t.Fatal("空工作区不应有变更")
	}

	path := filepath.Join(workspace, "docs", "note.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	rel := filepath.Join("docs", "note.txt") // Windows 下 filepath.Rel 返回反斜杠分隔
	if !app.reconcileOnce() {
		t.Fatal("新文件应被识别为变更")
	}
	waitForTasks(t, rec, func(tasks []*taskqueue.Task) bool {
		return findTask(tasks, "extract", rel) != nil
	})
}

func TestReconcileDetectsMtimeChange(t *testing.T) {
	workspace := t.TempDir()
	app, rec, st := newReconcileTestApp(t, workspace)

	path := filepath.Join(workspace, "a.txt")
	if err := os.WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatal(err)
	}

	// 首次：发现新文件并入队
	if !app.reconcileOnce() {
		t.Fatal("新文件应触发变更")
	}
	waitForTasks(t, rec, func(tasks []*taskqueue.Task) bool {
		return countTasks(tasks, "extract", "a.txt") >= 1
	})

	// 模拟索引完成：DB 与磁盘一致
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FilesUpsert(&contract.FileInfo{
		RelPath: "a.txt", Size: stat.Size(), Mtime: stat.ModTime().UnixMilli(),
		DocType: "txt", IndexStatus: "indexed",
	}); err != nil {
		t.Fatal(err)
	}

	// 一致状态：无变更
	if changed := app.reconcileOnce(); changed {
		t.Fatal("一致的 mtime/size 不应触发变更")
	}

	// 修改文件：size 变化（内容更长）
	if err := os.WriteFile(path, []byte("v2 更长的内容触发 size 变化"), 0644); err != nil {
		t.Fatal(err)
	}
	if !app.reconcileOnce() {
		t.Fatal("mtime/size 变化应触发变更")
	}
	waitForTasks(t, rec, func(tasks []*taskqueue.Task) bool {
		return countTasks(tasks, "extract", "a.txt") >= 2
	})
}

func TestReconcileMarksDeletedFiles(t *testing.T) {
	workspace := t.TempDir()
	app, rec, st := newReconcileTestApp(t, workspace)

	path := filepath.Join(workspace, "gone.txt")
	if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.FilesUpsert(&contract.FileInfo{
		RelPath: "gone.txt", Size: stat.Size(), Mtime: stat.ModTime().UnixMilli(),
		DocType: "txt", IndexStatus: "indexed",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 从磁盘删除
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	// reconcile 发现磁盘缺失 → 入队 delete_index（标记缺失）
	if !app.reconcileOnce() {
		t.Fatal("磁盘缺失文件应触发变更")
	}
	waitForTasks(t, rec, func(tasks []*taskqueue.Task) bool {
		return findTask(tasks, "delete_index", "gone.txt") != nil
	})

	// 模拟 delete_index 任务执行完成：DB 标记 ignored（含"文件已删除"）
	if err := st.FilesMarkStatus(id, "ignored", "文件已删除"); err != nil {
		t.Fatal(err)
	}
	// ignored 且磁盘缺失 → 不再视为变更，也不重复入队
	if changed := app.reconcileOnce(); changed {
		t.Fatal("ignored 且磁盘缺失不应再触发变更")
	}
}

func TestReconcileReenqueuesPendingFiles(t *testing.T) {
	workspace := t.TempDir()
	app, rec, st := newReconcileTestApp(t, workspace)

	path := filepath.Join(workspace, "pending.txt")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FilesUpsert(&contract.FileInfo{
		RelPath: "pending.txt", Size: stat.Size(), Mtime: stat.ModTime().UnixMilli(),
		DocType: "txt", IndexStatus: "pending",
	}); err != nil {
		t.Fatal(err)
	}

	// 磁盘与 DB 一致（无 diff），pending 文件应重新入队（带 fileId），且不计入磁盘变更
	if changed := app.reconcileOnce(); changed {
		t.Fatal("pending 重新入队不应计入磁盘变更")
	}
	waitForTasks(t, rec, func(tasks []*taskqueue.Task) bool {
		for _, task := range tasks {
			if task.Type != "extract" {
				continue
			}
			if p, ok := task.Payload.(map[string]interface{}); ok {
				if rp, _ := p["relPath"].(string); rp == "pending.txt" {
					if fid, _ := p["fileId"].(float64); fid > 0 {
						return true
					}
				}
			}
		}
		return false
	})
}
