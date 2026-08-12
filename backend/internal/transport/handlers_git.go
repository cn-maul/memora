package transport

import (
	"fmt"
	"io"
	"memora/internal/contract"
	"net/http"
	"strings"
)

// handleCommitAuto POST /api/commits/auto
func (m *Module) handleCommitAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}

	// 先尝试用 AI 生成提交备注
	aiMsg, aiErr := m.handler.Timeline.SuggestCommitMessage()

	if aiErr == nil && aiMsg != "" {
		// AI 成功,用 AI 备注手动提交
		hash, err := m.handler.Git.CommitManual(aiMsg)
		if err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]string{"hash": hash, "message": aiMsg, "ai": "true"})
		return
	}

	// AI 不可用,回退到默认自动提交
	hash, skipped, err := m.handler.Git.CommitAuto(nil)
	if err != nil {
		writeContractError(w, err)
		return
	}

	if skipped {
		writeOK(w, map[string]bool{"skipped": true})
		return
	}
	writeOK(w, map[string]string{"hash": hash})
}

// handleCommitHead GET /api/commits/head —— 当前版本（HEAD）概要
func (m *Module) handleCommitHead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	head, err := m.handler.Git.Head()
	if err != nil {
		writeContractError(w, err)
		return
	}
	writeOK(w, head)
}

// handleCommitList GET /api/commits/list —— 提交列表（每个提交含备注、id、改动文件明细）
func (m *Module) handleCommitList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	// withFiles=true 时附带每个提交的改动文件明细（用于展开后的文件列表）；
	// 默认不附带（避免逐提交 git diff 拖慢列表加载，改由展开时按需获取）。
	withFiles := getQueryParam(r, "withFiles") == "true"
	commits, err := m.handler.Git.Log()
	if err != nil {
		writeContractError(w, err)
		return
	}

	type commitItem struct {
		Hash    string                 `json:"hash"`
		Time    int64                  `json:"time"`
		Message string                 `json:"message"`
		Author  string                 `json:"author"`
		Files   []*contract.CommitFile `json:"files,omitempty"`
	}

	items := make([]*commitItem, 0, len(commits))
	for _, c := range commits {
		item := &commitItem{
			Hash:    c.Hash,
			Time:    c.Time,
			Message: c.Message,
			Author:  c.Author,
		}
		if withFiles {
			files, err := m.handler.Git.CommitFiles(c.Hash)
			if err != nil {
				files = nil
			}
			item.Files = files
		}
		items = append(items, item)
	}

	writeOK(w, map[string]interface{}{"commits": items})
}

// handleCommitByHash GET /api/commits/{hash}/files、POST /api/commits/{hash}/summary
func (m *Module) handleCommitByHash(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// GET /api/commits/{hash}/files —— 列出该提交快照的全部文件
	if strings.HasSuffix(path, "/files") && r.Method == http.MethodGet {
		hash := getPathParam(path, "/api/commits/")
		hash = strings.TrimSuffix(hash, "/files")
		if !isHexSHA1(hash) {
			writeError(w, "bad_request", "无效提交哈希", http.StatusBadRequest)
			return
		}
		files, err := m.handler.Git.ListTreeAt(hash)
		if err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]interface{}{"hash": hash, "files": files})
		return
	}

	// GET /api/commits/{hash}/diff —— 该提交的改动文件列表（新增/修改/删除），
	// 供版本记录页展开单个提交时按需获取（避免列表接口逐提交全量 diff）。
	if strings.HasSuffix(path, "/diff") && r.Method == http.MethodGet {
		hash := getPathParam(path, "/api/commits/")
		hash = strings.TrimSuffix(hash, "/diff")
		if !isHexSHA1(hash) {
			writeError(w, "bad_request", "无效提交哈希", http.StatusBadRequest)
			return
		}
		files, err := m.handler.Git.CommitFiles(hash)
		if err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]interface{}{"hash": hash, "files": files})
		return
	}

	// GET /api/commits/{hash}/content?path=... —— 该版本中某文件的文本内容（版本预览）
	if strings.HasSuffix(path, "/content") && r.Method == http.MethodGet {
		hash := getPathParam(path, "/api/commits/")
		hash = strings.TrimSuffix(hash, "/content")
		if !isHexSHA1(hash) {
			writeError(w, "bad_request", "无效提交哈希", http.StatusBadRequest)
			return
		}
		relPath := getQueryParam(r, "path")
		if relPath == "" {
			writeError(w, "bad_request", "缺少 path 参数", http.StatusBadRequest)
			return
		}
		content, err := m.handler.Git.ShowFileAt(relPath, hash)
		if err != nil {
			writeError(w, "not_found", "该版本中不存在此文件", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, content)
		return
	}

	// POST /api/commits/{hash}/summary
	if strings.HasSuffix(path, "/summary") && r.Method == http.MethodPost {
		hash := getPathParam(path, "/api/commits/")
		hash = strings.TrimSuffix(hash, "/summary")
		if hash == "" {
			writeError(w, "bad_request", "缺少提交哈希", http.StatusBadRequest)
			return
		}
		summary, err := m.handler.Timeline.GenerateSummary(hash)
		if err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeNotFound, "该版本无法生成总结", err))
			return
		}
		writeOK(w, map[string]string{"summary": summary})
		return
	}
	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleCommitStatus GET /api/commits/status —— 列出当前未提交的变动
