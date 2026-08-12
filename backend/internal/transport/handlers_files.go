package transport

import (
	"io"
	"memora/internal/contract"
	"memora/internal/taskqueue"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// handleFiles GET /api/files
func (m *Module) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	status := getQueryParam(r, "status")
	tag := getQueryParam(r, "tag")
	page := getQueryInt(r, "page", 0)
	pageSize := getQueryInt(r, "pageSize", 50)
	// 钳制 pageSize 上限：批量标签查询的 IN 占位符受 SQLite 变量上限（32766）约束,
	// 且超大 pageSize 无意义（security_review low 观察）
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	sortOrder := getQueryParam(r, "sort") // 格式：field:asc / field:desc

	files, total, err := m.handler.Storage.FilesList(status, tag, page, pageSize, sortOrder)
	if err != nil {
		writeContractError(w, err)
		return
	}

	// 构造 items（含标签）,批量查询避免逐文件 N+1（修复审计发现）
	type FileItem struct {
		contract.FileInfo
		Tags []contract.FileTag `json:"tags"`
	}
	ids := make([]int64, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	tagMap, err := m.handler.Storage.FileTagsByFiles(ids)
	if err != nil {
		writeContractError(w, err)
		return
	}
	items := make([]FileItem, 0, len(files))
	for _, f := range files {
		items = append(items, FileItem{FileInfo: *f, Tags: tagMap[f.ID]})
	}

	writeOK(w, map[string]interface{}{
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
		"items":    items,
	})
}

// handleFileByID GET /api/files/{id} 等
func (m *Module) handleFileByID(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	idStr := getPathParam(path, "/api/files/")

	if idStr == "" || idStr == "search" {
		// /api/files/search 与 /api/files/ 无对应资源：
		// 此前直接 return 留下空 200 响应体,前端拿到 undefined 再点属性会白屏（review 发现）
		writeError(w, "not_found", "文件不存在", http.StatusNotFound)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, "bad_request", "无效文件 ID", http.StatusBadRequest)
		return
	}

	// GET /api/files/{id}
	if r.Method == http.MethodGet && !strings.Contains(path, "/text") && !strings.Contains(path, "/history") && !strings.Contains(path, "/tags") && !strings.Contains(path, "/restore") && !strings.Contains(path, "/open") {
		file, err := m.handler.Storage.FilesGet(id)
		if err != nil || file == nil {
			writeError(w, "not_found", "文件不存在", http.StatusNotFound)
			return
		}
		tags, _ := m.handler.Storage.FileTagsListByFile(file.ID)
		// 返回扁平结构：前端期望 FileItem = FileInfo + tags[]
		result := map[string]interface{}{
			"id":            file.ID,
			"relPath":       file.RelPath,
			"size":          file.Size,
			"mtime":         file.Mtime,
			"contentHash":   file.ContentHash,
			"docType":       file.DocType,
			"indexStatus":   file.IndexStatus,
			"lastError":     file.LastError,
			"firstSeenAt":   file.FirstSeenAt,
			"lastIndexedAt": file.LastIndexedAt,
			"tags":          tags,
		}
		writeOK(w, result)
		return
	}

	// GET /api/files/{id}/text
	if strings.HasSuffix(path, "/text") && r.Method == http.MethodGet {
		chunks, err := m.handler.Storage.ChunksByFile(id)
		if err != nil {
			writeContractError(w, err)
			return
		}
		var text string
		for _, c := range chunks {
			text += c.Text + "\n"
		}
		writeOK(w, map[string]string{"text": text})
		return
	}

	// POST /api/files/{id}/open
	if strings.HasSuffix(path, "/open") && r.Method == http.MethodPost {
		file, err := m.handler.Storage.FilesGet(id)
		if err != nil || file == nil {
			writeError(w, "not_found", "文件不存在", http.StatusNotFound)
			return
		}
		// 使用系统默认程序打开文件（修复 H-06）
		if err := m.handler.Browser.OpenFile(m.workspacePath(), file.RelPath); err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]bool{"ok": true})
		return
	}

	// GET /api/files/{id}/history
	if strings.HasSuffix(path, "/history") && r.Method == http.MethodGet {
		file, err := m.handler.Storage.FilesGet(id)
		if err != nil || file == nil {
			writeError(w, "not_found", "文件不存在", http.StatusNotFound)
			return
		}
		commits, err := m.handler.Git.FileHistory(file.RelPath)
		if err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]interface{}{
			"fileId":  id,
			"relPath": file.RelPath,
			"commits": commits,
		})
		return
	}

	// POST /api/files/{id}/tags
	if strings.HasSuffix(path, "/tags") && r.Method == http.MethodPost {
		var req struct {
			Add    []string `json:"add"`
			Remove []string `json:"remove"`
		}
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}
		if err := m.handler.Tag.ManualOverride(id, req.Add, req.Remove); err != nil {
			writeContractError(w, err)
			return
		}
		tags, _ := m.handler.Storage.FileTagsListByFile(id)
		writeOK(w, map[string]interface{}{"tags": tags})
		return
	}

	// POST /api/files/{id}/retry —— 将 failed 文件重置为 pending 并重新入队
	if strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost {
		if err := m.handler.Storage.FilesRetryStatus(id); err != nil {
			writeContractError(w, err)
			return
		}
		// 重新入队索引任务
		file, err := m.handler.Storage.FilesGet(id)
		if err == nil && file != nil {
			m.handler.TaskQueue.Submit(&taskqueue.Task{
				Type:    "extract",
				Payload: map[string]interface{}{"relPath": file.RelPath},
			})
		}
		writeOK(w, map[string]interface{}{"ok": true})
		return
	}

	// POST /api/files/{id}/restore
	if strings.HasSuffix(path, "/restore") && r.Method == http.MethodPost {
		file, err := m.handler.Storage.FilesGet(id)
		if err != nil || file == nil {
			writeError(w, "not_found", "文件不存在", http.StatusNotFound)
			return
		}

		var req struct {
			CommitHash string `json:"commitHash"`
		}
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}
		if err := m.handler.Timeline.Restore(file.RelPath, req.CommitHash); err != nil {
			// 恢复已无 409 门槛：工作区有改动时后端自动先保存当前状态再恢复
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]interface{}{"ok": true, "modified": []string{file.RelPath}})
		return
	}

	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleFileDownloadHistory GET /api/files/download-history?relPath=...&hash=...
