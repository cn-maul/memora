package storage

import (
	"reflect"
	"testing"

	"memora/internal/contract"
)

// TestFilesByIDs 空集 / 部分命中 / 全命中 / 重复 ID 去重。
func TestFilesByIDs(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	names := []string{"a.md", "b.md", "c.md"}
	ids := make([]int64, 0, len(names))
	for _, n := range names {
		id, err := m.FilesUpsert(&contract.FileInfo{
			RelPath: n, Size: 1, Mtime: 1, DocType: "md", IndexStatus: "indexed",
		})
		if err != nil {
			t.Fatalf("FilesUpsert 失败: %v", err)
		}
		ids = append(ids, id)
	}

	t.Run("空集", func(t *testing.T) {
		got, err := m.FilesByIDs(nil)
		if err != nil {
			t.Fatalf("FilesByIDs 失败: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("空集应返回空 map, 实际 %v", got)
		}
	})

	t.Run("部分命中", func(t *testing.T) {
		got, err := m.FilesByIDs([]int64{ids[0], ids[2], 99999})
		if err != nil {
			t.Fatalf("FilesByIDs 失败: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("部分命中应返回 2 条, 实际 %d", len(got))
		}
		if got[ids[0]] == nil || got[ids[2]] == nil || got[99999] != nil {
			t.Fatalf("部分命中结果不符: %v", got)
		}
		if got[ids[0]].RelPath != "a.md" {
			t.Fatalf("字段未填充: %+v", got[ids[0]])
		}
	})

	t.Run("全命中", func(t *testing.T) {
		got, err := m.FilesByIDs(ids)
		if err != nil {
			t.Fatalf("FilesByIDs 失败: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("全命中应返回 3 条, 实际 %d", len(got))
		}
		for _, id := range ids {
			if got[id] == nil {
				t.Fatalf("缺少文件 %d", id)
			}
		}
	})

	t.Run("重复 ID 去重", func(t *testing.T) {
		got, err := m.FilesByIDs([]int64{ids[0], ids[0], ids[1]})
		if err != nil {
			t.Fatalf("FilesByIDs 失败: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("重复 ID 应去重, 实际 %d", len(got))
		}
	})
}

// TestChunksByIDs 空集 / 部分命中 / 全命中。
func TestChunksByIDs(t *testing.T) {
	m, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatalf("新建存储模块失败: %v", err)
	}
	defer m.Close()

	fileID := mustInsertFile(t, m)
	chunks := []*contract.Chunk{
		{Seq: 0, TokenEst: 10, Text: "c0"},
		{Seq: 1, TokenEst: 20, Text: "c1"},
		{Seq: 2, TokenEst: 30, Text: "c2"},
	}
	if err := m.ReplaceFileIndex(fileID, chunks, [][]float32{makeUnitVec(4, 0), makeUnitVec(4, 1), makeUnitVec(4, 2)}, 4); err != nil {
		t.Fatalf("ReplaceFileIndex 失败: %v", err)
	}
	dbChunks, err := m.ChunksByFile(fileID)
	if err != nil || len(dbChunks) != 3 {
		t.Fatalf("查询分块失败: %v", err)
	}
	ids := []int64{dbChunks[0].ID, dbChunks[1].ID, dbChunks[2].ID}

	t.Run("空集", func(t *testing.T) {
		got, err := m.ChunksByIDs(nil)
		if err != nil {
			t.Fatalf("ChunksByIDs 失败: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("空集应返回空 map, 实际 %v", got)
		}
	})

	t.Run("部分命中", func(t *testing.T) {
		got, err := m.ChunksByIDs([]int64{ids[0], ids[2], 99999})
		if err != nil {
			t.Fatalf("ChunksByIDs 失败: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("部分命中应返回 2 条, 实际 %d", len(got))
		}
		if got[ids[0]] == nil || got[ids[2]] == nil || got[99999] != nil {
			t.Fatalf("部分命中结果不符: %v", got)
		}
		if got[ids[0]].Text != "c0" || got[ids[0]].FileID != fileID || got[ids[0]].Seq != 0 {
			t.Fatalf("字段未填充: %+v", got[ids[0]])
		}
	})

	t.Run("全命中", func(t *testing.T) {
		got, err := m.ChunksByIDs(ids)
		if err != nil {
			t.Fatalf("ChunksByIDs 失败: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("全命中应返回 3 条, 实际 %d", len(got))
		}
		want := map[int64]bool{ids[0]: true, ids[1]: true, ids[2]: true}
		gotSet := make(map[int64]bool, len(got))
		for id := range got {
			gotSet[id] = true
		}
		if !reflect.DeepEqual(gotSet, want) {
			t.Fatalf("全命中集合不符: got %v want %v", gotSet, want)
		}
	})
}
