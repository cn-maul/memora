package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P1-16：注入工作区根后，ExtractFile 对工作区外的路径必须在读取前拒绝。
func TestExtractFile_RejectsOutsideWorkspace(t *testing.T) {
	m, err := New(t.TempDir(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	outside := t.TempDir()
	evil := filepath.Join(outside, "evil.txt")
	if err := os.WriteFile(evil, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.SetWorkspaceRoot(workspace)

	if _, _, err := m.ExtractFile(evil); err == nil {
		t.Fatal("ExtractFile(工作区外路径) 期望拒绝，实际 nil")
	} else if !strings.Contains(err.Error(), "路径校验失败") {
		t.Fatalf("错误应含 [extract] 路径校验失败，实际 %v", err)
	}
}

// P1-16：未注入工作区根时不启用 containment（保持旧行为，不阻断合法调用方）。
func TestExtractFile_NoWorkspaceRootProceeds(t *testing.T) {
	m, err := New(t.TempDir(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	evil := filepath.Join(outside, "evil.txt")
	if err := os.WriteFile(evil, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 未设置 workspaceRoot：不校验，进入读取阶段
	if _, _, err := m.ExtractFile(evil); err == nil {
		t.Fatal("期望读取阶段报错（不可能是 nil），实际 nil")
	}
}
