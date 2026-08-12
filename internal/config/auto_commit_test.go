package config

import (
	"path/filepath"
	"testing"
)

// newTestModule 在临时目录创建真实配置模块（含默认值落盘）
func newTestModule(t *testing.T) *Module {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := New(path, nil)
	if err != nil {
		t.Fatalf("New(%q) 失败: %v", path, err)
	}
	return m
}

// TestAutoCommitDefault 默认 GetAutoCommitConfig 应返回 enabled=true（默认自动保存开启）
func TestAutoCommitDefault(t *testing.T) {
	m := newTestModule(t)
	enabled, debounceSec := m.GetAutoCommitConfig()
	if !enabled {
		t.Fatalf("GetAutoCommitConfig() enabled = %v, want true", enabled)
	}
	if debounceSec != 90 {
		t.Fatalf("GetAutoCommitConfig() debounceSec = %d, want 90", debounceSec)
	}
}

// TestAutoCommitSetEnabled 关闭开关后生效，并持久化到磁盘（重新 New 后仍为 false）
func TestAutoCommitSetEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := New(path, nil)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	if err := m.Set("autoCommit.enabled", false); err != nil {
		t.Fatalf("Set(autoCommit.enabled, false) 失败: %v", err)
	}
	enabled, _ := m.GetAutoCommitConfig()
	if enabled {
		t.Fatalf("Set 后 GetAutoCommitConfig() enabled = true, want false")
	}

	// 重新加载验证持久化
	m2, err := New(path, nil)
	if err != nil {
		t.Fatalf("重新 New 失败: %v", err)
	}
	enabled2, _ := m2.GetAutoCommitConfig()
	if enabled2 {
		t.Fatal("持久化后 enabled 仍为 true, want false")
	}
}

// TestAutoCommitSetInvalidType 非 bool 值应返回错误且不改变配置
func TestAutoCommitSetInvalidType(t *testing.T) {
	m := newTestModule(t)
	if err := m.Set("autoCommit.enabled", "notbool"); err == nil {
		t.Fatal(`Set(autoCommit.enabled, "notbool") 应返回错误`)
	}
	enabled, _ := m.GetAutoCommitConfig()
	if !enabled {
		t.Fatal("类型错误的 Set 不应改变 enabled 值（仍应为 true）")
	}
}

// TestAutoCommitSetDebounceSec 设置 debounceSec 生效且持久化
func TestAutoCommitSetDebounceSec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := New(path, nil)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	if err := m.Set("autoCommit.debounceSec", 30); err != nil {
		t.Fatalf("Set(autoCommit.debounceSec, 30) 失败: %v", err)
	}
	enabled, debounceSec := m.GetAutoCommitConfig()
	if !enabled {
		t.Fatal("仅修改 debounceSec 不应影响 enabled（仍应为 true）")
	}
	if debounceSec != 30 {
		t.Fatalf("GetAutoCommitConfig() debounceSec = %d, want 30", debounceSec)
	}

	// 重新加载验证持久化
	m2, err := New(path, nil)
	if err != nil {
		t.Fatalf("重新 New 失败: %v", err)
	}
	_, debounceSec2 := m2.GetAutoCommitConfig()
	if debounceSec2 != 30 {
		t.Fatalf("持久化后 debounceSec = %d, want 30", debounceSec2)
	}
}
