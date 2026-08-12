package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"memora/internal/contract"
)

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

// FilesByIDs 按 ID 集合批量获取文件（单条 WHERE id IN (...) 查询）。
// 返回 map[id]*FileInfo；未命中的 ID 不出现在 map 中。替代逐条 FilesGet（消除 N+1）。
func (m *Module) FilesByIDs(ids []int64) (map[int64]*contract.FileInfo, error) {
	args := dedupeIDs(ids)
	if len(args) == 0 {
		return map[int64]*contract.FileInfo{}, nil
	}
	placeholders := strings.Repeat("?,", len(args))
	placeholders = placeholders[:len(placeholders)-1]
	rows, err := m.db.Query(
		`SELECT id, rel_path, size, mtime, content_hash, doc_type, index_status, COALESCE(last_error,''), first_seen_at, COALESCE(last_indexed_at,0)
		 FROM files WHERE id IN (`+placeholders+`)`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("[storage] 批量获取文件失败: %w", err)
	}
	defer rows.Close()

	files := make(map[int64]*contract.FileInfo, len(args))
	for rows.Next() {
		f := &contract.FileInfo{}
		if err := rows.Scan(&f.ID, &f.RelPath, &f.Size, &f.Mtime, &f.ContentHash, &f.DocType, &f.IndexStatus, &f.LastError, &f.FirstSeenAt, &f.LastIndexedAt); err != nil {
			return nil, fmt.Errorf("[storage] 扫描批量文件行失败: %w", err)
		}
		files[f.ID] = f
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("[storage] 遍历批量文件失败: %w", err)
	}
	return files, nil
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
