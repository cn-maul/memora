package transport

// 文件恢复（file history + restore）characterization 集成测试（审计 Phase 0）：
// 用真实 go-git + storage + timeline 在临时工作区构造提交历史，经 HTTP 链路
// GET /api/files/{id}/history 与 POST /api/files/{id}/restore 驱动恢复，
// 断言恢复到历史版本后磁盘文件内容被还原。
// 不依赖外部 LLM / Python / 网络。

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"memora/internal/contract"
)

// historyResponse 匹配 GET /api/files/{id}/history 的响应形状。
type historyResponse struct {
	Code string `json:"code"`
	Data struct {
		FileID  int64  `json:"fileId"`
		RelPath string `json:"relPath"`
		Commits []struct {
			Hash    string `json:"hash"`
			Message string `json:"message"`
		} `json:"commits"`
	} `json:"data"`
}

func restoreIDPath(id int64) string {
	return "/api/files/" + strconv.FormatInt(id, 10)
}

// 修改 + 提交后恢复到更早版本：内容应还原，且脏工作区在此过程中被自动快照。
func TestFileRestoreViaHTTP(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(ws, "doc.md")
	writeFile(t, doc, "v1 content\n")

	cfgPath := filepath.Join(base, ".memora", "config.json")
	m, _, gm, st := newWSHandler(t, cfgPath)
	defer st.Close()

	// 初始提交（v1）→ 修改并提交（v2）
	if err := gm.EnsureRepo(ws); err != nil {
		t.Fatalf("初始化 Git 失败: %v", err)
	}
	writeFile(t, doc, "v2 content\n")
	if _, err := gm.CommitManual("改版 v2"); err != nil {
		t.Fatalf("提交 v2 失败: %v", err)
	}

	// 入库文件记录（模拟索引器写入的元数据）
	fid, err := st.FilesUpsert(&contract.FileInfo{
		RelPath: "doc.md", Size: 12, Mtime: time.Now().UnixMilli(),
		DocType: "md", IndexStatus: "indexed",
	})
	if err != nil {
		t.Fatalf("写入文件记录失败: %v", err)
	}

	// GET history → 应含 2 条（初始 + v2，新→旧）
	rr := doReq(m, http.MethodGet, restoreIDPath(fid)+"/history", "")
	assertStatus(t, rr, http.StatusOK)
	var hist historyResponse
	decodeResp(t, rr, &hist)
	if len(hist.Data.Commits) != 2 {
		t.Fatalf("历史应含 2 条提交，got %d: %+v", len(hist.Data.Commits), hist.Data.Commits)
	}
	v1Hash := hist.Data.Commits[1].Hash
	if hist.Data.RelPath != "doc.md" {
		t.Fatalf("relPath = %q, want doc.md", hist.Data.RelPath)
	}

	// 再改成未提交的"脏"内容（验证 restore 前自动快照）
	writeFile(t, doc, "dirty content\n")

	// 恢复到 v1
	rr = doReq(m, http.MethodPost, restoreIDPath(fid)+"/restore",
		`{"commitHash":"`+v1Hash+`"}`)
	assertStatus(t, rr, http.StatusOK)
	var restoreResp struct {
		Code string `json:"code"`
		Data struct {
			OK       bool     `json:"ok"`
			Modified []string `json:"modified"`
		} `json:"data"`
	}
	decodeResp(t, rr, &restoreResp)
	if !restoreResp.Data.OK || len(restoreResp.Data.Modified) != 1 || restoreResp.Data.Modified[0] != "doc.md" {
		t.Fatalf("restore 响应不符: %+v", restoreResp)
	}

	got, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("读取恢复后文件失败: %v", err)
	}
	if string(got) != "v1 content\n" {
		t.Fatalf("恢复后内容 = %q, want %q", string(got), "v1 content\n")
	}
}

// 删除文件后恢复：RestoreFile 应重建磁盘文件。
func TestFileRestoreDeletedViaHTTP(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(ws, "note.txt")
	writeFile(t, note, "hello world\n")

	cfgPath := filepath.Join(base, ".memora", "config.json")
	m, _, gm, st := newWSHandler(t, cfgPath)
	defer st.Close()

	if err := gm.EnsureRepo(ws); err != nil {
		t.Fatalf("初始化 Git 失败: %v", err)
	}

	fid, err := st.FilesUpsert(&contract.FileInfo{
		RelPath: "note.txt", Size: 12, Mtime: time.Now().UnixMilli(),
		DocType: "txt", IndexStatus: "indexed",
	})
	if err != nil {
		t.Fatalf("写入文件记录失败: %v", err)
	}

	// 删除磁盘文件
	if err := os.Remove(note); err != nil {
		t.Fatal(err)
	}

	// history → 仅初始 1 条
	rr := doReq(m, http.MethodGet, restoreIDPath(fid)+"/history", "")
	assertStatus(t, rr, http.StatusOK)
	var hist historyResponse
	decodeResp(t, rr, &hist)
	if len(hist.Data.Commits) != 1 {
		t.Fatalf("删除前单一版本：历史应 1 条，got %d", len(hist.Data.Commits))
	}
	v1Hash := hist.Data.Commits[0].Hash

	// 恢复已删除文件 → 重建
	rr = doReq(m, http.MethodPost, restoreIDPath(fid)+"/restore",
		`{"commitHash":"`+v1Hash+`"}`)
	assertStatus(t, rr, http.StatusOK)
	var restoreResp struct {
		Code string `json:"code"`
		Data struct {
			OK bool `json:"ok"`
		} `json:"data"`
	}
	decodeResp(t, rr, &restoreResp)
	if !restoreResp.Data.OK {
		t.Fatalf("restore 未返回 ok=true: %s", rr.Body.String())
	}

	got, err := os.ReadFile(note)
	if err != nil {
		t.Fatalf("恢复后文件未重建: %v", err)
	}
	if string(got) != "hello world\n" {
		t.Fatalf("恢复后内容 = %q, want %q", string(got), "hello world\n")
	}
}

// 恢复到不存在的提交 → 500 internal（当前行为）。
func TestFileRestoreBadCommitHash(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, "doc.md"), "v1\n")

	cfgPath := filepath.Join(base, ".memora", "config.json")
	m, _, gm, st := newWSHandler(t, cfgPath)
	defer st.Close()

	if err := gm.EnsureRepo(ws); err != nil {
		t.Fatalf("初始化 Git 失败: %v", err)
	}
	fid, err := st.FilesUpsert(&contract.FileInfo{
		RelPath: "doc.md", Size: 3, Mtime: time.Now().UnixMilli(),
		DocType: "md", IndexStatus: "indexed",
	})
	if err != nil {
		t.Fatalf("写入文件记录失败: %v", err)
	}

	rr := doReq(m, http.MethodPost, restoreIDPath(fid)+"/restore",
		`{"commitHash":"0000000000000000000000000000000000000000"}`)
	assertStatus(t, rr, http.StatusInternalServerError)
}

// history 校验：非法 commitHash 不在 history 校验范围；此处仅锁定文件不存在 → 404。
func TestFileRestoreNonexistentFile(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, "doc.md"), "v1\n")

	cfgPath := filepath.Join(base, ".memora", "config.json")
	m, _, gm, st := newWSHandler(t, cfgPath)
	defer st.Close()

	if err := gm.EnsureRepo(ws); err != nil {
		t.Fatalf("初始化 Git 失败: %v", err)
	}

	rr := doReq(m, http.MethodPost, "/api/files/999/restore",
		`{"commitHash":"abc"}`)
	assertStatus(t, rr, http.StatusNotFound)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
