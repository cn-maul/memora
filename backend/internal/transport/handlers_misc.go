package transport

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// handleStatic 托管前端静态资源 + SPA 回退到 index.html。
// 优先磁盘目录（webDir,如 MEMORA_WEB）,否则用内嵌文件系统（webFS）。
func (m *Module) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// /api 未匹配到的路径视为 API 404,不落入静态目录
	if path == "/api" || strings.HasPrefix(path, "/api/") {
		http.NotFound(w, r)
		return
	}

	// 归一化 + 防目录穿越
	rel := strings.TrimPrefix(path, "/")
	if rel == "" {
		rel = "index.html"
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "/") {
		http.NotFound(w, r)
		return
	}

	// 磁盘目录（MEMORA_WEB 等外部目录）
	if m.webDir != "" {
		full := filepath.Join(m.webDir, cleaned)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			// SPA 回退
			http.ServeFile(w, r, filepath.Join(m.webDir, "index.html"))
			return
		}
		http.ServeFile(w, r, full)
		return
	}

	// 内嵌文件系统（go:embed 的 frontend/dist）
	if m.webFS != nil {
		if serveEmbedded(w, r, m.webFS, cleaned) {
			return
		}
		serveEmbedded(w, r, m.webFS, "index.html") // SPA 回退
		return
	}

	http.NotFound(w, r)
}

// serveEmbedded 从内嵌文件系统提供单个文件,返回是否成功写出。
func serveEmbedded(w http.ResponseWriter, r *http.Request, webFS fs.FS, name string) bool {
	// 先确认存在且是文件（ServeFileFS 对目录会 302 重定向,干扰 SPA 回退判断）
	info, err := fs.Stat(webFS, name)
	if err != nil || info.IsDir() {
		return false
	}
	http.ServeFileFS(w, r, webFS, name)
	return true
}

// handleQueueStatus GET /api/queue/status 任务队列状态
func (m *Module) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}
	if m.handler.TaskQueue == nil {
		writeError(w, "not_ready", "任务队列未就绪", http.StatusServiceUnavailable)
		return
	}
	running, pending, paused := m.handler.TaskQueue.Status()
	writeOK(w, map[string]interface{}{
		"running": running,
		"pending": pending,
		"paused":  paused,
	})
}

// handleQueuePause POST /api/queue/pause 暂停任务队列
func (m *Module) handleQueuePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	if m.handler.TaskQueue == nil {
		writeError(w, "not_ready", "任务队列未就绪", http.StatusServiceUnavailable)
		return
	}
	if err := m.handler.TaskQueue.Pause(); err != nil {
		writeContractError(w, err)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// handleQueueResume POST /api/queue/resume 恢复任务队列
func (m *Module) handleQueueResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}
	if m.handler.TaskQueue == nil {
		writeError(w, "not_ready", "任务队列未就绪", http.StatusServiceUnavailable)
		return
	}
	if err := m.handler.TaskQueue.Resume(); err != nil {
		writeContractError(w, err)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// handleTest POST /api/test/{type}
func (m *Module) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}

	path := r.URL.Path

	// POST /api/test/markitdown
	if strings.HasSuffix(path, "/markitdown") {
		var req struct {
			PythonPath string `json:"pythonPath"`
			Command    string `json:"command"`
		}
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}
		ok, msg := m.handler.Extract.Probe(req.PythonPath, req.Command)
		writeOK(w, map[string]interface{}{"ok": ok, "message": msg})
		return
	}

	// POST /api/test/llm
	if strings.HasSuffix(path, "/llm") {
		var req struct {
			Type        string  `json:"type"` // chat|embed|models
			Kind        string  `json:"kind"` // models 时区分 chat/embed/rerank,用于回退正确密钥
			BaseURL     string  `json:"baseUrl"`
			Model       string  `json:"model"`
			ApiKey      string  `json:"apiKey"`
			Temperature float64 `json:"temperature"`
		}
		if err := readBody(r, &req); err != nil {
			writeError(w, "bad_request", "请求体解析失败", http.StatusBadRequest)
			return
		}
		var err error
		switch req.Type {
		case "models":
			models, lerr := m.handler.LLM.ListModels(req.Kind, req.BaseURL, req.ApiKey)
			if lerr != nil {
				writeOK(w, map[string]interface{}{"ok": false, "message": lerr.Error()})
				return
			}
			writeOK(w, map[string]interface{}{"ok": true, "models": models})
			return
		case "embed":
			err = m.handler.LLM.TestEmbedWith(req.BaseURL, req.ApiKey, req.Model)
		case "rerank":
			err = m.handler.LLM.TestRerankWith(req.BaseURL, req.ApiKey, req.Model)
		default:
			err = m.handler.LLM.TestChatWith(req.BaseURL, req.ApiKey, req.Model, req.Temperature)
		}
		if err != nil {
			writeOK(w, map[string]interface{}{"ok": false, "message": err.Error()})
			return
		}
		writeOK(w, map[string]interface{}{"ok": true, "message": "测试通过"})
		return
	}

	writeError(w, "bad_request", "不支持的测试类型", http.StatusBadRequest)
}

