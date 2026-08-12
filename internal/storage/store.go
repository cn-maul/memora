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

// dedupeIDs 过滤非法（<=0）并去重 ID，返回供 WHERE id IN (...) 使用的参数切片
// （保持首个出现顺序）。供 FilesByIDs / ChunksByIDs 批量查询复用。
func dedupeIDs(ids []int64) []interface{} {
	seen := make(map[int64]bool, len(ids))
	args := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		args = append(args, id)
	}
	return args
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

// Ping 探测数据库连接可用性（/ready、/diagnostics 使用）。
func (m *Module) Ping() error {
	return m.db.Ping()
}
