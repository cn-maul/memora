package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// P1-16 词法级：ListDir 必须拒绝 ../ 越界与绝对路径 subPath。
func TestListDir_RejectsTraversal(t *testing.T) {
	tmp := t.TempDir()

	// 越界相对路径
	for _, sub := range []string{"..", "../..", "a/../.."} {
		if _, err := ListDir(tmp, sub); err == nil {
			t.Fatalf("ListDir(%q) 期望拒绝，实际 nil", sub)
		} else if !strings.Contains(err.Error(), "非法路径") {
			t.Fatalf("ListDir(%q) 错误应含 [browser] 非法路径，实际 %v", sub, err)
		}
	}

	// 绝对路径（Windows 与类 Unix 通用：传一个绝对目录作为 subPath）
	if _, err := ListDir(tmp, tmp); err == nil {
		t.Fatal("ListDir(绝对路径) 期望拒绝，实际 nil")
	}

	// 合法子目录不受影响
	sub := filepath.Join(tmp, "ok")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ListDir(tmp, "ok"); err != nil {
		t.Fatalf("ListDir(ok) 期望成功，实际 %v", err)
	}
}

// P1-16 词法级：OpenFile 必须拒绝 ../ 越界与绝对路径 relPath。
func TestOpenFile_RejectsTraversal(t *testing.T) {
	tmp := t.TempDir()

	for _, rel := range []string{"..", "../evil.txt", "a/../../evil.txt"} {
		if err := OpenFile(tmp, rel); err == nil {
			t.Fatalf("OpenFile(%q) 期望拒绝，实际 nil", rel)
		} else if !strings.Contains(err.Error(), "非法相对路径") {
			t.Fatalf("OpenFile(%q) 错误应含 [browser] 非法相对路径，实际 %v", rel, err)
		}
	}

	// 绝对路径
	if err := OpenFile(tmp, filepath.Join(tmp, "evil.txt")); err == nil {
		t.Fatal("OpenFile(绝对路径) 期望拒绝，实际 nil")
	}
}

// P1-16 最终路径级（仅 Windows）：junction 指向工作区外的文件必须被 FinalPath 拒绝。
// 无法创建 junction 时跳过（仅验证词法用例）。
func TestOpenFile_RejectsJunctionEscape(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junction 仅在 Windows 上可建，跳过")
	}

	workspace := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(workspace, "link")
	if err := makeJunction(t, link, outside); err != nil {
		t.Skipf("无法创建 junction，跳过: %v", err)
	}

	if err := OpenFile(workspace, "link/secret.txt"); err == nil {
		t.Fatal("OpenFile(junction 指向工作区外) 期望拒绝，实际 nil")
	}
}

// makeJunction 用 cmd mklink /J 创建目录 junction（无需管理员权限）。
func makeJunction(t *testing.T, link, target string) error {
	t.Helper()
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	// mklink 成功输出以中文/英文 "为 link 创建的 junction" 开头，仅确认链接已存在
	if _, err := os.Stat(link); err != nil {
		return err
	}
	_ = out
	return nil
}
