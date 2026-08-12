package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureRepoCreatesInitialCommit 首次初始化：有文件的工作区应自动生成「初始版本」，
// 让 Timeline 从第一天就有基线（修复：此前直到首次文件变更才有提交）
func TestEnsureRepoCreatesInitialCommit(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/plan.md", "# 计划\n")
	write("readme.txt", "hello\n")

	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}

	head, err := m.Head()
	if err != nil {
		t.Fatal(err)
	}
	if !head.HasCommits {
		t.Fatal("初始化后应有初始版本提交")
	}
	logs, err := m.Log()
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("应恰好 1 条初始提交，got %d", len(logs))
	}
	if !strings.Contains(logs[0].Message, "初始版本") {
		t.Fatalf("初始提交信息应含「初始版本」，got %q", logs[0].Message)
	}

	files, err := m.ListTreeAt(head.Hash)
	if err != nil {
		t.Fatal(err)
	}
	// 初始版本应包含全部文件（含 EnsureRepo 自动创建的 .gitignore）
	if len(files) != 3 {
		t.Fatalf("初始版本应包含 3 个文件（含 .gitignore），got %d: %v", len(files), files)
	}

	// 重复 EnsureRepo 不应重复提交（幂等）
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}
	head2, _ := m.Head()
	if head2.Hash != head.Hash {
		t.Fatalf("重复 EnsureRepo 不应新建提交: %s != %s", head2.Hash, head.Hash)
	}
}

// TestEnsureRepoEmptyDirNoInitialCommit 空工作区初始化不产生提交（git 不允许空提交），
// 首个版本留给首次文件变更
func TestEnsureRepoEmptyDirNoInitialCommit(t *testing.T) {
	dir := t.TempDir()
	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}
	head, err := m.Head()
	if err != nil {
		t.Fatal(err)
	}
	if head.HasCommits {
		t.Fatal("空工作区不应产生初始提交")
	}
}

// TestRestoreDeletedFile 恢复已删除文件：历史版本中存在的文件、当前工作区已删除时，
// RestoreFile 应重建文件内容
func TestRestoreDeletedFile(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	write("a.txt", "版本一内容\n")
	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}
	head, _ := m.Head()
	hash := head.Hash

	// 模拟用户删除了文件
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}

	if err := m.RestoreFile("a.txt", hash); err != nil {
		t.Fatalf("恢复已删除文件失败: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "版本一内容\n" {
		t.Fatalf("恢复内容不符: %q", string(content))
	}
}