// 下载文件在指定 git 提交时的历史版本内容,返回文件字节流。
func (m *Module) handleFileDownloadHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	relPath := getQueryParam(r, "relPath")
	hash := getQueryParam(r, "hash")
	if relPath == "" || hash == "" {
		writeError(w, "bad_request", "缺少 relPath 或 hash 参数", http.StatusBadRequest)
		return
	}
	// 校验 hash 为 40 位 hex（git 完整 SHA-1）,非法输入返回 400 而非 500
	if !isHexSHA1(hash) {
		writeError(w, "bad_request", "无效版本哈希", http.StatusBadRequest)
		return
	}

	content, err := m.handler.Git.ShowFileAt(relPath, hash)
	if err != nil {
		writeContractError(w, err)
		return
	}

	// 用 mime.FormatMediaType 生成 Content-Disposition,自动转义引号/反斜杠并剥离 CR/LF
	name := filepath.Base(relPath)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disposition)
	_, _ = io.WriteString(w, content)
}

// isHexSHA1 判断字符串是否为 40 位十六进制（git 完整哈希）
func isHexSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// handleFileResolve GET /api/files/resolve?relPath=...
// 返回对应文件 id（供详情弹窗关联版本历史）
func (m *Module) handleFileResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	relPath := getQueryParam(r, "relPath")
	if relPath == "" {
		writeError(w, "bad_request", "缺少 relPath 参数", http.StatusBadRequest)
		return
	}
	// 前端传的是浏览器模块的正斜杠路径,数据库 rel_path 在 Windows 上是反斜杠,
	// 需归一化后再查,否则子目录文件恒返回"未索引"（review 发现）
	file, err := m.handler.Storage.FilesFindByRelPath(filepath.FromSlash(relPath))
	if err != nil || file == nil {
		writeError(w, "not_found", "文件未索引", http.StatusNotFound)
		return
	}
	writeOK(w, map[string]interface{}{"fileId": file.ID})
}

// handleFilesRecent GET /api/files/recent?window=24&limit=50
// 返回最近 window 小时内修改的文件（按 mtime 倒序）。window=0 表示不限制时间窗。
func (m *Module) handleFilesRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	// 工作区未初始化无意义
	if m.workspacePath() == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}

	window := getQueryInt(r, "window", 24) // 小时；0 = 全部
	limit := getQueryInt(r, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var since int64
	if window > 0 {
		since = time.Now().Add(-time.Duration(window) * time.Hour).UnixMilli()
	}

	files, err := m.handler.Storage.FilesRecent(since, limit)
	if err != nil {
		writeContractError(w, err)
		return
	}

	// 批量附带标签（与 handleFiles 一致,避免逐文件 N+1）
	type RecentItem struct {
		contract.FileInfo
		Tags []contract.FileTag `json:"tags"`
	}
	ids := make([]int64, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	tagMap, _ := m.handler.Storage.FileTagsByFiles(ids)
	items := make([]RecentItem, 0, len(files))
	for _, f := range files {
		items = append(items, RecentItem{FileInfo: *f, Tags: tagMap[f.ID]})
	}

	writeOK(w, map[string]interface{}{
		"window": window,
		"items":  items,
	})
}
