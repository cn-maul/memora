package assembler

// RuntimeManager 单元 + 工作区重建集成测试（P0-02/P0-03）：
// generation 递增、原子交换返回旧代、RebuildWorkspace 切换存储/传输引用并保存配置。
// 不依赖外部 LLM / Python / 网络：使用默认（空）配置，工作区用临时目录。

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeManagerGenerationIncrements(t *testing.T) {
	m := newRuntimeManager()
	g1 := m.beginBuild()
	g2 := m.beginBuild()
	if g1 == g2 || g1 == "" || g2 == "" {
		t.Fatalf("generation 应互不相同且非空, got %q / %q", g1, g2)
	}
	if m.Generation() != "" {
		t.Fatalf("尚未提交 Runtime 时 Generation 应为空, got %q", m.Generation())
	}
}

func TestRuntimeManagerCommitReturnsOld(t *testing.T) {
	m := newRuntimeManager()

	rt1 := &Runtime{Generation: "w1"}
	old := m.commit(rt1)
	if old != nil {
		t.Fatalf("首次 commit 应返回 nil 旧代, got %+v", old)
	}
	if m.Current() != rt1 || m.Generation() != "w1" {
		t.Fatalf("commit 后当前代应为 w1")
	}

	rt2 := &Runtime{Generation: "w2"}
	old = m.commit(rt2)
	if old != rt1 {
		t.Fatalf("第二次 commit 应返回 w1, got %+v", old)
	}
	if m.Current() != rt2 {
		t.Fatalf("commit 后当前代应为 w2")
	}
}

// TestRebuildWorkspaceSwitchesGenerationAndModules 用真实装配 App 验证工作区重建：
// 旧 storage 被替换、generation 递增、传输层 handler 引用同时切换、配置随工作区移动。
func TestRebuildWorkspaceSwitchesGenerationAndModules(t *testing.T) {
	cfgDir := t.TempDir()
	app, err := NewApp(context.Background(), filepath.Join(cfgDir, "config.json"))
	if err != nil {
		t.Fatalf("装配 App 失败: %v", err)
	}
	defer app.Shutdown()

	oldRT := app.runtime.Current()
	if oldRT == nil {
		t.Fatal("初始 Runtime 应为非 nil")
	}
	oldGen := app.runtime.Generation()
	oldStorage := oldRT.Storage

	workspace := t.TempDir()

	if err := app.RebuildWorkspace(workspace); err != nil {
		t.Fatalf("RebuildWorkspace 失败: %v", err)
	}

	cur := app.runtime.Current()
	if cur == nil {
		t.Fatal("重建后 Runtime 应为非 nil")
	}
	if app.runtime.Generation() == oldGen {
		t.Fatalf("重建后 generation 应递增, old=%q new=%q", oldGen, app.runtime.Generation())
	}
	if cur.Storage == oldStorage {
		t.Fatalf("重建后 storage 应替换为实例, old 与 new 相同")
	}
	if app.wsPath != workspace {
		t.Fatalf("重建后工作区应为 %q, got %q", workspace, app.wsPath)
	}
	if app.handler == nil || app.handler.Storage != cur.Storage {
		t.Fatalf("重建后传输层 handler 应指向新 storage")
	}
	if ws, err := app.Config.Get("workspace.path"); err != nil || ws != workspace {
		t.Fatalf("配置 workspace.path 应为 %q, got %v (err=%v)", workspace, ws, err)
	}

	// 触发重建应经队列执行并在超时内结束（空工作区立即完成）
	if err := app.TriggerReindex(); err != nil {
		t.Fatalf("TriggerReindex 失败: %v", err)
	}
	if !app.TaskQueue.WaitReindex(5 * time.Second) {
		t.Fatalf("等待 reindex 结束超时")
	}
}
