package transport

import (
	"encoding/json"
	"errors"
	"memora/internal/contract"
	"memora/internal/logx"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// handleQASessions GET /api/qa/sessions
func (m *Module) handleQASessions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		sessions, err := m.handler.QA.Sessions()
		if err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]interface{}{"sessions": sessions})
		return
	}
	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleQA POST /api/qa
func (m *Module) handleQA(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req contract.QARequest
		if !m.decodeStrictBody(w, r, &req) {
			return
		}
		if req.Question == "" {
			writeError(w, "bad_request", "问题不能为空", http.StatusBadRequest)
			return
		}
		// 文件问答必须指定文件（修复 B-05：避免静默退化为全局问答）
		if req.Mode == "file" && req.FileID <= 0 {
			writeError(w, "bad_request", "文件问答需要先选择文件", http.StatusBadRequest)
			return
		}
		resp, err := m.handler.QA.Ask(&req)
		if err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, resp)
		return
	}

	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}

// handleQAStream POST /api/qa/stream —— 流式问答,返回 SSE
func (m *Module) handleQAStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "bad_request", "仅支持 POST", http.StatusBadRequest)
		return
	}

	var req contract.QARequest
	if !m.decodeStrictBody(w, r, &req) {
		return
	}
	if req.Question == "" {
		writeError(w, "bad_request", "问题不能为空", http.StatusBadRequest)
		return
	}
	if req.Mode == "file" && req.FileID <= 0 {
		writeError(w, "bad_request", "文件问答需要先选择文件", http.StatusBadRequest)
		return
	}

	// 设置 SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, "internal", "不支持流式", http.StatusInternalServerError)
		return
	}

	// 通过 r.Context().Done() 检测客户端断开
	cancel := make(chan struct{})
	go func() {
		<-r.Context().Done()
		close(cancel)
	}()

	chunks, done := m.handler.QA.AskStream(&req, cancel)

	for chunk := range chunks {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		// chunk 可能含真实换行（\n\n）,直接写 SSE 会破坏帧结构导致前端丢内容。
		// 用 JSON 字符串编码传输,前端解码还原。
		chunkJSON, _ := json.Marshal(chunk)
		if err := writeStreamFrame(w, flusher, "data: "+string(chunkJSON)+"\n\n", qaStreamWriteIdleTimeout); err != nil {
			// 写失败或空闲超时：断开连接退出
			if errors.Is(err, os.ErrDeadlineExceeded) {
				logx.Warn("transport", "流式问答空闲超时,断开连接")
			}
			return
		}
	}

	// 等待最终结果
	result := <-done
	if result == nil {
		// 防御：goroutine 异常退出时 done 可能关闭无值,避免 nil 解引用
		_ = writeStreamFrame(w, flusher, "event: error\ndata: \"问答中断\"\n\n", qaStreamWriteIdleTimeout)
		return
	}
	if result.Error != "" {
		// error 数据同样可能含换行,JSON 编码传输
		errJSON, _ := json.Marshal(result.Error)
		_ = writeStreamFrame(w, flusher, "event: error\ndata: "+string(errJSON)+"\n\n", qaStreamWriteIdleTimeout)
		return
	}

	// 发送结束事件（含 sessionId 和 sources）
	type finalEvent struct {
		Done      bool                `json:"done"`
		SessionID int64               `json:"sessionId,omitempty"`
		Sources   []contract.QASource `json:"sources,omitempty"`
	}
	final := finalEvent{
		Done:      true,
		SessionID: result.SessionID,
		Sources:   result.Sources,
	}
	finalJSON, _ := json.Marshal(final)
	_ = writeStreamFrame(w, flusher, "event: done\ndata: "+string(finalJSON)+"\n\n", qaStreamWriteIdleTimeout)
}

// handleQAByID GET/DELETE /api/qa/sessions/{id}
func (m *Module) handleQAByID(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// GET /api/qa/sessions/{id}/messages
	if strings.Contains(path, "/messages") && r.Method == http.MethodGet {
		idStr := getPathParam(path, "/api/qa/sessions/")
		idStr = strings.TrimSuffix(idStr, "/messages")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, "bad_request", "无效会话 ID", http.StatusBadRequest)
			return
		}
		messages, err := m.handler.Storage.QAMessagesBySession(id)
		if err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]interface{}{"messages": messages})
		return
	}

	// DELETE /api/qa/sessions/{id}
	if r.Method == http.MethodDelete {
		idStr := getPathParam(path, "/api/qa/sessions/")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, "bad_request", "无效会话 ID", http.StatusBadRequest)
			return
		}
		if err := m.handler.QA.DeleteSession(id); err != nil {
			writeContractError(w, err)
			return
		}
		writeOK(w, map[string]bool{"ok": true})
		return
	}

	writeError(w, "bad_request", "不支持的请求", http.StatusBadRequest)
}
