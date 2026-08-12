package index

import (
	"path/filepath"
	"strings"
	"testing"

	"memora/internal/contract"
)

// P1-16：ProcessFile 对含 ../ 越界的 RelPath 必须返回错误并标记 failed，而非读取工作区外文件。
func TestProcessFile_TraversalRelPathMarkedFailed(t *testing.T) {
	s := newFakeStorage()
	m := newTestModule(s, &fakeLLM{dim: 4})

	tmpDir := t.TempDir()
	m.workspace = tmpDir
	// 工作区外真实存在的文件，验证 ProcessFile 不会去读它
	writeTestFile(t, filepath.Dir(tmpDir), "escape.md", "outside content")

	f := &contract.FileInfo{ID: 1, RelPath: "../escape.md", ContentHash: "old"}
	s.files[1] = f

	err := m.ProcessFile(f)
	if err == nil {
		t.Fatal("ProcessFile(越界 RelPath) 期望返回错误，实际 nil")
	}
	if !strings.Contains(err.Error(), "提取失败") {
		t.Fatalf("错误应含 [index] 提取失败，实际 %v", err)
	}
	if got := s.statuses[1]; got != "failed" {
		t.Fatalf("期望状态 failed，实际 %q", got)
	}
}

// P1-16：ProcessFile 对绝对路径 RelPath 同样必须拒绝。
func TestProcessFile_AbsoluteRelPathMarkedFailed(t *testing.T) {
	s := newFakeStorage()
	m := newTestModule(s, &fakeLLM{dim: 4})

	tmpDir := t.TempDir()
	m.workspace = tmpDir

	f := &contract.FileInfo{ID: 1, RelPath: filepath.Join(tmpDir, "escape.md"), ContentHash: "old"}
	s.files[1] = f

	if err := m.ProcessFile(f); err == nil {
		t.Fatal("ProcessFile(绝对路径 RelPath) 期望返回错误，实际 nil")
	}
	if got := s.statuses[1]; got != "failed" {
		t.Fatalf("期望状态 failed，实际 %q", got)
	}
}
