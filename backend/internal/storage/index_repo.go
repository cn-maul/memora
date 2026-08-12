package storage

import (
	"container/heap"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"memora/internal/contract"
	"memora/internal/logx"
)

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

// ReplaceFileIndex 单事务原子替换某文件的全部分块与向量（P0-05）。
// 删除旧 chunks（级联删向量）→ 插入新 chunks → 写入全部向量 → 一次提交。
// 事务内完成，搜索方要么看到完整旧版本要么看到完整新版本。
func (m *Module) ReplaceFileIndex(fileID int64, chunks []*contract.Chunk, vectors [][]float32, dim int) error {
	if len(chunks) != len(vectors) {
		return fmt.Errorf("[storage] 分块与向量数量不一致: chunks=%d vectors=%d", len(chunks), len(vectors))
	}
	if dim <= 0 {
		return fmt.Errorf("[storage] 向量维度非法: %d", dim)
	}
	for _, v := range vectors {
		if len(v) != dim {
			return fmt.Errorf("[storage] 向量维度不一致: 期望 %d 实际 %d", dim, len(v))
		}
	}

	tx, err := m.db.Begin()
	if err != nil {
		return fmt.Errorf("[storage] 开始事务失败: %w", err)
	}
	defer tx.Rollback()

	// 先收集旧 chunkID，供事务提交后清理内存索引
	oldChunkIDs := make(map[int64]bool)
	oldRows, err := tx.Query("SELECT id FROM chunks WHERE file_id=?", fileID)
	if err != nil {
		return fmt.Errorf("[storage] 查询旧分块失败: %w", err)
	}
	for oldRows.Next() {
		var id int64
		if err := oldRows.Scan(&id); err != nil {
			oldRows.Close()
			return fmt.Errorf("[storage] 扫描旧分块失败: %w", err)
		}
		oldChunkIDs[id] = true
	}
	if err := oldRows.Err(); err != nil {
		oldRows.Close()
		return fmt.Errorf("[storage] 遍历旧分块失败: %w", err)
	}
	oldRows.Close()

	// 删旧块（外键级联删除 chunk_vectors）
	if _, err := tx.Exec("DELETE FROM chunks WHERE file_id=?", fileID); err != nil {
		return fmt.Errorf("[storage] 删除旧分块失败: %w", err)
	}

	// 插入新块并写入向量（chunk 新 ID 来自 LastInsertId）
	newChunkIDs := make([]int64, 0, len(chunks))
	for i, ch := range chunks {
		res, err := tx.Exec(
			`INSERT INTO chunks(file_id, seq, token_est, text) VALUES(?, ?, ?, ?)`,
			fileID, ch.Seq, ch.TokenEst, ch.Text,
		)
		if err != nil {
			return fmt.Errorf("[storage] 插入分块失败: %w", err)
		}
		chunkID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("[storage] 获取分块 ID 失败: %w", err)
		}
		newChunkIDs = append(newChunkIDs, chunkID)

		if _, err := tx.Exec(
			`INSERT INTO chunk_vectors(chunk_id, vec, dim) VALUES(?, ?, ?)`,
			chunkID, vecToBlob(vectors[i]), dim,
		); err != nil {
			return fmt.Errorf("[storage] 写入向量失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("[storage] 提交事务失败: %w", err)
	}

	// 同步内存向量索引：移除该文件旧条目，追加新条目（结构同 ChunksReplaceForFile）
	m.mu.Lock()
	if len(oldChunkIDs) > 0 {
		var newIndex []vectorEntry
		for _, e := range m.vectorIndex {
			if !oldChunkIDs[e.ChunkID] {
				newIndex = append(newIndex, e)
			}
		}
		m.vectorIndex = newIndex
	}
	for i, id := range newChunkIDs {
		m.vectorIndex = append(m.vectorIndex, vectorEntry{ChunkID: id, Vec: vectors[i]})
	}
	m.mu.Unlock()

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

// ChunksByIDs 按 ID 集合批量获取分块（单条 WHERE id IN (...) 查询）。
// 返回 map[id]*Chunk；未命中的 ID 不出现在 map 中。替代逐条 ChunksGet（消除 N+1）。
func (m *Module) ChunksByIDs(ids []int64) (map[int64]*contract.Chunk, error) {
	args := dedupeIDs(ids)
	if len(args) == 0 {
		return map[int64]*contract.Chunk{}, nil
	}
	placeholders := strings.Repeat("?,", len(args))
	placeholders = placeholders[:len(placeholders)-1]
	rows, err := m.db.Query(
		`SELECT id, file_id, seq, token_est, text FROM chunks WHERE id IN (`+placeholders+`)`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("[storage] 批量获取分块失败: %w", err)
	}
	defer rows.Close()

	chunks := make(map[int64]*contract.Chunk, len(args))
	for rows.Next() {
		c := &contract.Chunk{}
		if err := rows.Scan(&c.ID, &c.FileID, &c.Seq, &c.TokenEst, &c.Text); err != nil {
			return nil, fmt.Errorf("[storage] 扫描批量分块行失败: %w", err)
		}
		chunks[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("[storage] 遍历批量分块失败: %w", err)
	}
	return chunks, nil
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

// scoreMinHeap 固定容量小顶堆：堆顶是候选集里"最差"的条目
// （分数最低；分数并列时 ChunkID 最大）。用于 VectorsSearch 的 bounded top-K。
type scoreMinHeap struct {
	items []vectorEntry
}

func (h *scoreMinHeap) Len() int           { return len(h.items) }
func (h *scoreMinHeap) Less(i, j int) bool { return worseScore(h.items[i], h.items[j]) }
func (h *scoreMinHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *scoreMinHeap) Push(x interface{}) { h.items = append(h.items, x.(vectorEntry)) }
func (h *scoreMinHeap) Pop() interface{} {
	old := h.items
	n := len(old)
	e := old[n-1]
	h.items = old[:n-1]
	return e
}

// worseScore a 是否应排在 b 之后（分数更低；分数并列时 ChunkID 更大的排后面，
// 保证并列结果确定：升序取 ChunkID 较小的）。
func worseScore(a, b vectorEntry) bool {
	if a.Score != b.Score {
		return a.Score < b.Score
	}
	return a.ChunkID > b.ChunkID
}

// topKVectors 用固定容量小顶堆选出分数最高的 topK 个向量（bounded top-K，复杂度 O(N log K)，
// 不做全量排序）。返回按分数从高到低；分数并列时按 ChunkID 升序，与"全量排序取前 K"的结果一致。
func topKVectors(candidates []vectorEntry, topK int) []vectorEntry {
	if topK <= 0 || len(candidates) == 0 {
		return []vectorEntry{}
	}
	if topK > len(candidates) {
		topK = len(candidates)
	}
	h := &scoreMinHeap{}
	for _, c := range candidates {
		if h.Len() < topK {
			heap.Push(h, c)
		} else if worseScore(h.items[0], c) {
			// 新条目优于堆顶（当前最差条目）：弹出最差，压入新条目
			heap.Pop(h)
			heap.Push(h, c)
		}
	}
	// 依次弹出得到"最差→最好"，反转得到"最好→最差"
	out := make([]vectorEntry, 0, h.Len())
	for h.Len() > 0 {
		out = append(out, heap.Pop(h).(vectorEntry))
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// VectorsSearch 在内存索引中做余弦相似度线性扫描，返回 top-K。
// 采用固定容量小顶堆（container/heap）只保留最高分 topK 个，不做全量排序：
// 复杂度 O(N log K)，N=50k 时远快于全量 sort.Slice 的 O(N log N)，
// 也避免逐趟挑选在 K 接近 n 时退化为 O(n²)。
func (m *Module) VectorsSearch(queryVec []float32, topK int) ([]contract.VectorEntry, error) {
	if topK <= 0 {
		return []contract.VectorEntry{}, nil
	}

	m.mu.RLock()
	index := make([]vectorEntry, len(m.vectorIndex))
	copy(index, m.vectorIndex)
	m.mu.RUnlock()

	// 计算所有相似度（一次线性扫描）
	for i := range index {
		index[i].Score = cosineSimilarity(queryVec, index[i].Vec)
	}

	// bounded top-K：小顶堆只保留最高分的 topK 个（并列按 ChunkID 升序保证确定性）
	top := topKVectors(index, topK)

	result := make([]contract.VectorEntry, 0, len(top))
	for i := range top {
		result = append(result, contract.VectorEntry{
			ChunkID: top[i].ChunkID,
			Vec:     top[i].Vec,
			Score:   top[i].Score,
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
