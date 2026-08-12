package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"memora/internal/contract"
	"memora/internal/logx"
)

// streamResponse 流式聊天响应体
// delta 为流式增量（标准 OpenAI 格式）；部分兼容端点首块用 message 字段承载完整内容，
// 或并发返回多个 choice，故均做兼容处理。
// Reasoning 为推理模型的思维链增量（delta.reasoning，如 SenseNova reasoning 系列）：
// 这类模型先整段输出思维链、最后才输出 delta.content，若不解析则思考期流式无任何输出。
type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// handleStreamLine 解析一行 SSE data，输出 delta 增量（内容或思考过程）或暂存 message 回退内容。
// 返回 sent 表示本行是否向 ch 发出了内容（含带前缀的思考块）；error 表示应终止读取（取消或解析终止）。
func (m *Module) handleStreamLine(line string, ch chan<- string, cancel <-chan struct{}, receivedDelta *bool, messageFallback *string) (bool, error) {
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		return false, errStreamDone
	}

	var streamResp streamResponse
	if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
		// 解析失败：记录便于排查（此前静默丢弃导致"空回答"无法诊断）
		logx.Debug("llm", "流式行解析失败", "line", data)
		return false, nil
	}

	sent := false
	for _, choice := range streamResp.Choices {
		delta := choice.Delta.Content
		if delta != "" {
			*receivedDelta = true
			sent = true
			select {
			case ch <- delta:
			case <-cancel:
				return sent, errStreamDone
			}
		} else if think := choice.Delta.Reasoning; think != "" {
			// 推理模型思维链（delta.reasoning）：带前缀标记发出，前端单独渲染"思考过程"。
			// 修复：此前未解析，整段思考期流式无输出，用户误以为对话卡死/未流式。
			// 注意不计入 receivedDelta / messageFallback，最终回答仍以 delta.content 为准。
			sent = true
			select {
			case ch <- contract.ThinkChunkPrefix + think:
			case <-cancel:
				return sent, errStreamDone
			}
		} else if msg := choice.Message.Content; msg != "" && !*receivedDelta && *messageFallback == "" {
			// 非标准端点：整条流首块用 message 承载完整内容。
			// 仅在未收到任何 delta 时暂存，流结束时若仍无 delta 则整体输出。
			*messageFallback = msg
		}
	}
	return sent, nil
}

// clipSample 截断原始流行用于诊断日志（保留前 300 字符）
func clipSample(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// idleReadCloser 包装流式响应体，提供读侧 idle 超时：
// 底层 Read 持续空闲超过 idle 后被中断（关闭底层 body 使阻塞的 Read 返回错误），
// 避免对端断流但连接未关（SSE 停推）时流式 goroutine 永久悬挂。
// 每次读取都开启独立的空闲窗口，正常推流（数据持续到达）不受影响。
type idleReadCloser struct {
	body io.ReadCloser
	idle time.Duration
}

func newIdleReadCloser(body io.ReadCloser, idle time.Duration) io.ReadCloser {
	return &idleReadCloser{body: body, idle: idle}
}

func (b *idleReadCloser) Read(p []byte) (int, error) {
	type readResult struct {
		n   int
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		n, err := b.body.Read(p)
		ch <- readResult{n, err}
	}()

	timer := time.NewTimer(b.idle)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.n, r.err
	case <-timer.C:
		// 数据恰好在超时瞬间到达则不中断，避免误杀正常推流
		select {
		case r := <-ch:
			return r.n, r.err
		default:
		}
		b.body.Close() // 关闭底层 body 使阻塞的 Read 返回，解除读阻塞
		<-ch           // 等待被中断的 Read 退出，避免 goroutine 泄漏
		return 0, fmt.Errorf("[llm] 流式读取空闲超时")
	}
}

func (b *idleReadCloser) Close() error {
	return b.body.Close()
}
