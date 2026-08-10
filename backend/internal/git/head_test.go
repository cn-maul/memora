package git

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type testCfg struct{}

func (testCfg) GetAutoCommitConfig() (bool, int) { return true, 90 }
func (testCfg) GetGitAuthor() (string, string)   { return "Test", "t@test.x" }

// TestHeadEmptyRepo 空仓库（无提交）时 Head 不应报错（修复空仓库 500）
func TestHeadEmptyRepo(t *testing.T) {
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = repo

	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}

	head, err := m.Head()
	if err != nil {
		t.Fatalf("Head on empty repo: %v", err)
	}
	if head.HasCommits {
		t.Fatal("empty repo should have no commits")
	}
	if head.Branch == "" {
		t.Fatal("empty repo should still report a branch name")
	}
}

func TestHeadAndCommitFiles(t *testing.T) {
	dir := t.TempDir()

	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Test", Email: "t@test.x", When: time.Now()}

	// 首次提交
	write("a.txt", "hello")
	write("b.txt", "world")
	wt.Add(".")
	wt.Commit("首次提交", &gogit.CommitOptions{Author: sig})

	// 第二次提交：改 a、删 b、新增 sub/c
	write("a.txt", "hello world")
	os.Remove(filepath.Join(dir, "b.txt"))
	write("sub/c.txt", "new")
	wt.Add(".")
	if _, err := wt.Commit("修改a 删除b 新增c", &gogit.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}

	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}

	head, err := m.Head()
	if err != nil {
		t.Fatal(err)
	}
	if !head.HasCommits {
		t.Fatal("should have commits")
	}
	if head.CountFiles != 2 { // a.txt + sub/c.txt
		t.Fatalf("CountFiles = %d, want 2", head.CountFiles)
	}
	if head.ChangedFiles != 3 {
		t.Fatalf("ChangedFiles = %d, want 3", head.ChangedFiles)
	}

	commits, err := m.Log()
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("len(commits) = %d, want 2", len(commits))
	}

	// HEAD 提交文件明细
	files, err := m.CommitFiles(commits[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.Status
	}
	if got["a.txt"] != "modified" || got["b.txt"] != "deleted" || got["sub/c.txt"] != "added" {
		t.Fatalf("unexpected files: %+v", got)
	}

	// 首次提交：全部新增
	files0, err := m.CommitFiles(commits[1].Hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files0 {
		if f.Status != "added" {
			t.Fatalf("first commit file %s status = %s, want added", f.Path, f.Status)
		}
	}
}

// TestFileHistoryOnlyRealChanges 文件版本只应包含真实改动该文件的提交。
// 场景：首次提交含 a.txt（未再改动），后续提交只改 b.txt——a.txt 的历史应只有 1 条。
func TestFileHistoryOnlyRealChanges(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Test", Email: "t@test.x", When: time.Now()}

	// 首次提交：a.txt + b.txt
	write("a.txt", "hello")
	write("b.txt", "world")
	wt.Add(".")
	if _, err := wt.Commit("首次提交", &gogit.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}

	// 后续 3 次提交只改 b.txt（a.txt 始终未变）
	for i := 0; i < 3; i++ {
		write("b.txt", "world change "+string(rune('1'+i)))
		wt.Add(".")
		if _, err := wt.Commit("改 b", &gogit.CommitOptions{Author: sig}); err != nil {
			t.Fatal(err)
		}
	}

	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}

	// a.txt 从未改动：历史应只有首次提交 1 条
	histA, err := m.FileHistory("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(histA) != 1 {
		t.Fatalf("a.txt history = %d commits, want 1（未改动文件不应被后续提交计入）", len(histA))
	}

	// b.txt 改动 4 次（首次+3 次）：历史应为 4 条
	histB, err := m.FileHistory("b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(histB) != 4 {
		t.Fatalf("b.txt history = %d commits, want 4", len(histB))
	}
}

// TestFileHistoryAddAndDelete 覆盖 PathFilter 边界：文件在非根提交新增、以及删除文件的提交。
func TestFileHistoryAddAndDelete(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Test", Email: "t@test.x", When: time.Now()}

	// 根提交：只有 a.txt
	write("a.txt", "hello")
	wt.Add(".")
	if _, err := wt.Commit("根提交", &gogit.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}

	// 第二次提交：新增 c.txt（非根提交新增）
	write("c.txt", "new file")
	wt.Add(".")
	if _, err := wt.Commit("新增 c.txt", &gogit.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}

	// 第三次提交：删除 c.txt
	os.Remove(filepath.Join(dir, "c.txt"))
	wt.Add(".")
	if _, err := wt.Commit("删除 c.txt", &gogit.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}

	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}

	// c.txt 非根提交新增 + 删除提交 = 2 条历史
	histC, err := m.FileHistory("c.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(histC) != 2 {
		t.Fatalf("c.txt history = %d commits, want 2（新增+删除）", len(histC))
	}

	// a.txt 从未改动：只有根提交 1 条
	histA, err := m.FileHistory("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(histA) != 1 {
		t.Fatalf("a.txt history = %d commits, want 1", len(histA))
	}
}
