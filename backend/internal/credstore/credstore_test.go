package credstore

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCredStoreRoundTrip Set->Get 往返，且重新打开同一目录仍可读（持久化）。
func TestCredStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("New(%q) 失败: %v", dir, err)
	}
	if err := store.Set("llm", "api_key", "sk-123"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	v, err := store.Get("llm", "api_key")
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if v != "sk-123" {
		t.Fatalf("Get 返回 %q, want sk-123", v)
	}

	// 重新打开验证持久化（Windows DPAPI 解密须在同一用户上下文，测试运行于当前用户）
	store2, err := New(dir)
	if err != nil {
		t.Fatalf("重新 New 失败: %v", err)
	}
	v2, err := store2.Get("llm", "api_key")
	if err != nil {
		t.Fatalf("重新打开后 Get 失败: %v", err)
	}
	if v2 != "sk-123" {
		t.Fatalf("重新打开后 Get 返回 %q, want sk-123", v2)
	}
}

// TestCredStoreDelete Delete 后 Get 返回 os.ErrNotExist；删除不存在条目同样返回。
func TestCredStoreDelete(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := store.Set("svc", "k", "v"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if err := store.Delete("svc", "k"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, err := store.Get("svc", "k"); err != os.ErrNotExist {
		t.Fatalf("删除后 Get 返回 %v, want os.ErrNotExist", err)
	}
	if err := store.Delete("svc", "k"); err != os.ErrNotExist {
		t.Fatalf("删除不存在条目返回 %v, want os.ErrNotExist", err)
	}
}

// TestCredStoreKeyIsolation 不同 service 下同 key 相互隔离，删除互不影响。
func TestCredStoreKeyIsolation(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := store.Set("service-a", "shared", "A"); err != nil {
		t.Fatalf("Set A 失败: %v", err)
	}
	if err := store.Set("service-b", "shared", "B"); err != nil {
		t.Fatalf("Set B 失败: %v", err)
	}
	va, err := store.Get("service-a", "shared")
	if err != nil || va != "A" {
		t.Fatalf("Get(service-a) = %q, %v, want A, nil", va, err)
	}
	vb, err := store.Get("service-b", "shared")
	if err != nil || vb != "B" {
		t.Fatalf("Get(service-b) = %q, %v, want B, nil", vb, err)
	}
	if err := store.Delete("service-a", "shared"); err != nil {
		t.Fatalf("Delete(service-a) 失败: %v", err)
	}
	if _, err := store.Get("service-b", "shared"); err != nil {
		t.Fatalf("删除 service-a 影响了 service-b: %v", err)
	}
}

// TestCredStoreFilePermissions 凭据文件应为 0600（Windows 无权限位语义，跳过；内容已 DPAPI 加密）。
func TestCredStoreFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows 无 POSIX 权限位；凭据内容已由 DPAPI 绑定当前用户加密
		t.Skip("Windows 无 POSIX 权限位语义")
	}
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := store.Set("svc", "k", "v"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "credentials.bin"))
	if err != nil {
		t.Fatalf("Stat 凭据文件失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("凭据文件权限 = %v, want 0600", perm)
	}
}

// TestCredStoreCorruptFile 损坏的凭据文件应返回错误而非 panic/静默成功。
func TestCredStoreCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.bin"), []byte("{{{not-json"), 0600); err != nil {
		t.Fatalf("写入损坏文件失败: %v", err)
	}
	store, err := New(dir)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if _, err := store.Get("svc", "k"); err == nil {
		t.Fatal("损坏的凭据文件 Get 应返回错误")
	}
	if err := store.Set("svc", "k", "v"); err == nil {
		t.Fatal("损坏的凭据文件 Set 应返回错误")
	}
}

// TestCredStorePlaintextMigrationFlag 迁移标记：新建存储待迁移，Mark 后完成。
func TestCredStorePlaintextMigrationFlag(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if !store.HasPlaintextMigration() {
		t.Fatal("新建存储应报告尚有明文待迁移")
	}
	if err := store.MarkPlaintextMigrated(); err != nil {
		t.Fatalf("MarkPlaintextMigrated 失败: %v", err)
	}
	if store.HasPlaintextMigration() {
		t.Fatal("标记后不应再报告有待迁移明文")
	}
}