// handleDetectPython GET /api/python/detect —— 尝试检测系统中 Python 解释器并回显版本
// 同时检测 markitdown 可执行文件路径。
func (m *Module) handleDetectPython(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "bad_request", "仅支持 GET", http.StatusBadRequest)
		return
	}

	type pyResult struct {
		Path          string `json:"path"`
		Ok            bool   `json:"ok"`
		Version       string `json:"version,omitempty"`
		MarkitdownCmd string `json:"markitdownCmd,omitempty"`
		Error         string `json:"error,omitempty"`
	}
	found := pyResult{}

	// 所有候选：优先真实 Python 安装目录,跳过 WindowsApps Store 壳
	candidates := []string{}

	// Windows 常见路径
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Python", "bin", "python.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Python", "pythoncore-3.14-64", "python.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Python", "pythoncore-3.13-64", "python.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Python", "pythoncore-3.12-64", "python.exe"),
			`C:\Python314\python.exe`,
			`C:\Python313\python.exe`,
			`C:\Python312\python.exe`,
			`C:\Python311\python.exe`,
			`C:\Python310\python.exe`,
			`C:\Python39\python.exe`,
		)
	}
	// PATH 里的可执行名（最后兜底,因为可能找到 WindowsApps 壳）
	candidates = append(candidates, "python", "python3", "py")

	for _, c := range candidates {
		var pyExe string
		if filepath.IsAbs(c) {
			pyExe = c
		} else {
			p, err := exec.LookPath(c)
			if err != nil {
				continue
			}
			// 跳过 WindowsApps 下的 Store 壳
			if runtime.GOOS == "windows" && strings.Contains(strings.ToLower(p), "windowsapps") {
				continue
			}
			pyExe = p
		}

		if _, err := os.Stat(pyExe); err != nil {
			continue
		}
		ver := probeVersion(pyExe)
		if ver == "" {
			continue
		}
		found.Path = pyExe
		found.Ok = true
		found.Version = ver

		// 按 python.exe 路径推导 markitdown 路径
		pyDir := filepath.Dir(pyExe)
		scriptsDir := pyDir
		// 如果 python.exe 在 bin/ 下,Python 根目录在上一级
		if filepath.Base(pyDir) == "bin" {
			scriptsDir = filepath.Dir(pyDir)
		}
		// 如果 Scripts 不在 python.exe 同目录,尝试 pythoncore 子目录
		var mdCandidates []string
		if filepath.Base(pyDir) == "bin" {
			// bin/ 下无 Scripts,尝试 pythoncore-<ver>-64/Scripts
			mdCandidates = append(mdCandidates, filepath.Join(scriptsDir, "pythoncore-3.14-64", "Scripts", "markitdown.exe"))
			mdCandidates = append(mdCandidates, filepath.Join(scriptsDir, "pythoncore-3.13-64", "Scripts", "markitdown.exe"))
			mdCandidates = append(mdCandidates, filepath.Join(scriptsDir, "pythoncore-3.12-64", "Scripts", "markitdown.exe"))
		}
		mdCandidates = append(mdCandidates,
			filepath.Join(scriptsDir, "Scripts", "markitdown.exe"),
			filepath.Join(pyDir, "Scripts", "markitdown.exe"),
			filepath.Join(scriptsDir, "markitdown.exe"),
		)
		for _, md := range mdCandidates {
			if _, err := os.Stat(md); err == nil {
				found.MarkitdownCmd = md
				break
			}
		}
		break
	}

	if !found.Ok {
		found.Error = "未找到可用的 Python 解释器"
	}

	writeOK(w, map[string]interface{}{"results": []pyResult{found}})
}

// probeVersion 运行 --version 返回简化版本号（如 "3.12.2"）或空字符串
func probeVersion(pythonExe string) string {
	cmd := exec.Command(pythonExe, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	ver := strings.TrimSpace(string(out))
	ver = strings.TrimPrefix(ver, "Python ")
	ver = strings.TrimPrefix(ver, "python ")
	return ver
}

func runtimeGOOS() string {
	return runtime.GOOS
}

// Addr 返回监听地址
func (m *Module) Addr() string {
	return m.addr
}

// Stop 优雅停止服务（P0-04 关闭顺序第一步）：
// 先 http.Server.Shutdown 等待活动请求自然结束（最多 5s），超时则 force Close。
func (m *Module) Stop() error {
	if m.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.server.Shutdown(ctx); err != nil {
		return m.server.Close()
	}
	return nil
}
