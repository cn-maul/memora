package transport

import (
	"memora/internal/contract"
	"memora/internal/logx"
	"net/http"
	"strconv"
	"strings"
)

// handleTags GET /api/tags
func (m *Module) handleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	tags, err := m.handler.Tag.ListLibrary()
	if err != nil {
		writeContractError(w, err)
		return
	}

	writeOK(w, map[string]interface{}{"tags": tags})
}

// handleTagSuggestions GET/POST /api/tag-suggestions
func (m *Module) handleTagSuggestions(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// GET /api/tag-suggestions
	if path == "/api/tag-suggestions" && r.Method == http.MethodGet {
		suggestions, err := m.handler.Storage.SuggestionsListPending()
		if err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]interface{}{"suggestions": suggestions})
		return
	}

	// POST /api/tag-suggestions/{id}/accept
	if strings.Contains(path, "/accept") && r.Method == http.MethodPost {
		idStr := getPathParam(path, "/api/tag-suggestions/")
		idStr = strings.TrimSuffix(idStr, "/accept")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, "bad_request", "无效建议 ID", http.StatusBadRequest)
			return
		}
		if err := m.handler.Tag.AcceptSuggestion(id); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeNotFound, "建议不存在或已处理", err))
			return
		}
		writeOK(w, map[string]bool{"ok": true})
		return
	}

	// POST /api/tag-suggestions/{id}/reject
	if strings.Contains(path, "/reject") && r.Method == http.MethodPost {
		idStr := getPathParam(path, "/api/tag-suggestions/")
		idStr = strings.TrimSuffix(idStr, "/reject")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, "bad_request", "无效建议 ID", http.StatusBadRequest)
			return
		}
		if err := m.handler.Tag.RejectSuggestion(id); err != nil {
			writeContractError(w, wrapErr(contract.ErrCodeNotFound, "建议不存在或已处理", err))
			return
		}
		writeOK(w, map[string]bool{"ok": true})
		return
	}

	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleSettings GET/PUT /api/settings
func (m *Module) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, m.handler.Config.Snapshot())
	case http.MethodPut:
		// PUT /api/settings/secrets
		if strings.HasSuffix(r.URL.Path, "/secrets") {
			var req struct {
				LLMApiKey    string `json:"llmApiKey"`
				EmbedApiKey  string `json:"embedApiKey"`
				RerankApiKey string `json:"rerankApiKey"`
			}
			if !m.decodeStrictBody(w, r, &req) {
				return
			}
			if err := m.handler.Config.UpsertSecrets(req.LLMApiKey, req.EmbedApiKey, req.RerankApiKey); err != nil {
				writeContractError(w, err)
				return
			}
			writeOK(w, map[string]bool{"ok": true})
			return
		}

		// PUT /api/settings
		var req map[string]interface{}
		if !m.decodeStrictBody(w, r, &req) {
			return
		}

		// 热更新收集（修复 H-09：明确区分热更新项与需重启项）
		var newPythonPath, newCommand, newMarkitdownCmd string
		hasMarkitdown := false
		restartKeys := make(map[string]bool)
		// embed.dimensions 变更检测：若新值与旧值不同,标记需自动重建索引。
		dimChanged := false
		newDim := int64(0)

		for key, value := range req {
			// embed.dimensions 变更：先读旧值,变更则刷新索引模块维度并触发自动重建。
			// 维度变更后若仍用旧 dim 查询,cosine 全 0、检索恒为空,必须重建索引。
			if key == "embed.dimensions" {
				oldVal, _ := m.handler.Config.Get("embed.dimensions")
				var oldDim int64
				switch v := oldVal.(type) {
				case int:
					oldDim = int64(v)
				case int64:
					oldDim = v
				}
				var newDimVal int64
				switch v := value.(type) {
				case int:
					newDimVal = int64(v)
				case int64:
					newDimVal = v
				case float64:
					newDimVal = int64(v)
				}
				if oldDim != 0 && oldDim != newDimVal {
					dimChanged = true
					newDim = newDimVal
				}
			}

			if err := m.handler.Config.Set(key, value); err != nil {
				writeContractError(w, wrapErr(contract.ErrCodeBadRequest, "保存配置失败", err))
				return
			}
			// 同步 stats.enabled 到 stats 模块（运行时开关）
			if key == "stats.enabled" {
				if b, ok := value.(bool); ok {
					if err := m.handler.Stats.SetEnabled(b); err != nil {
						writeContractError(w, err)
						return
					}
				}
			}
			// 标记 MarkItDown 热更新项
			switch key {
			case "markitdown.pythonPath":
				if s, ok := value.(string); ok {
					newPythonPath = s
					hasMarkitdown = true
				}
			case "markitdown.command":
				if s, ok := value.(string); ok {
					newCommand = s
					hasMarkitdown = true
				}
			case "markitdown.markitdownCmd":
				if s, ok := value.(string); ok {
					newMarkitdownCmd = s
					hasMarkitdown = true
				}
			default:
				// 其余配置项属于需重启生效项（或已由特定模块热更新）
				// llm/embed 配置由 llm 模块每次调用实时读取（GetLLMConfig/GetEmbedConfig）,
				// 改完即生效,不算"需重启"（修复：此前误标导致提示误导小白）。
				switch key {
				case "stats.enabled", "recent.windowHours", "rerank.baseUrl", "rerank.model",
					"rerank.apiKey", "llm.baseUrl", "llm.model", "llm.temperature",
					"llm.apiKey", "embed.baseUrl", "embed.model", "embed.dimensions", "embed.apiKey":
				default:
					restartKeys[key] = true
				}
			}
		}

		// 热更新 Extract（MarkItDown）运行参数
		if hasMarkitdown && m.handler.Extract != nil {
			m.handler.Extract.ApplyConfig(newPythonPath, newCommand, newMarkitdownCmd)
		}

		// embed.dimensions 变更：刷新索引模块运行期维度,并在确有存量向量时
		// 异步触发全量重建索引（沿用 handleIndexReindex 的 fire & forget 模式）。
		reindexRequired := false
		if dimChanged && m.handler.Index != nil {
			m.handler.Index.SetEmbedDim(newDim)
			if m.handler.Storage != nil {
				if cnt, err := m.handler.Storage.VectorCount(); err == nil && cnt > 0 {
					reindexRequired = true
					logx.Info("transport", "检测到向量维度变更,后台自动重建索引",
						"oldDim", "见上一步", "newDim", newDim, "vectorCount", cnt)
					// 经队列合并执行，避免并发重建（P0-03）
					m.triggerReindex()
				}
			}
		}

		// 返回需重启生效的配置项提示,避免"假成功"
		restartList := make([]string, 0, len(restartKeys))
		for k := range restartKeys {
			restartList = append(restartList, k)
		}
		writeOK(w, map[string]interface{}{
			"ok":              true,
			"restartRequired": restartList,
			"reindexRequired": reindexRequired,
		})
	default:
		writeError(w, "bad_request", "不支持的请求方法", http.StatusBadRequest)
	}
}
