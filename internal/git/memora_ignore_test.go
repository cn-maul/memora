package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestEnsureRepoAddsMemoraIgnore 初始化仓库时，.gitignore 必须包含 .memora/ 规则，
// 防止含明文 API Key 的 .memora/config.json 与数据库进入 Git 历史（P0-01）。
func TestEnsureRepoAddsMemoraIgnore(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".memora"), 0755)
	os.WriteFile(filepath.Join(dir, ".memora", "config.json"), []byte(`{"llm":{"api_key":"sk-test"}}`), 0644)

	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".memora") {
		t.Fatalf(".gitignore 应包含 .memora 规则, got:\n%s", string(data))
	}

	// .memora/config.json 必须处于未跟踪状态，不能出现在提交内容里
	status, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	for f := range status {
		if strings.HasPrefix(f, ".memora") {
			t.Fatalf(".memora 路径不应出现在待提交状态中: %s", f)
		}
	}
}

// TestEnsureRepoOnExistingRepoEnforcesIgnore 复用已有仓库（PlainOpen 分支）时，
// 也必须补写 .memora ignore 规则，而不是只处理新建分支（P0-01 修复）。
func TestEnsureRepoOnExistingRepoEnforcesIgnore(t *testing.T) {
	dir := t.TempDir()
	// 先建一个不含 .memora 规则的仓库
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0644)

	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".memora") {
		t.Fatalf("复用已有仓库时 .gitignore 也应补写 .memora 规则, got:\n%s", string(data))
	}
}

// TestScanForMemoraLeaksDetectsTracked 已 tracked 的 .memora 文件应被泄漏扫描检出。
func TestScanForMemoraLeaksDetectsTracked(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0644)
	os.MkdirAll(filepath.Join(dir, ".memora"), 0755)
	os.WriteFile(filepath.Join(dir, ".memora", "config.json"), []byte(`{"key":"x"}`), 0644)

	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}
	// 模拟 ignore 前已被 track 的历史：手动把 .memora/config.json 加入 index
	wt, err := m.repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(".memora/config.json"); err != nil {
		t.Fatal(err)
	}

	leaks, err := ScanForMemoraLeaks(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range leaks {
		if l == ".memora/config.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应检出 .memora/config.json 泄露, got %v", leaks)
	}
}

// TestScanHistoryForMemoraLeaksDetects 已写入历史（即使当前已从 index 移除）的
// .memora 文件应被历史扫描检出（P0-01：可用历史检查）。
func TestScanHistoryForMemoraLeaksDetects(t *testing.T) {
	dir := t.TempDir()
	// 先建一个不含 ignore 规则的仓库，并让 .memora/config.json 进入一次历史提交
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0644)
	os.MkdirAll(filepath.Join(dir, ".memora"), 0755)
	os.WriteFile(filepath.Join(dir, ".memora", "config.json"), []byte(`{"key":"x"}`), 0644)

	m := New(testCfg{})
	if err := m.EnsureRepo(dir); err != nil {
		t.Fatal(err)
	}
	wt, err := m.repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("readme.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(".memora/config.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("模拟误提交 .memora", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@local", When: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	// 之后从 index 移除（假设人工清理了当前 state），仅历史残留
	if _, err := wt.Remove(".memora/config.json"); err != nil {
		t.Fatal(err)
	}

	leaks, err := ScanHistoryForMemoraLeaks(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range leaks {
		if l == ".memora/config.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("历史扫描应检出 .memora/config.json, got %v", leaks)
	}
}
