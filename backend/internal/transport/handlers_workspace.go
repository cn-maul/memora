package transport

import (
	"errors"
	"memora/internal/contract"
	"memora/internal/logx"
	"net/http"
	"os"
)

// ──────────────────── 处理器 ────────────────────

// handleWorkspaceInit POST /api/workspace/init
func (m *Module) handleWorkspaceInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}

	var req struct {
		WorkspacePath string `json:"workspacePath"`
		Markitdown    struct {
			PythonPath string `json:"pythonPath"`
			Command    string `json:"command"`
		} `json:"markitdown"`
		LLM *struct {
			BaseURL     string  `json:"baseUrl"`
			APIKey      string  `json:"apiKey"`
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
		} `json:"llm"`
		Embed struct {
			BaseURL    string `json:"baseUrl"`
			APIKey     string `json:"apiKey"`
			Model      string `json:"model"`
			Dimensions int    `json:"dimensions"`
		} `json:"embed"`
		Rerank struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Model   string `json:"model"`
		} `json:"rerank"`
	}
	if !m.decodeStrictBody(w, r, &req) {
		return
	}

	if req.WorkspacePath == "" {
		writeError(w, "bad_request", "workspacePath 不能为空", http.StatusBadRequest)
		return
	}

	// 校验工作区路径存在且为目录（M-01）
	wsInfo, err := os.Stat(req.WorkspacePath)
	if err != nil || !wsInfo.IsDir() {
		writeError(w, "bad_request", "工作区路径不存在或不是目录", http.StatusBadRequest)
		return
	}

	// ── M-01：提交配置前先做探测与模型测试,失败不得留下半初始化状态 ──
	// 1) 提取（MarkItDown）探测
	if req.Markitdown.PythonPath != "" || req.Markitdown.Command != "" {
		probePython := req.Markitdown.PythonPath
		probeCmd := req.Markitdown.Command
		if probeCmd == "" {
			probeCmd = "python -m markitdown \"{file}\""
		}
		ok, msg := m.handler.Extract.Probe(probePython, probeCmd)
		if !ok {
			writeContractError(w, wrapErr(contract.ErrCodeBadRequest, "MarkItDown 探测失败", errors.New(msg)))
			return
		}
	}
	// 2) 嵌入端点测试（若本次提供了嵌入配置）
	if req.Embed.BaseURL != "" || req.Embed.Model != "" {
		if err := m.handler.LLM.TestEmbedWith(req.Embed.BaseURL, req.Embed.APIKey, req.Embed.Model); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeBadRequest, "嵌入端点测试失败", err))
			return
		}
	}
	// 3) 聊天端点测试（若本次提供了 LLM 配置）
	if req.LLM != nil && (req.LLM.BaseURL != "" || req.LLM.Model != "") {
		if err := m.handler.LLM.TestChatWith(req.LLM.BaseURL, req.LLM.APIKey, req.LLM.Model, req.LLM.Temperature); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeBadRequest, "聊天端点测试失败", err))
			return
		}
	}

	// 1. 保存工作区路径（错误须检查,M-02）
	if err := m.handler.Config.Set("workspace.path", req.WorkspacePath); err != nil {
		writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存工作区路径失败", err))
		return
	}

	// 1.5 将 config.json 迁移到工作区 .memora/（D13）
	if err := m.handler.Config.Relocate(req.WorkspacePath); err != nil {
		writeContractError(w, wrapErr(contract.ErrCodeInternal, "迁移配置文件失败", err))
		return
	}

	// 2. 保存 markitdown 配置（错误须检查,M-02）
	if req.Markitdown.PythonPath != "" {
		if err := m.handler.Config.Set("markitdown.pythonPath", req.Markitdown.PythonPath); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 pythonPath 失败", err))
			return
		}
	}
	if req.Markitdown.Command != "" {
		if err := m.handler.Config.Set("markitdown.command", req.Markitdown.Command); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 command 失败", err))
			return
		}
	}

	// 3. 保存 LLM 配置（错误须检查,M-02）
	if req.LLM != nil {
		if req.LLM.BaseURL != "" {
			if err := m.handler.Config.Set("llm.baseUrl", req.LLM.BaseURL); err != nil {
				writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 LLM 接口失败", err))
				return
			}
		}
		if req.LLM.APIKey != "" {
			if err := m.handler.Config.UpsertSecrets(req.LLM.APIKey, "", ""); err != nil {
				writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 LLM 密钥失败", err))
				return
			}
		}
		if req.LLM.Model != "" {
			if err := m.handler.Config.Set("llm.model", req.LLM.Model); err != nil {
				writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 LLM 模型失败", err))
				return
			}
		}
		if req.LLM != nil && (req.LLM.BaseURL != "" || req.LLM.Model != "") {
			// init 请求携带完整 LLM 配置时保存 temperature（含 0,确定性输出）。
			// 仅当 LLM 有实质配置时才保存,避免用户未配置 LLM 时误覆盖现有值（review warn）
			if err := m.handler.Config.Set("llm.temperature", req.LLM.Temperature); err != nil {
				writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 LLM 温度失败", err))
				return
			}
		}
	}

	// 4. 保存 Embed 配置（错误须检查,M-02）
	if req.Embed.BaseURL != "" {
		if err := m.handler.Config.Set("embed.baseUrl", req.Embed.BaseURL); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 Embed 接口失败", err))
			return
		}
	}
	if req.Embed.APIKey != "" {
		if err := m.handler.Config.UpsertSecrets("", req.Embed.APIKey, ""); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 Embed 密钥失败", err))
			return
		}
	}
	if req.Embed.Model != "" {
		if err := m.handler.Config.Set("embed.model", req.Embed.Model); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 Embed 模型失败", err))
			return
		}
	}
	if req.Embed.Dimensions != 0 {
		if err := m.handler.Config.Set("embed.dimensions", float64(req.Embed.Dimensions)); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 Embed 维度失败", err))
			return
		}
	}

	// 4.5 保存 Rerank 配置（错误须检查）
	if req.Rerank.BaseURL != "" {
		if err := m.handler.Config.Set("rerank.baseUrl", req.Rerank.BaseURL); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 Rerank 接口失败", err))
			return
		}
	}
	if req.Rerank.APIKey != "" {
		if err := m.handler.Config.UpsertSecrets("", "", req.Rerank.APIKey); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 Rerank 密钥失败", err))
			return
		}
	}
	if req.Rerank.Model != "" {
		if err := m.handler.Config.Set("rerank.model", req.Rerank.Model); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "保存 Rerank 模型失败", err))
			return
		}
	}

	// 5. 原地重建工作区相关模块（停止旧监视、重建存储/索引/时间线/监视、
	//    更新传输层引用、确保 Git 仓库、加载向量索引并触发全量重建）。
	//    若未注入重建回调,则退化为仅初始化 Git 并触发重建（修复 B-01）。
	if m.handler.RebuildWorkspace != nil {
		if err := m.handler.RebuildWorkspace(req.WorkspacePath); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "应用工作区失败", err))
			return
		}
	} else {
		// 确保 Git 仓库已初始化
		if err := m.handler.Git.EnsureRepo(req.WorkspacePath); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeInternal, "Git 初始化失败", err))
			return
		}
		// 触发全量重建索引（经队列合并执行，P0-03）
		m.triggerReindex()
	}

	writeOK(w, map[string]bool{"ok": true})
}

