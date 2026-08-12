package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// expectedTables 迁移到最新版本后应存在的业务表
var expectedTables = []string{
	"files", "chunks", "chunk_vectors", "tags", "file_tags",
	"tag_overrides", "tag_suggestions", "commit_summaries",
	"qa_sessions", "qa_messages",
}

// tableNames 读取库内全部业务表名（排除 sqlite_ 内建表）
func tableNames(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("查询 sqlite_master 失败: %v", err)
	}
	defer rows.Close()
	names := make(map[string]bool)
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("扫描表名失败: %v", err)
		}
		names[n] = true
	}
	return names
}

// readUserVersion 读取 PRAGMA user_version
func readUserVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("读取 user_version 失败: %v", err)
	}
	return v
}

// TestMigrations_FreshDB 全新 DB 迁移到最新版本：user_version 正确、表齐全、生成备份。
func TestMigrations_FreshDB(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, 8)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	if v := readUserVersion(t, m.db); v != latestVersion() {
		t.Fatalf("期望 user_version=%d, 实际 %d", latestVersion(), v)
	}

	names := tableNames(t, m.db)
	for _, want := range expectedTables {
		if !names[want] {
			t.Fatalf("全新库缺少表: %s", want)
		}
	}

	// 迁移前应生成备份文件
	matches, err := filepath.Glob(filepath.Join(dir, "meta.db.bak-*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("期望生成 meta.db.bak-* 备份文件, 实际 %v (err=%v)", matches, err)
	}
}

// TestMigrations_OldDBUpgrade 手工构造"旧库"（仅 files 表 + user_version=0），
// 升级后表齐全且旧数据保留。
func TestMigrations_OldDBUpgrade(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "meta.db")

	old, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	_, _ = old.Exec(`CREATE TABLE files (
		  id            INTEGER PRIMARY KEY,
		  rel_path      TEXT NOT NULL UNIQUE,
		  size          INTEGER NOT NULL DEFAULT 0,
		  mtime         INTEGER NOT NULL,
		  content_hash  TEXT,
		  doc_type      TEXT NOT NULL,
		  index_status  TEXT NOT NULL DEFAULT 'pending',
		  last_error    TEXT,
		  first_seen_at INTEGER NOT NULL,
		  last_indexed_at INTEGER
		)`)
	if _, err := old.Exec(`INSERT INTO files(rel_path, size, mtime, doc_type, first_seen_at) VALUES('old.txt', 10, 1, 'txt', 1)`); err != nil {
		t.Fatalf("写入旧库文件失败: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	m, err := New(dir, 8)
	if err != nil {
		t.Fatalf("升级旧库失败: %v", err)
	}
	defer m.Close()

	if v := readUserVersion(t, m.db); v != latestVersion() {
		t.Fatalf("期望 user_version=%d, 实际 %d", latestVersion(), v)
	}

	names := tableNames(t, m.db)
	for _, want := range expectedTables {
		if !names[want] {
			t.Fatalf("升级后缺少表: %s", want)
		}
	}

	// 旧数据保留
	var cnt int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM files").Scan(&cnt); err != nil {
		t.Fatalf("查询旧文件失败: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("期望保留 1 条旧文件, 实际 %d", cnt)
	}
}

// TestMigrations_FailedMigration 注入必然失败的迁移：
// 返回错误、user_version 未变、事务整体回滚（无表残留）、DB 仍可打开。
func TestMigrations_FailedMigration(t *testing.T) {
	orig := migrations
	defer func() { migrations = orig }()
	bad := Migration{Version: 999, Apply: func(tx *sql.Tx) error {
		return fmt.Errorf("injected migration failure")
	}}
	migrations = append(append([]Migration{}, orig...), bad)

	dir := t.TempDir()
	if _, err := New(dir, 8); err == nil {
		t.Fatal("期望迁移失败返回错误")
	}

	// 迁移失败后 DB 仍可打开
	db, err := sql.Open("sqlite", filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatalf("迁移失败后数据库无法打开: %v", err)
	}
	defer db.Close()

	// user_version 未变（仍为 0）
	if v := readUserVersion(t, db); v != 0 {
		t.Fatalf("迁移失败后 user_version 应保持 0, 实际 %d", v)
	}

	// 迁移事务整体回滚：v1 建的表也不应残留
	names := tableNames(t, db)
	for _, want := range expectedTables {
		if names[want] {
			t.Fatalf("迁移失败后不应存在表: %s", want)
		}
	}
}
