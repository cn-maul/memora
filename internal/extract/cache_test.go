package extract

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCacheFile 在缓存目录写入一个缓存文件并显式设置 mtime，返回文件名。
func writeCacheFile(t *testing.T, dir, name string, size int, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestCacheKey_VersionedPrefix 验证缓存 key 为版本化格式 v1-<sha256>，
// 且缓存命中读取的文件名为 v1-<hash>.md。
func TestCacheKey_VersionedPrefix(t *testing.T) {
	m, err := New(t.TempDir(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	content := "hello memora cache key"
	src := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256([]byte(content))
	wantKey := cacheVersionPrefix + "-" + fmt.Sprintf("%x", sum)

	// 预置版本化缓存文件，走缓存命中路径（无需 markitdown）
	cacheFile := filepath.Join(m.cacheDir, wantKey+".md")
	if err := os.WriteFile(cacheFile, []byte("# cached markdown"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, key, err := m.ExtractFile(src)
	if err != nil {
		t.Fatalf("ExtractFile 缓存命中失败: %v", err)
	}
	if key != wantKey {
		t.Fatalf("cacheKey 期望 %q，实际 %q", wantKey, key)
	}
	if !strings.HasPrefix(key, cacheVersionPrefix+"-") {
		t.Fatalf("cacheKey 缺少版本前缀 %q: %q", cacheVersionPrefix, key)
	}
	if text != "# cached markdown" {
		t.Fatalf("缓存命中文本期望 %q，实际 %q", "# cached markdown", text)
	}
}

// TestCacheKey_OldUnversionedIgnored 验证旧版本无前缀缓存文件不会被命中
// （版本化失效语义：v1- 前缀变更后旧缓存自动失效）。
func TestCacheKey_OldUnversionedIgnored(t *testing.T) {
	m, err := New(t.TempDir(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	content := "versioned cache please"
	src := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	legacy := filepath.Join(m.cacheDir, fmt.Sprintf("%x.md", sum))
	if err := os.WriteFile(legacy, []byte("# stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, _, err := m.ExtractFile(src)
	if err == nil && text == "# stale" {
		t.Fatal("旧版本无前缀缓存不应命中（不得返回陈旧内容）")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("旧缓存文件应仅被忽略，不应被删除: %v", err)
	}
}

// TestQuota_LRUEviction 验证配额超限时按 mtime 最旧优先删除（LRU 式）、
// 最新文件保留，且 mkd_* 临时输出文件不受配额管理。
func TestQuota_LRUEviction(t *testing.T) {
	m, err := New(t.TempDir(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	m.SetCacheQuota(250)

	old := time.Now().Add(-3 * time.Hour)
	mid := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)
	writeCacheFile(t, m.cacheDir, "v1-aaa.md", 100, old)
	writeCacheFile(t, m.cacheDir, "v1-bbb.md", 100, mid)
	writeCacheFile(t, m.cacheDir, "v1-ccc.md", 100, recent)
	// 临时输出文件不属于缓存项，不应被清理
	tmpPath := filepath.Join(m.cacheDir, "mkd_xyz.md")
	if err := os.WriteFile(tmpPath, make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}

	m.enforceCacheQuota()

	files, bytes, err := m.CacheStats()
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Fatalf("清理后缓存项期望 2，实际 %d", files)
	}
	if bytes != 200 {
		t.Fatalf("清理后总字节期望 200，实际 %d", bytes)
	}
	if _, err := os.Stat(filepath.Join(m.cacheDir, "v1-aaa.md")); !os.IsNotExist(err) {
		t.Fatalf("最旧文件 v1-aaa.md 应被删除，实际仍存在(err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(m.cacheDir, "v1-bbb.md")); err != nil {
		t.Fatalf("v1-bbb.md 应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.cacheDir, "v1-ccc.md")); err != nil {
		t.Fatalf("最新文件 v1-ccc.md 应保留: %v", err)
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("临时文件 mkd_xyz.md 不应被配额清理删除: %v", err)
	}
}

// TestQuota_KeepsNewestWhenOverQuota 验证单个超大缓存项即使超过配额也保留
// （配额清理至少保留最近文件，避免清空刚写入的缓存）。
func TestQuota_KeepsNewestWhenOverQuota(t *testing.T) {
	m, err := New(t.TempDir(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	m.SetCacheQuota(100)

	writeCacheFile(t, m.cacheDir, "v1-big.md", 200, time.Now().Add(-time.Hour))
	m.enforceCacheQuota()

	if _, err := os.Stat(filepath.Join(m.cacheDir, "v1-big.md")); err != nil {
		t.Fatalf("唯一缓存项即使超配额也应保留: %v", err)
	}
}

// TestCleanupExpired_OnlyExpired 验证 TTL 清理只删除 mtime 超过 maxAge 的缓存文件。
func TestCleanupExpired_OnlyExpired(t *testing.T) {
	m, err := New(t.TempDir(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	writeCacheFile(t, m.cacheDir, "v1-old.md", 10, time.Now().Add(-48*time.Hour))
	writeCacheFile(t, m.cacheDir, "v1-new.md", 10, time.Now().Add(-time.Hour))
	writeCacheFile(t, m.cacheDir, "v1-fresh.md", 10, time.Now())

	removed, err := m.CleanupExpired(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("期望删除 1 个过期文件，实际 %d", removed)
	}
	if _, err := os.Stat(filepath.Join(m.cacheDir, "v1-old.md")); !os.IsNotExist(err) {
		t.Fatalf("过期文件 v1-old.md 应被删除(err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(m.cacheDir, "v1-new.md")); err != nil {
		t.Fatalf("未过期文件 v1-new.md 应保留: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.cacheDir, "v1-fresh.md")); err != nil {
		t.Fatalf("未过期文件 v1-fresh.md 应保留: %v", err)
	}
}

// TestCacheStats 验证 CacheStats 只统计版本化缓存项（忽略 mkd_* 临时文件与
// 未版本化旧文件），数量与字节数正确。
func TestCacheStats(t *testing.T) {
	m, err := New(t.TempDir(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	writeCacheFile(t, m.cacheDir, "v1-a.md", 100, time.Now())
	writeCacheFile(t, m.cacheDir, "v1-b.md", 250, time.Now())
	if err := os.WriteFile(filepath.Join(m.cacheDir, "mkd_tmp.md"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.cacheDir, "legacy.md"), make([]byte, 900), 0o644); err != nil {
		t.Fatal(err)
	}

	files, bytes, err := m.CacheStats()
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Fatalf("CacheStats files 期望 2，实际 %d", files)
	}
	if bytes != 350 {
		t.Fatalf("CacheStats bytes 期望 350，实际 %d", bytes)
	}
}
