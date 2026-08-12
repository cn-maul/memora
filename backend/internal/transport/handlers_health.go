package transport

// Phase 5 可观测性：/health（liveness）、/ready（readiness）、/diagnostics（诊断摘要）。
// 三者均为原始 JSON 输出（不带标准 {code,data} 包裹），便于编排工具直接消费。

import (
	"encoding/json"
	"net/http"
	"time"
)

// writeRawJSON 写入原始 JSON（无标准响应包裹），用于 /health、/ready、/diagnostics。
func writeRawJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// handleHealth GET /health —— liveness 探针：进程活着即 ok，永远 200，不依赖任何模块。
func (m *Module) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeRawJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyStatus /ready 响应体。
type readyStatus struct {
	Status            string   `json:"status"`
	Generation        string   `json:"generation"`
	GenerationOk      bool     `json:"generationOk"`
	GenerationChecked bool     `json:"generationChecked"`
	Storage           bool     `json:"storage"`
	Reasons           []string `json:"reasons,omitempty"`
}

// handleReady GET /ready —— readiness 探针：
//   - storage 可用（StorageAPI.Ping）
//   - 工作区已初始化（GenerationFunc 返回非空代标识）；GenerationFunc 未注入（nil）时
//     跳过 generation 检查并标注 generationChecked=false，仅以 storage 判定就绪。
//
// 就绪 → 200 {status:"ready",...}；未就绪 → 503 {status:"not_ready",...}。
func (m *Module) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	rs := readyStatus{GenerationChecked: m.handler != nil && m.handler.GenerationFunc != nil}
	if rs.GenerationChecked {
		rs.Generation = m.handler.GenerationFunc()
		if rs.Generation != "" {
			rs.GenerationOk = true
		} else {
			rs.Reasons = append(rs.Reasons, "workspace_not_initialized")
		}
	}

	if m.handler != nil && m.handler.Storage != nil {
		if err := m.handler.Storage.Ping(); err == nil {
			rs.Storage = true
		} else {
			rs.Reasons = append(rs.Reasons, "storage_unavailable")
		}
	} else {
		rs.Reasons = append(rs.Reasons, "storage_unavailable")
	}

	if rs.Storage && (rs.GenerationOk || !rs.GenerationChecked) {
		rs.Status = "ready"
		writeRawJSON(w, http.StatusOK, rs)
		return
	}
	rs.Status = "not_ready"
	writeRawJSON(w, http.StatusServiceUnavailable, rs)
}

// handleDiagnostics GET /diagnostics —— 诊断摘要：版本、generation、队列深度、
// 存储可用性、提取缓存体积、uptime、最近错误。数据全部来自 APIHandler 已有接口，
// 不新增对 app.go 的依赖。
func (m *Module) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	// 队列深度
	running, pending := 0, 0
	if m.handler != nil && m.handler.TaskQueue != nil {
		running, pending, _ = m.handler.TaskQueue.Status()
	}

	// 存储可用性
	storageOK := false
	if m.handler != nil && m.handler.Storage != nil {
		storageOK = m.handler.Storage.Ping() == nil
	}

	// 提取缓存体积：T5-C 在 extract 上新增 CacheStats 时自动生效；
	// 尚未实现时类型断言失败即静默跳过，不影响编译与其他字段。
	cacheFiles, cacheBytes := 0, int64(0)
	if m.handler != nil {
		if cs, ok := any(m.handler.Extract).(interface {
			CacheStats() (int, int64, error)
		}); ok {
			if f, b, err := cs.CacheStats(); err == nil {
				cacheFiles, cacheBytes = f, b
			}
		}
	}

	// 版本 / 代际
	version := "dev"
	if m.handler != nil && m.handler.Version != "" {
		version = m.handler.Version
	}
	generation := ""
	if m.handler != nil && m.handler.GenerationFunc != nil {
		generation = m.handler.GenerationFunc()
	}

	// 最近错误：transport 暂未接错误日志环形缓冲，先返回空数组（可后续接 logx）。
	writeRawJSON(w, http.StatusOK, map[string]interface{}{
		"version":      version,
		"generation":   generation,
		"queue":        map[string]int{"running": running, "pending": pending},
		"storage":      map[string]bool{"ok": storageOK},
		"cache":        map[string]interface{}{"files": cacheFiles, "bytes": cacheBytes},
		"uptimeSec":    int(time.Since(m.startedAt).Seconds()),
		"recentErrors": []interface{}{},
	})
}