// triggerReindex 触发一次全量重建索引。
// 优先走装配层注入的回调（经任务队列合并执行）；未注入时退化为直连
// Index.FullReindex 并在独立 goroutine 中执行（保持 fire & forget 语义）。
func (m *Module) triggerReindex() {
	if m.handler != nil && m.handler.TriggerReindex != nil {
		if err := m.handler.TriggerReindex(); err != nil {
			logx.Warn("transport", "触发全量重建索引失败", "err", err.Error())
		}
		return
	}
	if m.handler != nil && m.handler.Index != nil {
		go func() {
			if err := m.handler.Index.FullReindex(); err != nil {
				logx.Warn("transport", "全量重建索引警告", "err", err.Error())
			}
		}()
	}
}

// handleWorkspaceInfo GET /api/workspace/info
func (m *Module) handleWorkspaceInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	snapshot := m.handler.Config.Snapshot()
	workspacePath, _ := snapshot["workspacePath"].(string)
	initialized := workspacePath != ""

	var dirtyCounts map[string]int
	if initialized {
		status, err := m.handler.Git.Status()
		if err == nil {
			dirtyCounts = map[string]int{"modified": 0, "untracked": 0, "deleted": 0}
			for _, code := range status {
				switch code {
				case "M":
					dirtyCounts["modified"]++
				case "?", "A":
					dirtyCounts["untracked"]++
				case "D":
					dirtyCounts["deleted"]++
				}
			}
		}
	}

	// HEAD 概要
	var headInfo *contract.HeadInfo
	if initialized {
		if hi, err := m.handler.Git.Head(); err == nil {
			headInfo = hi
		}
	}

	llmCfg, _ := snapshot["llm"].(map[string]interface{})
	embedCfg, _ := snapshot["embed"].(map[string]interface{})
	// markitdown 已配置：pythonPath / markitdownCmd 任一显式配置即视为已配置。
	// command 有默认值 `python -m markitdown "{file}"`,不能作为判断依据,否则恒为 true。
	mdConfigured := false
	if md, ok := snapshot["markitdown"].(map[string]interface{}); ok {
		mdConfigured = md["pythonPath"] != "" || md["markitdownCmd"] != ""
	}

	writeOK(w, map[string]interface{}{
		"initialized":          initialized,
		"workspacePath":        workspacePath,
		"dirtyCounts":          dirtyCounts,
		"head":                 headInfo,
		"markitdownConfigured": mdConfigured,
		"llmConfigured":        llmCfg != nil && llmCfg["baseUrl"] != "",
		"embedConfigured":      embedCfg != nil && embedCfg["baseUrl"] != "",
	})
}

// workspacePath 读取当前工作区路径
func (m *Module) workspacePath() string {
	snapshot := m.handler.Config.Snapshot()
	p, _ := snapshot["workspacePath"].(string)
	return p
}