func (m *Module) handleCommitStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	status, err := m.handler.Git.Status()
	if err != nil {
		writeContractError(w, err)
		return
	}
	type fileStatus struct {
		RelPath string `json:"relPath"`
		Code    string `json:"code"` // M/D/A/??
	}
	files := make([]fileStatus, 0, len(status))
	for rel, code := range status {
		if code == "" || code == " " {
			continue
		}
		files = append(files, fileStatus{RelPath: rel, Code: code})
	}
	writeOK(w, map[string]any{"files": files, "count": len(files)})
}

// handleCommitManual POST /api/commits/manual —— 用户自己写备注后提交全部变动
func (m *Module) handleCommitManual(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := readBody(r, &req); err != nil {
		writeContractError(w, wrapErr(contract.ErrCodeBadRequest, "请求体解析失败", err))
		return
	}
	msg := strings.TrimSpace(req.Message)

	// 无变更时返回 skipped,不报错（前端据此提示"无变更"）
	status, err := m.handler.Git.Status()
	if err != nil {
		writeContractError(w, err)
		return
	}
	hasChanges := false
	added, modified, deleted := 0, 0, 0
	for _, code := range status {
		if code != "" && code != " " {
			hasChanges = true
		}
		switch code {
		case "A", "?":
			added++
		case "M":
			modified++
		case "D":
			deleted++
		}
	}
	if !hasChanges {
		writeOK(w, map[string]interface{}{"skipped": true, "hash": ""})
		return
	}

	// 空备注不再拦截（小白写不出备注也能保存）：自动生成统计备注
	if msg == "" {
		var parts []string
		if modified > 0 {
			parts = append(parts, fmt.Sprintf("修改 %d 个文件", modified))
		}
		if added > 0 {
			parts = append(parts, fmt.Sprintf("新增 %d 个", added))
		}
		if deleted > 0 {
			parts = append(parts, fmt.Sprintf("删除 %d 个", deleted))
		}
		msg = "手动保存：" + strings.Join(parts, "、")
	}

	hash, err := m.handler.Git.CommitManual(msg)
	if err != nil {
		writeContractError(w, err)
		return
	}
	writeOK(w, map[string]string{"hash": hash, "message": msg})
}

// handleCommitSuggest POST /api/commits/suggest —— AI 根据未提交变动生成备注建议
func (m *Module) handleCommitSuggest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	suggestion, err := m.handler.Timeline.SuggestCommitMessage()
	if err != nil {
		writeContractError(w, contract.NewAppError("ai_unavailable", "AI 服务暂不可用", http.StatusUnprocessableEntity).WithCause(err))
		return
	}
	writeOK(w, map[string]string{"suggestion": suggestion})
}
