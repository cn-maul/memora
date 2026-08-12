package storage

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"memora/internal/logx"
)

// Migration 数据库迁移步骤：Version 唯一标识 schema 版本，Apply 在单事务内执行该版本变更。
type Migration struct {
	Version int
	Apply   func(tx *sql.Tx) error
}

// migrations 版本化迁移表，按版本号升序排列（P1-14）。
// 新增 schema 变更时向末尾追加 {Version: n+1, Apply: ...}。
var migrations = []Migration{
	{Version: 1, Apply: migrateV1},
}

// latestVersion 返回当前最新 schema 版本号
func latestVersion() int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].Version
}

// migrateV1 初始 schema（v1）。
// 全部使用 IF NOT EXISTS，保证老库（user_version=0）升级时幂等不炸。
func migrateV1(tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS files (
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
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
		  id         INTEGER PRIMARY KEY,
		  file_id    INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		  seq        INTEGER NOT NULL,
		  token_est  INTEGER NOT NULL,
		  text       TEXT NOT NULL,
		  UNIQUE (file_id, seq)
		)`,
		`CREATE TABLE IF NOT EXISTS chunk_vectors (
		  chunk_id INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
		  vec      BLOB NOT NULL,
		  dim      INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tags (
		  id          INTEGER PRIMARY KEY,
		  name        TEXT NOT NULL UNIQUE,
		  source      TEXT NOT NULL,
		  created_at  INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS file_tags (
		  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		  tag_id  INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
		  origin  TEXT NOT NULL,
		  PRIMARY KEY (file_id, tag_id)
		)`,
		`CREATE TABLE IF NOT EXISTS tag_overrides (
		  file_id    INTEGER NOT NULL,
		  tag_name   TEXT NOT NULL,
		  action     TEXT NOT NULL,
		  created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tag_suggestions (
		  id          INTEGER PRIMARY KEY,
		  name        TEXT NOT NULL,
		  reason      TEXT,
		  suggested_by_file INTEGER NOT NULL,
		  status      TEXT NOT NULL DEFAULT 'pending',
		  created_at  INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS commit_summaries (
		  commit_hash TEXT PRIMARY KEY,
		  summary     TEXT NOT NULL,
		  generated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS qa_sessions (
		  id         INTEGER PRIMARY KEY,
		  created_at INTEGER NOT NULL,
		  mode       TEXT NOT NULL,
		  file_id    INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS qa_messages (
		  id          INTEGER PRIMARY KEY,
		  session_id  INTEGER NOT NULL REFERENCES qa_sessions(id) ON DELETE CASCADE,
		  role        TEXT NOT NULL,
		  content     TEXT NOT NULL,
		  sources     TEXT,
		  created_at  INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_files_rel_path ON files(rel_path)`,
		`CREATE INDEX IF NOT EXISTS idx_files_index_status ON files(index_status)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_file_id ON chunks(file_id)`,
		`CREATE INDEX IF NOT EXISTS idx_file_tags_file_id ON file_tags(file_id)`,
		`CREATE INDEX IF NOT EXISTS idx_file_tags_tag_id ON file_tags(tag_id)`,
		`CREATE INDEX IF NOT EXISTS idx_qa_messages_session ON qa_messages(session_id)`,
	}

	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("建表失败: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}

// schemaVersion 读取当前 schema 版本（PRAGMA user_version）
func (m *Module) schemaVersion() (int, error) {
	var v int
	if err := m.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("[storage] 读取 schema 版本失败: %w", err)
	}
	return v, nil
}

// backupDB 迁移前将当前 DB 文件复制为 meta.db.bak-<timestamp>（同 dataDir）。
// 先 WAL checkpoint 合并 WAL，保证备份为一致快照。
func (m *Module) backupDB() error {
	// 尽力合并 WAL；失败也不阻断，后续文件复制仍可执行
	_, _ = m.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	src := filepath.Join(m.dataDir, "meta.db")
	dst := filepath.Join(m.dataDir, fmt.Sprintf("meta.db.bak-%d", time.Now().UnixMilli()))

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// initSchema 版本化迁移（P1-14）：
// 读 user_version → 备份当前 DB → 单事务依次执行 version > user_version 的迁移
// → 提交后写入最新版本号。迁移失败则整体回滚，DB 保持不变。
func (m *Module) initSchema() error {
	current, err := m.schemaVersion()
	if err != nil {
		return err
	}

	var pending []Migration
	for _, mig := range migrations {
		if mig.Version > current {
			pending = append(pending, mig)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	// 升级前备份当前 DB 文件；备份失败仅告警，不阻断迁移
	if err := m.backupDB(); err != nil {
		logx.Warn("storage", "迁移前备份数据库失败", "err", err.Error())
	}

	tx, err := m.db.Begin()
	if err != nil {
		return fmt.Errorf("[storage] 迁移事务开始失败: %w", err)
	}
	defer tx.Rollback()

	for _, mig := range pending {
		if err := mig.Apply(tx); err != nil {
			return fmt.Errorf("[storage] 迁移到 v%d 失败: %w", mig.Version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("[storage] 迁移事务提交失败: %w", err)
	}

	// 提交后写入最新版本号（迁移语句幂等，重复执行无副作用）
	if _, err := m.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", latestVersion())); err != nil {
		return fmt.Errorf("[storage] 写入 schema 版本失败: %w", err)
	}

	logx.Info("storage", "数据库 schema 迁移完成", "from", current, "to", latestVersion())
	return nil
}
