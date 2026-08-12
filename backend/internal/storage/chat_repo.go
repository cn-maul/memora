package storage

import (
	"fmt"
	"time"

	"memora/internal/contract"
)

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

// SaveExchange 单事务保存一次问答回合：创建/复用会话并追加用户+助手两条消息。
// sessionID 为 0 时事务内新建会话；非 0 时校验会话存在（不存在则报错）。
// 全部成功才提交；任一失败整体回滚，绝不出现单边消息（P1-12）。
// 返回 sessionID 与消息总数。
func (m *Module) SaveExchange(sessionID int64, mode string, fileID int64, userMsg, assistantMsg, sources string, createdAt int64) (int64, int, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("[storage] 开始事务失败: %w", err)
	}
	defer tx.Rollback()

	if sessionID == 0 {
		// 新建会话
		res, err := tx.Exec(
			`INSERT INTO qa_sessions(created_at, mode, file_id) VALUES(?, ?, ?)`,
			createdAt, mode, fileID,
		)
		if err != nil {
			return 0, 0, fmt.Errorf("[storage] 创建问答会话失败: %w", err)
		}
		sessionID, err = res.LastInsertId()
		if err != nil {
			return 0, 0, fmt.Errorf("[storage] 获取会话 ID 失败: %w", err)
		}
	} else {
		// 校验会话存在（不存在的会话让后续 FK 失败，这里提前给出明确错误）
		var exists int
		if err := tx.QueryRow("SELECT COUNT(*) FROM qa_sessions WHERE id=?", sessionID).Scan(&exists); err != nil {
			return 0, 0, fmt.Errorf("[storage] 校验会话存在性失败: %w", err)
		}
		if exists == 0 {
			return 0, 0, fmt.Errorf("[storage] 会话不存在: %d", sessionID)
		}
	}

	// 事务内追加用户 + 助手两条消息（user 在前，assistant 在后）
	messages := []struct {
		role    string
		content string
		sources string
	}{
		{"user", userMsg, ""},
		{"assistant", assistantMsg, sources},
	}
	for _, msg := range messages {
		if _, err := tx.Exec(
			`INSERT INTO qa_messages(session_id, role, content, sources, created_at) VALUES(?, ?, ?, ?, ?)`,
			sessionID, msg.role, msg.content, msg.sources, createdAt,
		); err != nil {
			return 0, 0, fmt.Errorf("[storage] 追加问答消息失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("[storage] 提交事务失败: %w", err)
	}

	return sessionID, len(messages), nil
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
