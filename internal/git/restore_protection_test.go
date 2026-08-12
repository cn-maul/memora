package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setupRestoreRepo 建一个工作区并生成含单个文件的初始提交，返回模块与初始提交 hash。
// 测试与 git 模块同包，可直接访问 m.path。
func setupRestoreRepo(t *testing.T, rel, content string) (*Module, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0755)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
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
		t.Fatal("初始化后应有初始提交")
	}
	return m, head.Hash
}

// TestRestoreFileConflictBackup 冲突保护（P1-05）：提交版本A后工作区被改成内容B，
// 恢复版本A应成功，工作区回到A，且同目录生成 .restore-bak-* 备份（内容为B），
// LastRestoreBackup 返回该备份路径——用户内容不被静默覆盖丢失。
func TestRestoreFileConflictBackup(t *testing.T) {
	m, hash := setupRestoreRepo(t, "note.txt", "版本A")

	// 工作区手动改成不同内容 B（未提交），触发冲突
	fp := filepath.Join(m.path, "note.txt")
	if err := os.WriteFile(fp, []byte("版本B"), 0644); err != nil {
		t.Fatal(err)
	}
	if m.LastRestoreBackup() != "" {
		t.Fatal("恢复前不应有遗留备份记录")
	}

	if err := m.RestoreFile("note.txt", hash); err != nil {
		t.Fatalf("冲突恢复失败: %v", err)
	}

	// 工作区文件回到历史版本 A
	got, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "版本A" {
		t.Fatalf("恢复后内容=%q, want 版本A", string(got))
	}

	// LastRestoreBackup 应返回同目录的备份路径
	backup := m.LastRestoreBackup()
	if backup == "" {
		t.Fatal("冲突恢复应记录备份路径（LastRestoreBackup 非空）")
	}
	if !strings.HasPrefix(filepath.Base(backup), "note.txt.restore-bak-") {
		t.Fatalf("备份应生成在目标文件旁且带 .restore-bak- 标记: %q", filepath.Base(backup))
	}
	if filepath.Dir(backup) != filepath.Dir(fp) {
		t.Fatalf("备份应位于目标同目录: %q", backup)
	}
	bcontent, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("读取备份失败: %v", err)
	}
	if string(bcontent) != "版本B" {
		t.Fatalf("备份内容=%q, want 版本B（不得丢失用户内容）", string(bcontent))
	}
}

// TestRestoreFileIdempotentNoBackup 幂等（P1-05）：文件内容已是历史版本时，
// 恢复成功且不产生备份，LastRestoreBackup 保持为空。
func TestRestoreFileIdempotentNoBackup(t *testing.T) {
	m, hash := setupRestoreRepo(t, "note.txt", "版本A")

	if m.LastRestoreBackup() != "" {
		t.Fatal("不应有预置备份记录")
	}
	if err := m.RestoreFile("note.txt", hash); err != nil {
		t.Fatalf("幂等恢复失败: %v", err)
	}
	if m.LastRestoreBackup() != "" {
		t.Fatalf("幂等恢复不应产生备份, got %q", m.LastRestoreBackup())
	}
	entries, err := os.ReadDir(filepath.Join(m.path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".restore-bak-") {
			t.Fatalf("幂等恢复不应留下备份文件: %v", e.Name())
		}
	}
}

// TestRestoreFileDeletedRecreatesParent 恢复已删除文件（P1-05/P1-16）：
// 目标与父目录都被删除时，应整体重建父目录并写入历史内容。
func TestRestoreFileDeletedRecreatesParent(t *testing.T) {
	m, hash := setupRestoreRepo(t, filepath.Join("docs", "note.md"), "# 文档\n")

	if err := os.RemoveAll(filepath.Join(m.path, "docs")); err != nil {
		t.Fatal(err)
	}
	if err := m.RestoreFile(filepath.Join("docs", "note.md"), hash); err != nil {
		t.Fatalf("恢复已删除文件失败: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(m.path, "docs", "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# 文档\n" {
		t.Fatalf("恢复内容=%q, want %q", string(got), "# 文档\n")
	}
}

// TestRestoreFileLexicalTraversalRejected 词法越界（P1-16）：relPath 含 `..` 应被拒绝，
// 不依赖文件是否存在。
func TestRestoreFileLexicalTraversalRejected(t *testing.T) {
	m, hash := setupRestoreRepo(t, "note.txt", "版本A")

	err := m.RestoreFile(filepath.Join("..", "escape.txt"), hash)
	if err == nil {
		t.Fatal("含 .. 的 relPath 应被拒绝")
	}
	if !strings.Contains(err.Error(), "越界") {
		t.Fatalf("错误应指明越界, got %v", err)
	}
}

// TestRestoreFileAbsolutePathRejected 绝对路径（P1-16）：应被拒绝。
func TestRestoreFileAbsolutePathRejected(t *testing.T) {
	m, hash := setupRestoreRepo(t, "note.txt", "版本A")

	err := m.RestoreFile(filepath.Join(m.path, "..", "abs.txt"), hash)
	if err == nil {
		t.Fatal("绝对路径应被拒绝")
	}
	if !strings.Contains(err.Error(), "越界") && !strings.Contains(err.Error(), "绝对路径") {
		t.Fatalf("错误应指明路径违规, got %v", err)
	}
}

// TestRestoreFileJunctionEscapeRejected Windows junction 越界（P1-16）：
// 工作区内 junction 指向工作区外目录时，恢复应被拒绝且不会写穿到外部。
// Windows 上 EvalSymlinks 不解析 junction，本测试能验证 ensureNoLinkAncestor 的 fail-closed 拦截。
func TestRestoreFileJunctionEscapeRejected(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 验证 junction")
	}
	m, hash := setupRestoreRepo(t, filepath.Join("link", "secret.txt"), "版本A")

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("外部数据"), 0644); err != nil {
		t.Fatal(err)
	}
	// 用 junction 把工作区内 link 目录指向外部目录
	linkDir := filepath.Join(m.path, "link")
	if err := os.RemoveAll(linkDir); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", linkDir, outside)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("无法创建 junction（权限不足），跳过: %v %s", err, string(b))
	}

	err := m.RestoreFile(filepath.Join("link", "secret.txt"), hash)
	if err == nil {
		t.Fatal("指向工作区外的 junction 恢复应被拒绝")
	}
	if !strings.Contains(err.Error(), "链接") && !strings.Contains(err.Error(), "越界") {
		t.Fatalf("错误应指明链接越界, got %v", err)
	}

	// 外部文件必须保持原样（不得被恢复写穿覆盖）
	got, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "外部数据" {
		t.Fatalf("外部文件不得被恢复覆盖, got %q", string(got))
	}
}

// TestShowFileAtTraversalRejected showFileAtLocked 的词法入口校验（P1-16）：
// ShowFileAt 读取含 `..` 的路径也应被拒绝。
func TestShowFileAtTraversalRejected(t *testing.T) {
	m, hash := setupRestoreRepo(t, "note.txt", "版本A")

	_, err := m.ShowFileAt(filepath.Join("..", "note.txt"), hash)
	if err == nil {
		t.Fatal("ShowFileAt 含 .. 的路径应被拒绝")
	}
	if !strings.Contains(err.Error(), "越界") {
		t.Fatalf("错误应指明越界, got %v", err)
	}

	// 正常路径读取不受影响
	content, err := m.ShowFileAt("note.txt", hash)
	if err != nil {
		t.Fatalf("正常路径读取失败: %v", err)
	}
	if content != "版本A" {
		t.Fatalf("content=%q, want 版本A", content)
	}
}
