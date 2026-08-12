// Package storage SQLite 与内存向量索引的唯一访问者
package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// VectorIndex 内存向量索引条目
type vectorEntry struct {
	ChunkID int64
	Vec     []float32
	Score   float64
}

// Module 存储模块
type Module struct {
	db      *sql.DB
	dataDir string
	dim     int

	mu          sync.RWMutex
	vectorIndex []vectorEntry // 内存向量索引

	initOnce sync.Once
}

// New 创建存储模块，初始化 SQLite 并建表
func New(dataDir string, dim int) (*Module, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("[storage] 创建数据目录失败: %w", err)
	}

	dbPath := filepath.Join(dataDir, "meta.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1")
	if err != nil {
		return nil, fmt.Errorf("[storage] 打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1) // 串行写入

	// 显式启用外键
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("[storage] 启用外键失败: %w", err)
	}

	m := &Module{
		db:      db,
		dataDir: dataDir,
		dim:     dim,
	}

	if err := m.initSchema(); err != nil {
		db.Close()
		return nil, err
	}

	return m, nil
}

// ──────────── 生命周期 ────────────

// Close 关闭数据库连接
func (m *Module) Close() error {
	return m.db.Close()
}
