package taskqueue

import (
	"sync/atomic"
	"testing"
	"time"
)

// controlledHandler 可观测的测试 handler：记录调用次数；
// block=true 时每次调用读取一个放行令牌（未放行则阻塞），并用 ready 信号告知"已开始运行"。
type controlledHandler struct {
	calls int32
	gate  chan struct{} // 非 nil 时，每次调用需读取一个放行令牌
	ready chan struct{} // 非 nil 时，每次调用开始时发出信号
}

func newControlledHandler(block bool) *controlledHandler {
	h := &controlledHandler{}
	if block {
		h.gate = make(chan struct{}, 8)
		h.ready = make(chan struct{}, 8)
	}
	return h
}

func (h *controlledHandler) Handle(task *Task) error {
	atomic.AddInt32(&h.calls, 1)
	if h.ready != nil {
		h.ready <- struct{}{}
	}
	if h.gate != nil {
		<-h.gate
	}
	return nil
}

func (h *controlledHandler) count() int32 {
	return atomic.LoadInt32(&h.calls)
}

// allow 放行一个阻塞中的调用
func (h *controlledHandler) allow() {
	h.gate <- struct{}{}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// 同 generation 连续多次触发：队列中 reindex 至多一个，实际完整执行次数 ≤2
func TestTriggerReindexMergeRapid(t *testing.T) {
	h := newControlledHandler(false)
	m := New(h.Handle, nil)
	gen := "ws-1"
	for i := 0; i < 10; i++ {
		if err := m.TriggerReindex(gen); err != nil {
			t.Fatalf("TriggerReindex(%d) err: %v", i, err)
		}
	}
	if !m.WaitReindex(5 * time.Second) {
		t.Fatalf("WaitReindex timed out")
	}
	gotGen, running, followup := m.ReindexState()
	if gotGen != gen || running || followup {
		t.Fatalf("ReindexState unexpected: gen=%q running=%v followup=%v", gotGen, running, followup)
	}
	if got := h.count(); got < 1 || got > 2 {
		t.Fatalf("reindex 执行 %d 次，want 1..2（合并：≤1 次排队 + ≤1 次 follow-up）", got)
	}
	if m.reindexQueued {
		t.Fatalf("完成后 reindexQueued 应为 false")
	}
}

// 运行中再触发同 generation：执行 2 次（运行 1 次 + follow-up 1 次），不无限膨胀
func TestTriggerReindexDuringRunFollowupOnce(t *testing.T) {
	h := newControlledHandler(true)
	m := New(h.Handle, nil)
	gen := "ws-1"
	if err := m.TriggerReindex(gen); err != nil {
		t.Fatal(err)
	}
	<-h.ready // 第一次运行已开始
	gotGen, running, followup := m.ReindexState()
	if gotGen != gen || !running || followup {
		t.Fatalf("运行前状态: gen=%q running=%v followup=%v", gotGen, running, followup)
	}
	// 运行中多次触发同代际：只记录一次 follow-up
	for i := 0; i < 5; i++ {
		if err := m.TriggerReindex(gen); err != nil {
			t.Fatal(err)
		}
	}
	if _, running, followup := m.ReindexState(); !running || !followup {
		t.Fatalf("运行中状态: running=%v followup=%v", running, followup)
	}
	// 放行第一次运行：应自动再跑一轮 follow-up
	h.allow()
	if !waitFor(t, 3*time.Second, func() bool { return h.count() == 2 }) {
		t.Fatalf("follow-up 未执行, count=%d", h.count())
	}
	h.allow()
	if !m.WaitReindex(5 * time.Second) {
		t.Fatalf("WaitReindex timed out")
	}
	if got := h.count(); got != 2 {
		t.Fatalf("reindex 执行 %d 次，want 2（运行 1 次 + follow-up 1 次）", got)
	}
	// 不无限膨胀：稍等确认无第 3 轮
	time.Sleep(100 * time.Millisecond)
	if got := h.count(); got != 2 {
		t.Fatalf("reindex 膨胀到 %d 次，want 2", got)
	}
	if m.reindexQueued || m.reindexRunning {
		t.Fatalf("reindex 状态残留: queued=%v running=%v", m.reindexQueued, m.reindexRunning)
	}
}

// 不同 generation 互不合并：各执行一次
func TestTriggerReindexDifferentGenerationNoMerge(t *testing.T) {
	h := newControlledHandler(false)
	m := New(h.Handle, nil)
	if err := m.TriggerReindex("gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.TriggerReindex("gen-2"); err != nil {
		t.Fatal(err)
	}
	if !m.WaitReindex(5 * time.Second) {
		t.Fatalf("WaitReindex timed out")
	}
	if got := h.count(); got != 2 {
		t.Fatalf("reindex 执行 %d 次，want 2（不同 generation 各一次）", got)
	}
	// 最终代际为最后一次提交的代际
	if gotGen, _, _ := m.ReindexState(); gotGen != "gen-2" {
		t.Fatalf("ReindexState generation = %q, want gen-2", gotGen)
	}
}

// CancelAll 清除 follow-up，之后重新触发正常
func TestCancelAllClearsFollowup(t *testing.T) {
	h := newControlledHandler(true)
	m := New(h.Handle, nil)
	gen := "ws-1"
	if err := m.TriggerReindex(gen); err != nil {
		t.Fatal(err)
	}
	<-h.ready // 第一次运行已开始
	if err := m.TriggerReindex(gen); err != nil {
		t.Fatal(err)
	}
	if _, _, followup := m.ReindexState(); !followup {
		t.Fatalf("运行中触发应记录 follow-up")
	}
	if err := m.CancelAll(); err != nil {
		t.Fatal(err)
	}
	if _, _, followup := m.ReindexState(); followup {
		t.Fatalf("CancelAll 后 follow-up 应被清除")
	}
	// 放行当前运行：结束后不应再排 follow-up
	h.allow()
	if !m.WaitReindex(5 * time.Second) {
		t.Fatalf("WaitReindex timed out")
	}
	if got := h.count(); got != 1 {
		t.Fatalf("reindex 执行 %d 次，want 1（follow-up 已取消）", got)
	}
	// 取消后重新触发应恢复正常
	if err := m.TriggerReindex(gen); err != nil {
		t.Fatal(err)
	}
	<-h.ready
	h.allow()
	if !m.WaitReindex(5 * time.Second) {
		t.Fatalf("二次 WaitReindex timed out")
	}
	if got := h.count(); got != 2 {
		t.Fatalf("reindex 执行 %d 次，want 2（取消后重新触发）", got)
	}
}

// WaitReindex：卡死 handler 场景超时返回 false，放行后返回 true
func TestWaitReindexTimeoutOnStuckHandler(t *testing.T) {
	h := newControlledHandler(true)
	m := New(h.Handle, nil)
	if err := m.TriggerReindex("ws-1"); err != nil {
		t.Fatal(err)
	}
	<-h.ready
	if m.WaitReindex(150 * time.Millisecond) {
		t.Fatalf("卡死 handler 场景 WaitReindex 应超时返回 false")
	}
	h.allow()
	if !m.WaitReindex(5 * time.Second) {
		t.Fatalf("放行后 WaitReindex 应返回 true")
	}
}

// WaitReindex：无任务时立即返回 true；任务完成后返回 true
func TestWaitReindexReturnsTrueWhenDone(t *testing.T) {
	h := newControlledHandler(false)
	m := New(h.Handle, nil)
	if !m.WaitReindex(10 * time.Millisecond) {
		t.Fatalf("无 reindex 时 WaitReindex 应立即返回 true")
	}
	if err := m.TriggerReindex("ws-1"); err != nil {
		t.Fatal(err)
	}
	if !m.WaitReindex(5 * time.Second) {
		t.Fatalf("任务完成后 WaitReindex 应返回 true")
	}
	if got := h.count(); got != 1 {
		t.Fatalf("reindex 执行 %d 次，want 1", got)
	}
}
