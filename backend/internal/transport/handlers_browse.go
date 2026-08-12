package transport

import (
	"encoding/json"
	"net/http"
	"path/filepath"
)

// handleBrowse GET /api/browse?path=subPath
// 资源管理器式浏览工作区目录。
func (m *Module) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	ws := m.workspacePath()
	if ws == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}
	sub := getQueryParam(r, "path")
	entries, err := m.handler.Browser.ListDir(ws, sub)
	if err != nil {
		writeContractError(w, err)
		return
	}
	// 为可索引的文件补充实际索引状态（indexed/pending/failed 等）,不支持的保持空
	for _, e := range entries {
		if e.IsDir || !e.Indexable {
			continue
		}
		// 数据库 rel_path 用系统分隔符（Windows 为反斜杠）,浏览器返回正斜杠,需归一化
		dbPath := filepath.FromSlash(e.RelPath)
		rec, ferr := m.handler.Storage.FilesFindByRelPath(dbPath)
		if ferr == nil && rec != nil {
			e.IndexStatus = rec.IndexStatus
		}
	}
	writeOK(w, map[string]interface{}{
		"path":    sub,
		"entries": entries,
	})
}

// handleBrowseSearch GET /api/browse/search?q=xxx
// 按文件名/相对路径模糊搜索（不依赖索引,实时扫描磁盘）。
func (m *Module) handleBrowseSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	ws := m.workspacePath()
	if ws == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}
	q := getQueryParam(r, "q")
	limit := getQueryInt(r, "limit", 100)
	results, total, err := m.handler.Browser.SearchByName(ws, q, limit)
	if err != nil {
		writeContractError(w, err)
		return
	}
	writeOK(w, map[string]interface{}{
		"query": q,
		"items": results,
		"total": total,
	})
}

// handleBrowseOpen POST /api/browse/open
// 用系统默认应用打开指定相对路径的文件（资源管理器/搜索结果可操作,修复 H-05）。
func (m *Module) handleBrowseOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	ws := m.workspacePath()
	if ws == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}
	var req struct {
		RelPath string `json:"relPath"`
	}
	if err := readBody(r, &req); err != nil {
		writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
		return
	}
	if req.RelPath == "" {
		writeError(w, "bad_request", "缺少 relPath", http.StatusBadRequest)
		return
	}
	if err := m.handler.Browser.OpenFile(ws, req.RelPath); err != nil {
		writeContractError(w, err)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// handleBrowsePickDir POST /api/browse/pickdir
// 弹出系统原生目录选择对话框,返回所选路径。
func (m *Module) handleBrowsePickDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	// 可选：body 传 initial 起始目录
	initial := ""
	var body map[string]string
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body != nil {
			initial = body["initial"]
		}
	}
	path, err := m.handler.Browser.PickDirectory(initial)
	if err != nil {
		writeContractError(w, err)
		return
	}
	writeOK(w, map[string]interface{}{
		"path":      path,
		"cancelled": path == "",
	})
}
