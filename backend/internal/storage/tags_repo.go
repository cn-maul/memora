package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"memora/internal/contract"
)

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
