package transport

import (
	"fmt"
	"memora/internal/contract"
	"net/http"
	"time"
)

// handleStats GET /api/stats
func (m *Module) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	if !m.handler.Stats.Enabled() {
		writeJSON(w, http.StatusOK, &Response{Code: "stats_disabled", Message: "统计已关闭", Data: map[string]bool{"enabled": false}})
		return
	}
	// 工作区未初始化时统计无数据,明确返回 not_configured 而非 500
	if m.workspacePath() == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}

	rng := getQueryParam(r, "range")
	from := getQueryInt(r, "from", 0)
	to := getQueryInt(r, "to", 0)

	metrics, err := m.handler.Stats.Summary(&contract.StatsRange{
		Range: rng,
		From:  int64(from),
		To:    int64(to),
	})
	if err != nil {
		writeContractError(w, err)
		return
	}

	writeOK(w, map[string]interface{}{
		"enabled": true,
		"metrics": metrics,
	})
}

// handleStatsExport GET /api/stats/export
func (m *Module) handleStatsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	// 统计关闭或工作区未初始化时明确提示,避免 500（修复审计发现）
	if !m.handler.Stats.Enabled() {
		writeJSON(w, http.StatusOK, &Response{Code: "stats_disabled", Message: "统计已关闭", Data: map[string]bool{"enabled": false}})
		return
	}
	if m.workspacePath() == "" {
		writeError(w, "not_configured", "工作区未初始化", http.StatusBadRequest)
		return
	}

	format := getQueryParam(r, "format")
	rng := getQueryParam(r, "range")

	content, err := m.handler.Stats.Export(format, &contract.StatsRange{Range: rng})
	if err != nil {
		writeContractError(w, err)
		return
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
	} else {
		w.Header().Set("Content-Type", "text/markdown")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=report_%d.%s", time.Now().Unix(), format))
	w.Write([]byte(content))
}
