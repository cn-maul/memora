// Package storage SQLite 与内存向量索引的唯一访问者
package storage

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"memora/internal/contract"
	"memora/internal/logx"

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

// initSchema 创建表结构
func (m *Module) initSchema() error {
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
		if _, err := m.db.Exec(stmt); err != nil {
			return fmt.Errorf("[storage] 建表失败: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}

// ──────────────────── 文件操作 ────────────────────

// FilesUpsert 插入或更新文件元数据
// content_hash 仅在非空时更新，避免空值覆盖已有 hash
// 注意：ON CONFLICT 命中已有行时 LastInsertId() 不会返回冲突行 ID，
// 因此改用 rel_path 查询真实 ID（修复 H-03）。
func (m *Module) FilesUpsert(f *contract.FileInfo) (int64, error) {
	now := time.Now().UnixMilli()

	// 先检查是否已存在
	existing, _ := m.FilesFindByRelPath(f.RelPath)
	contentHash := f.ContentHash
	if contentHash == "" && existing != nil {
		contentHash = existing.ContentHash
	}

	_, err := m.db.Exec(
		`INSERT INTO files(rel_path, size, mtime, content_hash, doc_type, index_status, first_seen_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(rel_path) DO UPDATE SET
		   size=excluded.size, mtime=excluded.mtime,
		   content_hash=CASE WHEN excluded.content_hash!='' THEN excluded.content_hash ELSE files.content_hash END,
		   doc_type=excluded.doc_type, last_error=NULL`,
		f.RelPath, f.Size, f.Mtime, contentHash, f.DocType, f.IndexStatus, now,
	)
	if err != nil {
		return 0, fmt.Errorf("[storage] 写入文件失败: %w", err)
	}

	// 按 rel_path 查询真实 ID（避免 LastInsertId 在 UPDATE 分支返回错误值）
	row, err := m.FilesFindByRelPath(f.RelPath)
	if err != nil {
		return 0, fmt.Errorf("[storage] 回查文件 ID 失败: %w", err)
	}
	if row == nil {
		return 0, fmt.Errorf("[storage] 写入后未找到文件 %s", f.RelPath)
	}
	return row.ID, nil
}

// FilesRecent 返回最近修改的文件（按 mtime 倒序）。
// sinceMs > 0 时仅返回 mtime >= sinceMs 的文件（供"最近 X 小时"时间窗）；
// limit <= 0 时钳制为 50（上限 200）。
func (m *Module) FilesRecent(sinceMs int64, limit int) ([]*contract.FileInfo, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, rel_path, size, mtime, content_hash, doc_type, index_status, COALESCE(last_error,''), first_seen_at, COALESCE(last_indexed_at,0)
		FROM files`
	var args []interface{}
	if sinceMs > 0 {
		query += ` WHERE mtime >= ?`
		args = append(args, sinceMs)
	}
	query += ` ORDER BY mtime DESC LIMIT ?`
	args = append(args, limit)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("[storage] 查询最近文件失败: %w", err)
	}
	defer rows.Close()

	// 空结果返回空切片而非 nil，避免 JSON 序列化为 null
	files := make([]*contract.FileInfo, 0)
	for rows.Next() {
		f := &contract.FileInfo{}
		if err := rows.Scan(&f.ID, &f.RelPath, &f.Size, &f.Mtime, &f.ContentHash, &f.DocType, &f.IndexStatus, &f.LastError, &f.FirstSeenAt, &f.LastIndexedAt); err != nil {
			return nil, fmt.Errorf("[storage] 扫描最近文件行失败: %w", err)
		}
		files = append(files, f)
	}
	return files, nil
}

// FileVectorDim 返回文件任一向量的维度；文件无向量时返回 (0, false, nil)。
// 用于幂等跳过校验：切换嵌入模型后维度变化时强制重新嵌入（修复：维度不匹配导致检索为空）。
func (m *Module) FileVectorDim(fileID int64) (int, bool, error) {
	var dim int
	err := m.db.QueryRow(
		`SELECT cv.dim FROM chunk_vectors cv JOIN chunks c ON c.id=cv.chunk_id WHERE c.file_id=? LIMIT 1`,
		fileID).Scan(&dim)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("[storage] 查询向量维度失败: %w", err)
	}
	return dim, true, nil
}

// FilesFindByRelPath 按路径查找
func (m *Module) FilesFindByRelPath(relPath string) (*contract.FileInfo, error) {
	row := m.db.QueryRow(
		`SELECT id, rel_path, size, mtime, content_hash, doc_type, index_status, COALESCE(last_error,''), first_seen_at, COALESCE(last_indexed_at,0)
		 FROM files WHERE rel_path=?`, relPath)
	f := &contract.FileInfo{}
	err := row.Scan(&f.ID, &f.RelPath, &f.Size, &f.Mtime, &f.ContentHash, &f.DocType, &f.IndexStatus, &f.LastError, &f.FirstSeenAt, &f.LastIndexedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("[storage] 查找文件失败: %w", err)
	}
	return f, nil
}

// FilesGet 按 ID 获取文件
func (m *Module) FilesGet(id int64) (*contract.FileInfo, error) {
	row := m.db.QueryRow(
		`SELECT id, rel_path, size, mtime, content_hash, doc_type, index_status, COALESCE(last_error,''), first_seen_at, COALESCE(last_indexed_at,0)
		 FROM files WHERE id=?`, id)
	f := &contract.FileInfo{}
	err := row.Scan(&f.ID, &f.RelPath, &f.Size, &f.Mtime, &f.ContentHash, &f.DocType, &f.IndexStatus, &f.LastError, &f.FirstSeenAt, &f.LastIndexedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("[storage] 获取文件失败: %w", err)
	}
	return f, nil
}

// FilesFindByName 按文件名/路径模糊搜索（LIKE 匹配），返回已索引文件。
// 供 AI 问答的"标题模糊搜索"兜底：语义检索无结果时用文件名匹配定位文件。
func (m *Module) FilesFindByName(keyword string, limit int) ([]*contract.FileInfo, error) {
	if keyword == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	// 转义 LIKE 通配符，避免用户输入 % _ 干扰匹配
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
	rows, err := m.db.Query(
		`SELECT id, rel_path, size, mtime, content_hash, doc_type, index_status, COALESCE(last_error,''), first_seen_at, COALESCE(last_indexed_at,0)
		 FROM files
		 WHERE rel_path LIKE '%' || ? || '%' ESCAPE '\' AND index_status='indexed'
		 ORDER BY last_indexed_at DESC LIMIT ?`,
		escaped, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("[storage] 按名称搜索文件失败: %w", err)
	}
	defer rows.Close()

	files := make([]*contract.FileInfo, 0)
	for rows.Next() {
		f := &contract.FileInfo{}
		if err := rows.Scan(&f.ID, &f.RelPath, &f.Size, &f.Mtime, &f.ContentHash, &f.DocType, &f.IndexStatus, &f.LastError, &f.FirstSeenAt, &f.LastIndexedAt); err != nil {
			return nil, fmt.Errorf("[storage] 扫描文件行失败: %w", err)
		}
		files = append(files, f)
	}
	return files, nil
}

// FilesList 列出文件（支持过滤和分页）
// sortSpec 文件列表排序规则（排序字段 + 方向）
type sortSpec struct {
	field string // name|type|size|time|status，默认 time
	desc  bool
}

// parseSortOrder 解析 "field:asc" / "field:desc" 字符串
func parseSortOrder(sortOrder string) sortSpec {
	spec := sortSpec{field: "time", desc: true} // 默认：按索引时间倒序
	if sortOrder == "" {
		return spec
	}
	parts := strings.SplitN(sortOrder, ":", 2)
	field := strings.TrimSpace(parts[0])
	order := ""
	if len(parts) > 1 {
		order = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	// 白名单
	switch field {
	case "name", "type", "size", "time", "status":
		spec.field = field
	default:
		field = "time"
	}
	if order == "asc" {
		spec.desc = false
	} else {
		spec.desc = true
	}
	return spec
}

// buildFileOrder 构造 ORDER BY 子句
func buildFileOrder(spec sortSpec) string {
	col := "f.last_indexed_at"
	switch spec.field {
	case "name":
		col = "f.rel_path"
	case "type":
		col = "f.doc_type"
	case "size":
		col = "f.size"
	case "time":
		col = "f.last_indexed_at"
	case "status":
		col = "f.index_status"
	}
	dir := "DESC"
	if !spec.desc {
		dir = "ASC"
	}
	return "ORDER BY " + col + " " + dir
}

func (m *Module) FilesList(status, tag string, page, pageSize int, sortOrder string) ([]*contract.FileInfo, int, error) {
	where := "1=1"
	args := []interface{}{}

	if status != "" {
		where += " AND f.index_status=?"
		args = append(args, status)
	}
	if tag != "" {
		where += " AND EXISTS(SELECT 1 FROM file_tags ft JOIN tags t ON ft.tag_id=t.id WHERE ft.file_id=f.id AND t.name=?)"
		args = append(args, tag)
	}

	// 解析排序（格式：field:asc / field:desc，默认 time:desc）
	spec := parseSortOrder(sortOrder)

	// 计数
	var total int
	countSQL := "SELECT COUNT(*) FROM files f WHERE " + where
	if err := m.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("[storage] 文件计数失败: %w", err)
	}

	// 分页 + 排序
	orderClause := buildFileOrder(spec)
	if page < 0 {
		page = 0 // 防御：负数 page 致 offset 为负，SQLite OFFSET 负数报错（修复 review should-fix）
	}
	if pageSize <= 0 {
		pageSize = 50 // 防御：负数/零 pageSize 会使 LIMIT 语义异常
	}
	offset := page * pageSize
	sql := "SELECT f.id, f.rel_path, f.size, f.mtime, f.content_hash, f.doc_type, f.index_status, COALESCE(f.last_error,''), f.first_seen_at, COALESCE(f.last_indexed_at,0) FROM files f WHERE " + where + " " + orderClause + " LIMIT ? OFFSET ?"
	args = append(args, pageSize, offset)

	rows, err := m.db.Query(sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("[storage] 列出文件失败: %w", err)
	}
	defer rows.Close()

	// 空结果返回空切片而非 nil，避免 JSON 序列化为 null（修复：前端模板 .length 崩溃）
	files := make([]*contract.FileInfo, 0)
	for rows.Next() {
		f := &contract.FileInfo{}
		if err := rows.Scan(&f.ID, &f.RelPath, &f.Size, &f.Mtime, &f.ContentHash, &f.DocType, &f.IndexStatus, &f.LastError, &f.FirstSeenAt, &f.LastIndexedAt); err != nil {
			return nil, 0, fmt.Errorf("[storage] 扫描文件行失败: %w", err)
		}
		files = append(files, f)
	}
	return files, total, nil
}

// FilesMarkAllPending 重建前将所有文件状态重置为 pending（含 last_error 清理），
// 让重建过程在列表中对用户可见。
func (m *Module) FilesMarkAllPending() error {
	// 不重置 ignored（含"文件已删除"）：FullReindex 扫描时跳过这些文件，
	// 若重置为 pending 会导致它们永久停留在"待索引"（修复审计高危）
	_, err := m.db.Exec(
		`UPDATE files SET index_status='pending', last_error=NULL
		 WHERE index_status IN ('extracting','embedding','indexed','failed')`)
	if err != nil {
		return fmt.Errorf("[storage] 重置文件状态失败: %w", err)
	}
	return nil
}

// FilesMarkStatus 更新文件状态（状态机）
func (m *Module) FilesMarkStatus(id int64, status, lastError string) error {
	_, err := m.db.Exec(
		`UPDATE files SET index_status=?, last_error=?, last_indexed_at=CASE WHEN ? IN ('indexed','failed','ignored') THEN ? ELSE last_indexed_at END
		 WHERE id=?`,
		status, lastError, status, time.Now().UnixMilli(), id,
	)
	if err != nil {
		return fmt.Errorf("[storage] 更新文件状态失败: %w", err)
	}
	return nil
}

// FilesRetryStatus 将 failed 文件重置为 pending，供用户手动重试
func (m *Module) FilesRetryStatus(id int64) error {
	_, err := m.db.Exec(
		`UPDATE files SET index_status='pending', last_error='' WHERE id=? AND index_status='failed'`,
		id,
	)
	if err != nil {
		return fmt.Errorf("[storage] 重置文件状态失败: %w", err)
	}
	return nil
}

// ──────────────────── 分块操作 ────────────────────

// ChunksReplaceForFile 事务内替换文件的所有分块和向量
// 同步清理内存向量索引中的旧条目
func (m *Module) ChunksReplaceForFile(fileID int64, chuns []*contract.Chunk) error {
	tx, err := m.db.Begin()
	if err != nil {
		return fmt.Errorf("[storage] 开始事务失败: %w", err)
	}
	defer tx.Rollback()

	// 先查询该文件所有旧 chunk ID，用于清理内存索引
	oldRows, err := tx.Query("SELECT id FROM chunks WHERE file_id=?", fileID)
	oldChunkIDs := make(map[int64]bool)
	if err == nil {
		for oldRows.Next() {
			var id int64
			oldRows.Scan(&id)
			oldChunkIDs[id] = true
		}
		oldRows.Close()
	}

	// 删旧块（外键级联删除 chunk_vectors）
	if _, err := tx.Exec("DELETE FROM chunks WHERE file_id=?", fileID); err != nil {
		return fmt.Errorf("[storage] 删除旧分块失败: %w", err)
	}

	// 插入新块
	for _, ch := range chuns {
		_, err := tx.Exec(
			`INSERT INTO chunks(file_id, seq, token_est, text) VALUES(?, ?, ?, ?)`,
			fileID, ch.Seq, ch.TokenEst, ch.Text,
		)
		if err != nil {
			return fmt.Errorf("[storage] 插入分块失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("[storage] 提交事务失败: %w", err)
	}

	// 同步清理内存向量索引中该文件的旧向量
	if len(oldChunkIDs) > 0 {
		m.mu.Lock()
		var newIndex []vectorEntry
		for _, e := range m.vectorIndex {
			if !oldChunkIDs[e.ChunkID] {
				newIndex = append(newIndex, e)
			}
		}
		m.vectorIndex = newIndex
		m.mu.Unlock()
	}

	return nil
}

// ChunksByFile 获取文件的所有分块
func (m *Module) ChunksByFile(fileID int64) ([]*contract.Chunk, error) {
	rows, err := m.db.Query(
		`SELECT id, file_id, seq, token_est, text FROM chunks WHERE file_id=? ORDER BY seq`,
		fileID,
	)
	if err != nil {
		return nil, fmt.Errorf("[storage] 查询分块失败: %w", err)
	}
	defer rows.Close()

	var chunks []*contract.Chunk
	for rows.Next() {
		c := &contract.Chunk{}
		if err := rows.Scan(&c.ID, &c.FileID, &c.Seq, &c.TokenEst, &c.Text); err != nil {
			return nil, fmt.Errorf("[storage] 扫描分块行失败: %w", err)
		}
		chunks = append(chunks, c)
	}
	return chunks, nil
}

// ChunksGet 获取单个分块
func (m *Module) ChunksGet(id int64) (*contract.Chunk, error) {
	row := m.db.QueryRow(
		`SELECT id, file_id, seq, token_est, text FROM chunks WHERE id=?`, id)
	c := &contract.Chunk{}
	err := row.Scan(&c.ID, &c.FileID, &c.Seq, &c.TokenEst, &c.Text)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("[storage] 获取分块失败: %w", err)
	}
	return c, nil
}

// ──────────────────── 向量操作 ────────────────────

// vecToBlob float32 切片 → 小端字节 BLOB
func vecToBlob(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// blobToVec 小端字节 BLOB → float32 切片
func blobToVec(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("[storage] 向量 BLOB 长度异常: %d", len(blob))
	}
	vec := make([]float32, len(blob)/4)
	for i := range vec {
		bits := binary.LittleEndian.Uint32(blob[i*4:])
		vec[i] = math.Float32frombits(bits)
	}
	return vec, nil
}

// VectorsInsert 写入向量（同时更新内存索引）
func (m *Module) VectorsInsert(chunkID int64, vec []float32, dim int) error {
	blob := vecToBlob(vec)
	_, err := m.db.Exec(
		`INSERT OR REPLACE INTO chunk_vectors(chunk_id, vec, dim) VALUES(?, ?, ?)`,
		chunkID, blob, dim,
	)
	if err != nil {
		return fmt.Errorf("[storage] 写入向量失败: %w", err)
	}

	// 更新内存索引
	m.mu.Lock()
	entry := vectorEntry{ChunkID: chunkID, Vec: vec}
	// 如果已存在，替换
	found := false
	for i, e := range m.vectorIndex {
		if e.ChunkID == chunkID {
			m.vectorIndex[i] = entry
			found = true
			break
		}
	}
	if !found {
		m.vectorIndex = append(m.vectorIndex, entry)
	}
	m.mu.Unlock()

	return nil
}

// VectorsDelete 删除向量
func (m *Module) VectorsDelete(chunkID int64) error {
	_, err := m.db.Exec("DELETE FROM chunk_vectors WHERE chunk_id=?", chunkID)
	if err != nil {
		return fmt.Errorf("[storage] 删除向量失败: %w", err)
	}

	m.mu.Lock()
	for i, e := range m.vectorIndex {
		if e.ChunkID == chunkID {
			m.vectorIndex = append(m.vectorIndex[:i], m.vectorIndex[i+1:]...)
			break
		}
	}
	m.mu.Unlock()

	return nil
}

// VectorsLoadAll 从数据库全量加载向量到内存索引
func (m *Module) VectorsLoadAll() ([]contract.VectorEntry, error) {
	rows, err := m.db.Query("SELECT chunk_id, vec, dim FROM chunk_vectors")
	if err != nil {
		return nil, fmt.Errorf("[storage] 加载向量失败: %w", err)
	}
	defer rows.Close()

	var entries []contract.VectorEntry
	m.mu.Lock()
	m.vectorIndex = m.vectorIndex[:0] // 清空
	for rows.Next() {
		var chunkID int64
		var blob []byte
		var dim int
		if err := rows.Scan(&chunkID, &blob, &dim); err != nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("[storage] 扫描向量行失败: %w", err)
		}
		vec, err := blobToVec(blob)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		m.vectorIndex = append(m.vectorIndex, vectorEntry{ChunkID: chunkID, Vec: vec})
		entries = append(entries, contract.VectorEntry{ChunkID: chunkID, Vec: vec})
	}
	m.mu.Unlock()

	// 诊断日志：确认内存向量索引规模（"找不到文件"且 sources=[] 时先看这里）
	logx.Info("storage", "内存向量索引已加载", "count", len(entries), "dim", func() int {
		if len(entries) > 0 {
			return len(entries[0].Vec)
		}
		return 0
	}())

	return entries, nil
}

// cosineSimilarity 计算余弦相似度
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// VectorsSearch 在内存索引中做余弦相似度线性扫描，返回 top-K
// 先整体排序再取前 K，替代逐趟挑选的 O(n·K) 实现：
// K 接近 n（如单文件问答按 chunk 数放大 topK）时旧实现退化为 O(n²)，大索引下明显卡顿（review 发现）。
func (m *Module) VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error) {
	if topK <= 0 {
		return []contract.VectorEntry{}, nil
	}

	m.mu.RLock()
	index := make([]vectorEntry, len(m.vectorIndex))
	copy(index, m.vectorIndex)
	m.mu.RUnlock()

	// 计算所有相似度
	for i := range index {
		index[i].Score = cosineSimilarity(queryVec, index[i].Vec)
	}

	// 按相似度降序排序，截取前 topK
	sort.Slice(index, func(i, j int) bool { return index[i].Score > index[j].Score })
	if topK > len(index) {
		topK = len(index)
	}

	result := make([]contract.VectorEntry, 0, topK)
	for i := 0; i < topK; i++ {
		result = append(result, contract.VectorEntry{
			ChunkID: index[i].ChunkID,
			Vec:     index[i].Vec,
			Score:   index[i].Score,
		})
	}
	return result, nil
}

// VectorCount 查询已存在的向量总数（用于判断"维度变更后是否确有存量向量需重建"）。
// 单独统计：不依赖内存索引，直接从 chunk_vectors 表计数。
func (m *Module) VectorCount() (int, error) {
	var cnt int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM chunk_vectors").Scan(&cnt); err != nil {
		return 0, fmt.Errorf("[storage] 统计向量数量失败: %w", err)
	}
	return cnt, nil
}

// ──────────────────── 标签操作 ─────────────────────

// TagsList 列出所有标签（含计数）
func (m *Module) TagsList() ([]*contract.TagInfo, error) {
	rows, err := m.db.Query(
		`SELECT t.id, t.name, t.source, t.created_at, COUNT(ft.file_id) as cnt
		 FROM tags t LEFT JOIN file_tags ft ON ft.tag_id = t.id
		 GROUP BY t.id ORDER BY cnt DESC`)
	if err != nil {
		return nil, fmt.Errorf("[storage] 列出标签失败: %w", err)
	}
	defer rows.Close()

	// 空结果返回空切片而非 nil，避免 JSON 序列化为 null（与建议列表一致的防御）
	tags := make([]*contract.TagInfo, 0)
	for rows.Next() {
		t := &contract.TagInfo{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Source, &t.CreatedAt, &t.Count); err != nil {
			return nil, fmt.Errorf("[storage] 扫描标签行失败: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, nil
}

// TagsGetByName 按名称查找标签
func (m *Module) TagsGetByName(name string) (*contract.TagInfo, error) {
	row := m.db.QueryRow(
		`SELECT id, name, source, created_at FROM tags WHERE name=?`, name)
	t := &contract.TagInfo{}
	err := row.Scan(&t.ID, &t.Name, &t.Source, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("[storage] 查找标签失败: %w", err)
	}
	return t, nil
}

// TagsCreate 创建标签
func (m *Module) TagsCreate(name, source string) (int64, error) {
	result, err := m.db.Exec(
		`INSERT INTO tags(name, source, created_at) VALUES(?, ?, ?)`,
		name, source, time.Now().UnixMilli(),
	)
	if err != nil {
		return 0, fmt.Errorf("[storage] 创建标签失败: %w", err)
	}
	return result.LastInsertId()
}

// ──────────────── 文件标签 ────────────

// FileTagsReplace 替换文件的所有标签
func (m *Module) FileTagsReplace(fileID int64, tags []contract.FileTag) error {
	tx, err := m.db.Begin()
	if err != nil {
		return fmt.Errorf("[storage] 开始事务失败: %w", err)
	}
	defer tx.Rollback()

	// 删旧标签
	if _, err := tx.Exec("DELETE FROM file_tags WHERE file_id=?", fileID); err != nil {
		return fmt.Errorf("[storage] 删除旧标签失败: %w", err)
	}

	// 插新标签
	for _, ft := range tags {
		// 先确保标签存在（在事务内查询）
		var tagID int64
		err := tx.QueryRow("SELECT id FROM tags WHERE name=?", ft.Name).Scan(&tagID)
		if err == sql.ErrNoRows {
			// 自动创建（source=auto_generated）
			res, err := tx.Exec(
				`INSERT INTO tags(name, source, created_at) VALUES(?, ?, ?)`,
				ft.Name, "auto_generated", time.Now().UnixMilli(),
			)
			if err != nil {
				return fmt.Errorf("[storage] 创建标签失败: %w", err)
			}
			tagID, _ = res.LastInsertId()
		} else if err != nil {
			return fmt.Errorf("[storage] 查询标签失败: %w", err)
		}

		if _, err := tx.Exec(
			`INSERT INTO file_tags(file_id, tag_id, origin) VALUES(?, ?, ?)`,
			fileID, tagID, ft.Origin,
		); err != nil {
			return fmt.Errorf("[storage] 插入文件标签失败: %w", err)
		}
	}

	return tx.Commit()
}

// FileTagsListByFile 列出文件的所有标签
func (m *Module) FileTagsListByFile(fileID int64) ([]contract.FileTag, error) {
	tags, err := m.FileTagsByFiles([]int64{fileID})
	if err != nil {
		return nil, err
	}
	return tags[fileID], nil
}

// FileTagsByFiles 批量查询多个文件的标签，返回 map[fileID][]FileTag。
// 单次 SQL 取代逐文件查询（修复 handleFiles 的 N+1）。
func (m *Module) FileTagsByFiles(fileIDs []int64) (map[int64][]contract.FileTag, error) {
	result := make(map[int64][]contract.FileTag)
	if len(fileIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(fileIDs))
	args := make([]interface{}, len(fileIDs))
	for i, id := range fileIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT ft.file_id, t.name, ft.origin FROM file_tags ft
		JOIN tags t ON ft.tag_id=t.id
		WHERE ft.file_id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("[storage] 批量查询文件标签失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fileID int64
		var ft contract.FileTag
		if err := rows.Scan(&fileID, &ft.Name, &ft.Origin); err != nil {
			return nil, fmt.Errorf("[storage] 扫描文件标签行失败: %w", err)
		}
		result[fileID] = append(result[fileID], ft)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("[storage] 遍历文件标签行失败: %w", err)
	}
	return result, nil
}

// FileTagsListByTag 列出某标签下的所有文件 ID
func (m *Module) FileTagsListByTag(tagID int64) ([]int64, error) {
	rows, err := m.db.Query(
		`SELECT file_id FROM file_tags WHERE tag_id=?`, tagID)
	if err != nil {
		return nil, fmt.Errorf("[storage] 查询文件标签失败: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("[storage] 扫描文件标签行失败: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ──────────────────── 标签覆盖记录 ────────────────────

// OverridesAppend 添加手动修正记录
func (m *Module) OverridesAppend(fileID int64, tagName, action string) error {
	_, err := m.db.Exec(
		`INSERT INTO tag_overrides(file_id, tag_name, action, created_at) VALUES(?, ?, ?, ?)`,
		fileID, tagName, action, time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("[storage] 记录标签覆盖失败: %w", err)
	}
	return nil
}

// ──────────────────── 标签建议 ────────────────────

// SuggestionsAdd 添加标签建议
func (m *Module) SuggestionsAdd(name, reason string, suggestedByFile int64) (int64, error) {
	result, err := m.db.Exec(
		`INSERT INTO tag_suggestions(name, reason, suggested_by_file, created_at) VALUES(?, ?, ?, ?)`,
		name, reason, suggestedByFile, time.Now().UnixMilli(),
	)
	if err != nil {
		return 0, fmt.Errorf("[storage] 添加标签建议失败: %w", err)
	}
	return result.LastInsertId()
}

// SuggestionsListPending 列出待确认的建议
func (m *Module) SuggestionsListPending() ([]*contract.TagSuggestion, error) {
	rows, err := m.db.Query(
		`SELECT s.id, s.name, s.reason, s.suggested_by_file, s.status, s.created_at, f.rel_path
		 FROM tag_suggestions s LEFT JOIN files f ON f.id = s.suggested_by_file
		 WHERE s.status='pending' ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("[storage] 列出标签建议失败: %w", err)
	}
	defer rows.Close()

	// 空结果返回空切片而非 nil，避免 JSON 序列化为 null 导致前端崩溃（修复：接受建议后白屏）
	suggestions := make([]*contract.TagSuggestion, 0)
	for rows.Next() {
		s := &contract.TagSuggestion{}
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Reason, &s.SuggestedByFile, &s.Status, &s.CreatedAt, &s.RelPath,
		); err != nil {
			return nil, fmt.Errorf("[storage] 扫描建议行失败: %w", err)
		}
		suggestions = append(suggestions, s)
	}
	return suggestions, nil
}

// SuggestionsSetStatus 设置建议状态
func (m *Module) SuggestionsSetStatus(id int64, status string) error {
	_, err := m.db.Exec(
		`UPDATE tag_suggestions SET status=? WHERE id=?`, status, id,
	)
	if err != nil {
		return fmt.Errorf("[storage] 更新标签建议状态失败: %w", err)
	}
	return nil
}

// ──────────────── 提交摘要 ────────────

// SummariesUpsert 写入或更新提交摘要
func (m *Module) SummariesUpsert(hash, summary string, genAt int64) error {
	_, err := m.db.Exec(
		`INSERT OR REPLACE INTO commit_summaries(commit_hash, summary, generated_at) VALUES(?, ?, ?)`,
		hash, summary, genAt,
	)
	if err != nil {
		return fmt.Errorf("[storage] 写入提交摘要失败: %w", err)
	}
	return nil
}

// SummariesGet 获取提交摘要
func (m *Module) SummariesGet(hash string) (*contract.CommitSummary, error) {
	row := m.db.QueryRow(
		`SELECT commit_hash, summary, generated_at FROM commit_summaries WHERE commit_hash=?`, hash)
	s := &contract.CommitSummary{}
	err := row.Scan(&s.CommitHash, &s.Summary, &s.GeneratedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("[storage] 获取提交摘要失败: %w", err)
	}
	return s, nil
}

// ──────────────────── 问答 ────────────────────

// QASessionsCreate 创建问答会话
func (m *Module) QASessionsCreate(mode string, fileID int64) (int64, error) {
	result, err := m.db.Exec(
		`INSERT INTO qa_sessions(created_at, mode, file_id) VALUES(?, ?, ?)`,
		time.Now().UnixMilli(), mode, fileID,
	)
	if err != nil {
		return 0, fmt.Errorf("[storage] 创建问答会话失败: %w", err)
	}
	return result.LastInsertId()
}

// QASessionsList 列出所有会话
func (m *Module) QASessionsList() ([]*contract.QASession, error) {
	rows, err := m.db.Query(
		`SELECT s.id, s.created_at, s.mode, COALESCE(s.file_id,0),
		 (SELECT COUNT(*) FROM qa_messages WHERE session_id=s.id) as cnt
		 FROM qa_sessions s ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("[storage] 列出问答会话失败: %w", err)
	}
	defer rows.Close()

	// 空结果返回空切片而非 nil，避免 JSON 序列化为 null（修复：前端模板 .length 崩溃）
	sessions := make([]*contract.QASession, 0)
	for rows.Next() {
		s := &contract.QASession{}
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.Mode, &s.FileID, &s.MessageCount); err != nil {
			return nil, fmt.Errorf("[storage] 扫描会话行失败: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

// QASessionsDelete 删除会话
func (m *Module) QASessionsDelete(id int64) error {
	_, err := m.db.Exec("DELETE FROM qa_sessions WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("[storage] 删除问答会话失败: %w", err)
	}
	return nil
}

// QAMessagesAppend 追加消息
func (m *Module) QAMessagesAppend(sessionID int64, role, content, sources string, createdAt int64) (int64, error) {
	result, err := m.db.Exec(
		`INSERT INTO qa_messages(session_id, role, content, sources, created_at) VALUES(?, ?, ?, ?, ?)`,
		sessionID, role, content, sources, createdAt,
	)
	if err != nil {
		return 0, fmt.Errorf("[storage] 追加问答消息失败: %w", err)
	}
	return result.LastInsertId()
}

// QAMessagesBySession 获取会话消息列表
func (m *Module) QAMessagesBySession(sessionID int64) ([]*contract.QAMessage, error) {
	rows, err := m.db.Query(
		`SELECT id, session_id, role, content, COALESCE(sources,''), created_at
		 FROM qa_messages WHERE session_id=? ORDER BY id`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("[storage] 查询会话消息失败: %w", err)
	}
	defer rows.Close()

	var messages []*contract.QAMessage
	for rows.Next() {
		msg := &contract.QAMessage{}
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &msg.Sources, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("[storage] 扫描消息行失败: %w", err)
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// ──────────────── 恢复 ────────────

// RecoverPending 启动时将非终态文件重置为 pending
func (m *Module) RecoverPending() error {
	_, err := m.db.Exec(
		`UPDATE files SET index_status='pending' WHERE index_status NOT IN ('indexed','failed','ignored')`,
	)
	if err != nil {
		return fmt.Errorf("[storage] 恢复待处理文件失败: %w", err)
	}
	return nil
}

// ──────────── 生命周期 ────────────

// Close 关闭数据库连接
func (m *Module) Close() error {
	return m.db.Close()
}
