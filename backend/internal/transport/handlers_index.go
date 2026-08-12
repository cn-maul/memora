package transport

import (
	"net/http"
)

// handleIndexReindex POST /api/index/reindex
// 触发全量重建索引（异步执行,返回立即）。
func (m *Module) handleIndexReindex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	ws := m.workspacePath()
	if ws == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}
	// 经任务队列合并执行，避免并发重建（P0-03）
	m.triggerReindex()
	writeOK(w, map[string]bool{"ok": true})
}
