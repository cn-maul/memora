package storage

import (
	"database/sql"
	"fmt"

	"memora/internal/contract"
)

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
