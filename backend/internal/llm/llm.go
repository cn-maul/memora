// Package llm 模型网关模块
// 统一封装 OpenAI 兼容端点的聊天和嵌入调用：限频/重试/密钥
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"memora/internal/contract"
	"memora/internal/credstore"
	"memora/internal/logx"
)

// 超时配置（包级变量，测试可调小以加速验证）：
// P1-11 前 Transport 依赖 Go 默认值——DialContext 无超时（TCP 建连可能永久挂）、
// ResponseHeaderTimeout 无（对端只建连不响应头时挂死），流式读侧也无 idle 上限。
var (
	dialTimeout           = 10 * time.Second // 单次 TCP 建连上限
	tlsHandshakeTimeout   = 10 * time.Second // TLS 握手上限（Go 默认即为 10s，显式声明）
	responseHeaderTimeout = 30 * time.Second // 请求写出后首字节（响应头）等待上限
	requestTimeout        = 60 * time.Second // 非流式整体超时（httpClient.Timeout + 请求 context 双保险）
	streamReadIdleTimeout = 90 * time.Second // 流式读侧 idle：对端停止推数据后中断 Read，防永久悬挂
)

// ConfigProvider LLM 配置获取接口
type ConfigProvider interface {
	GetLLMConfig() (baseURL, apiKey, model string, temperature float64)
	GetEmbedConfig() (baseURL, apiKey, model string, dimensions int)
	GetRerankConfig() (baseURL, apiKey, model string)
}

// Module 模型网关
type Module struct {
	httpClient   *http.Client // 普通请求（整体 60s 超时）
	streamClient *http.Client // 流式请求（无整体超时，仅限首字节等待，防长回答被截断）
	config       ConfigProvider
	credStore    credstore.Store // 凭据存储（DPAPI 加密），未注入时回退 config 明文

	mu          sync.Mutex
	lastReqTime time.Time
	rateLimit   time.Duration // 限频间隔（默认 50ms = 20 rps）
}

// New 创建模型网关模块
func New(cfg ConfigProvider) *Module {
	transport := &http.Transport{
		// 显式连接池：复用底层 TCP 连接，显著降低流式/嵌入请求的握手开销
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 20,
		MaxConnsPerHost:     20,
		IdleConnTimeout:     90 * time.Second,
		// P1-11 超时加固：此前以下三项依赖 Go 默认值，存在永久挂死风险
		DialContext: (&net.Dialer{
			// 单次 TCP 建连有上限，避免对端不响应 SYN/握手时 goroutine 永久阻塞
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		// 请求（含 body）写出后等待响应头的上限：对端只建连不响应头时按时返回而非挂死
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
	return &Module{
		httpClient: &http.Client{
			Timeout:   requestTimeout, // 非流式整体超时
			Transport: transport,
		},
		// 流式请求：Timeout=0 无整体超时（SSE 长回答可能超 60s）；
		// 首字节等待上限由共享 Transport 的 ResponseHeaderTimeout（30s）兜底，
		// 流式读侧 idle 上限由读取 goroutine 的 idleReadCloser 兜底（见 ChatStream）
		streamClient: &http.Client{
			Transport: transport,
		},
		config:    cfg,
		rateLimit: 50 * time.Millisecond, // 20 rps
	}
}

// SetCredStore 注入凭据存储（启动时由装配层调用），密钥优先从 credstore 读取。
func (m *Module) SetCredStore(store credstore.Store) {
	m.credStore = store
}

// credKey 解析 API 密钥：credstore 命中且非空时优先，否则回退 config 明文。
func (m *Module) credKey(service, fallback string) string {
	if m.credStore != nil {
		if v, err := m.credStore.Get(service, "api_key"); err == nil && v != "" {
			return v
		}
	}
	return fallback
}

// rateLimitWait 限频等待
func (m *Module) rateLimitWait() {
	m.mu.Lock()
	defer m.mu.Unlock()

	elapsed := time.Since(m.lastReqTime)
	if elapsed < m.rateLimit {
		time.Sleep(m.rateLimit - elapsed)
	}
	m.lastReqTime = time.Now()
}

// doRequest 发送 HTTP 请求并解析响应
func (m *Module) doRequest(method, url string, body interface{}, apiKey string) ([]byte, error) {
	var reqBody []byte
	var err error
	if body != nil {
		reqBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("[llm] 序列化请求失败: %w", err)
		}
	}

	// P1-11：每请求绑定超时 context 兜底——httpClient.Timeout（60s）之外的
	// 第二道防线，保证对端"只建连不响应头/只响应头不响应体"时也能按时返回，
	// 不依赖调用方是否有取消机制（重试/嵌入/设置页探测均走此路径）
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("[llm] 创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("[llm] 请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("[llm] 读取响应失败: %w", err)
	}

	// 错误映射
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("[llm] 429 限流（可重试）")
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("[llm] %d 服务端错误（可重试）", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("[llm] %d 客户端错误（致命）：%s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// retry 退避重试（≤3 次）
func (m *Module) retry(fn func() ([]byte, error)) ([]byte, error) {
	var lastErr error
	for i := 0; i < 3; i++ {
		m.rateLimitWait()

		data, err := fn()
		if err == nil {
			return data, nil
		}

		lastErr = err
		errStr := err.Error()

		// 4xx 不重试（致命）
		if strings.Contains(errStr, "400") || strings.Contains(errStr, "401") ||
			strings.Contains(errStr, "403") || strings.Contains(errStr, "404") {
			return nil, fmt.Errorf("[llm] 客户端错误，不重试: %w", err)
		}

		// 可重试错误：退避等待
		if i < 2 {
			wait := time.Duration(1<<uint(i)) * time.Second
			logx.Warn("llm", "重试", "attempt", i+1, "wait", wait.String(), "err", err.Error())
			time.Sleep(wait)
		}
	}
	return nil, fmt.Errorf("[llm] 重试耗尽: %w", lastErr)
}

// chatRequest 聊天请求体
type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature,omitempty"`
	MaxTokens      int           `json:"max_tokens,omitempty"`
	Stream         bool          `json:"stream,omitempty"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse 聊天响应体
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"` // 推理模型非流式的思维链（商汤/DeepSeek-R1 等）
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

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

// Chat 聊天调用
func (m *Module) Chat(system, user string, opts *contract.ChatOptions) (string, error) {
	if opts == nil {
		opts = &contract.ChatOptions{Temperature: 0.2}
	}

	baseURL, apiKey, model, defTemp := m.config.GetLLMConfig()
	apiKey = m.credKey("llm", apiKey)
	if baseURL == "" || model == "" {
		return "", fmt.Errorf("[llm] 聊天端点未配置（致命）")
	}

	temp := opts.Temperature
	if temp == 0 {
		temp = defTemp
	}

	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: temp,
	}

	if opts.MaxTokens > 0 {
		reqBody.MaxTokens = opts.MaxTokens
	}

	// 修复：JSONMode 此前被静默忽略——OpenAI 兼容端点需显式 response_format
	// 才进入严格的 JSON 输出模式（否则模型可能返回 Markdown 包裹的 JSON）
	if opts.JSONMode {
		reqBody.ResponseFormat = &struct {
			Type string `json:"type"`
		}{Type: "json_object"}
	}

	url := strings.TrimRight(baseURL, "/") + "/chat/completions"

	data, err := m.retry(func() ([]byte, error) {
		return m.doRequest("POST", url, reqBody, apiKey)
	})
	if err != nil {
		return "", err
	}

	var resp chatResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("[llm] 解析聊天响应失败: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("[llm] 聊天响应无 choices")
	}

	// 诊断日志：finish_reason 区分"被 max_tokens 截断(length)"还是"模型自然停止(stop)"
	logx.Debug("llm", "聊天响应", "finishReason", resp.Choices[0].FinishReason, "chars", len(resp.Choices[0].Message.Content))

	return resp.Choices[0].Message.Content, nil
}

// ChatStream 流式聊天调用，通过 channel 逐块返回内容，支持取消。
// 调用方读完 chunks 后必须关闭 cancel 或读完所有内容。
func (m *Module) ChatStream(system, user string, opts *contract.ChatOptions, cancel <-chan struct{}) (<-chan string, error) {
	if opts == nil {
		opts = &contract.ChatOptions{Temperature: 0.2}
	}

	baseURL, apiKey, model, defTemp := m.config.GetLLMConfig()
	apiKey = m.credKey("llm", apiKey)
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("[llm] 聊天端点未配置（致命）")
	}

	temp := opts.Temperature
	if temp == 0 {
		temp = defTemp
	}

	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: temp,
		Stream:      true,
	}
	if opts.MaxTokens > 0 {
		reqBody.MaxTokens = opts.MaxTokens
	}
	if opts.JSONMode {
		reqBody.ResponseFormat = &struct {
			Type string `json:"type"`
		}{Type: "json_object"}
	}

	bodyBytes, _ := json.Marshal(reqBody)
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"

	// 绑定取消上下文：client.Do 在 cancel 触发时中断挂起的 HTTP 请求，
	// 否则取消时连接一直挂着直到响应（修复 review should-fix）。
	// 注意：cancelReq 不能在 ChatStream 返回时立即触发（defer），否则响应体流尚未读完
	// 就被 context canceled 打断——流式输出会被截断（修复：由下方读取 goroutine 在自己结束时释放）。
	reqCtx, cancelReq := context.WithCancel(context.Background())
	go func() {
		select {
		case <-cancel:
			cancelReq()
		case <-reqCtx.Done():
		}
	}()

	// 修复：流式请求此前无任何重试——网络抖动/429/5xx 直接失败。
	// 流式一旦开始就无法重放，故只在"连接建立阶段"重试（≤3 次，退避等待）。
	var resp *http.Response
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case <-cancel:
			cancelReq()
			return nil, fmt.Errorf("[llm] 流式请求已取消")
		default:
		}

		req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			cancelReq()
			return nil, fmt.Errorf("[llm] 创建流式请求失败: %w", err)
		}
		req = req.WithContext(reqCtx) // 绑定取消上下文，client.Do 可被 cancel 中断
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		m.rateLimitWait()

		// 流式请求用 streamClient（无整体超时，长回答不被截断）
		resp, err = m.streamClient.Do(req)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			break // 连接成功（仅 2xx），进入流式读取
		}

		var statusCode int
		if resp != nil {
			errBody, _ := io.ReadAll(resp.Body)
			statusCode = resp.StatusCode
			resp.Body.Close()
			lastErr = fmt.Errorf("[llm] 流式请求 %d: %s", resp.StatusCode, string(errBody))
		} else {
			lastErr = fmt.Errorf("[llm] 流式请求失败: %w", err)
		}
		resp = nil

		// 4xx（除 429）为致命错误，不重试
		if statusCode >= 400 && statusCode < 500 && statusCode != http.StatusTooManyRequests {
			cancelReq()
			return nil, lastErr
		}

		if attempt < 2 {
			wait := time.Duration(1<<uint(attempt)) * time.Second
			logx.Warn("llm", "流式请求重试", "attempt", attempt+1, "wait", wait.String(), "err", lastErr.Error())
			select {
			case <-time.After(wait):
			case <-cancel:
				cancelReq()
				return nil, fmt.Errorf("[llm] 流式请求已取消")
			}
		}
	}
	if resp == nil {
		cancelReq()
		return nil, fmt.Errorf("[llm] 流式请求重试耗尽: %w", lastErr)
	}

	ch := make(chan string, 16)
	go func() {
		defer cancelReq() // 流结束时释放请求上下文（同时让上方 select goroutine 退出）
		defer resp.Body.Close()

		// P1-11：取消监视——cancel 触发时显式关闭响应体，使阻塞在 resp.Body.Read
		// 的读循环立即解除阻塞（仅靠 context 取消在大多数情况下也能中断读，
		// 显式 Close 是兜底保证，避免流式 goroutine 永久悬挂）。
		// stopWatch 在本 goroutine 结束时关闭，防止监视 goroutine 泄漏。
		stopWatch := make(chan struct{})
		go func() {
			select {
			case <-cancel:
				resp.Body.Close()
			case <-stopWatch:
			}
		}()
		defer close(stopWatch)

		// P1-11：流式读侧 idle 超时——对端连接建立后停止推数据（SSE 断流但连接未关）时，
		// ResponseHeaderTimeout 管不到 body 读。这里包装响应体（见 idleReadCloser）：
		// 底层 Read 持续空闲超过 streamReadIdleTimeout 后关闭底层 body 使阻塞的 Read
		// 返回错误，避免流式读永久悬挂。每次成功 Read 都重置空闲窗口，正常推流不受影响。
		// 包装后取消监视与 defer Close 仍生效（包装体 Close 委托给底层 body）。
		resp.Body = newIdleReadCloser(resp.Body, streamReadIdleTimeout)

		if resp.StatusCode >= 400 {
			errBody, _ := io.ReadAll(resp.Body)
			errMsg := fmt.Sprintf("[llm] 流式请求 %d: %s", resp.StatusCode, string(errBody))
			select {
			case ch <- "__ERROR__:" + errMsg:
			case <-cancel:
			}
			close(ch)
			return
		}

		// 兼容忽略 stream:true 的端点（如 tokenrhythm）：返回 Content-Type 非
		// text/event-stream（普通 JSON）时，整体解析为一条完整内容发出。
		// 修复：此前这类端点流式恒空，只能等非流式重试（慢且首字不可见）。
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
			body, _ := io.ReadAll(resp.Body)
			var plain chatResponse
			if err := json.Unmarshal(body, &plain); err == nil && len(plain.Choices) > 0 {
				if content := plain.Choices[0].Message.Content; content != "" {
					select {
					case ch <- content:
					case <-cancel:
					}
				} else if think := plain.Choices[0].Message.Reasoning; think != "" {
					// 推理模型非流式响应只含思维链、无最终内容时，至少把思考过程发出去
					select {
					case ch <- contract.ThinkChunkPrefix + think:
					case <-cancel:
					}
				}
			} else {
				// 场景：端点实际是流式（SSE 行）但 Content-Type 标注错误（如 application/json）。
				// 此前 io.ReadAll 等完整流结束后整体 JSON 解析必然失败，流式内容被整段丢弃。
				// 修复：整体内容按 SSE 行重新走 handleStreamLine 解析，至少恢复完整回答。
				var fb string
				received := false
				sent := false
				for _, l := range strings.Split(string(body), "\n") {
					l = strings.TrimSpace(l)
					if !strings.HasPrefix(l, "data: ") {
						continue
					}
					if strings.TrimPrefix(l, "data: ") == "[DONE]" {
						break
					}
					s, err := m.handleStreamLine(l, ch, cancel, &received, &fb)
					if err != nil {
						break
					}
					// sent 需覆盖思考块（带前缀）与内容块，否则纯思考流会被误判"解析失败"
					sent = sent || s
				}
				if !received && fb != "" {
					select {
					case ch <- fb:
						sent = true
					case <-cancel:
					}
				}
				if !sent {
					logx.Warn("llm", "流式端点返回非 SSE 且解析失败", "contentType", ct, "bodyLen", len(body))
				}
			}
			close(ch)
			return
		}

		reader := bufio.NewReader(resp.Body)
		// receivedDelta 标记是否已收到任何增量内容：用于 message 回退互斥（review 发现：
		// 非标准端点"首块 message 发完整内容 + 后续 delta 增量"若两者都取会重复拼接）
		receivedDelta := false
		// messageFallback 保存首个非增量块的完整 message 内容（仅当整条流无 delta 时使用）
		var messageFallback string
		// rawSample 记录首个 data 行原文（诊断：流式恒空时看端点到底发的是什么格式）
		rawSample := ""

		readLoop := func() {
			for {
				select {
				case <-cancel:
					return
				default:
				}

				line, err := reader.ReadString('\n')
				if err != nil {
					if err != io.EOF {
						// 非 EOF 读取错误（连接中断/被取消）：此前静默丢弃，导致流式回答在
						// 中途被截断却无任何痕迹。记录日志便于排查（修复测试发现）。
						logx.Warn("llm", "流式读取中断", "err", err.Error())
					}
					if err == io.EOF {
						line = strings.TrimSpace(line)
						// EOF 时可能带最后一个 data 行（无尾随换行），处理后再结束
						if line != "" {
							if rawSample == "" && strings.HasPrefix(line, "data: ") {
								rawSample = clipSample(line)
							}
							if _, err := m.handleStreamLine(line, ch, cancel, &receivedDelta, &messageFallback); err != nil {
								return
							}
						}
					}
					return
				}

				line = strings.TrimSpace(line)
				if line == "" || !strings.HasPrefix(line, "data: ") {
					continue
				}
				if rawSample == "" {
					rawSample = clipSample(line)
				}
				if _, err := m.handleStreamLine(line, ch, cancel, &receivedDelta, &messageFallback); err != nil {
					return
				}
			}
		}

		// 读流 + 回退补发统一在当前 goroutine：结束点唯一，避免重复 close(ch)
		readLoop()
		// 若整条流未收到任何 delta 但存在 message 回退内容，补发一次
		if messageFallback != "" && !receivedDelta {
			select {
			case ch <- messageFallback:
			case <-cancel:
			}
		}
		// 诊断日志：确认流是否真的收到内容（"流式回答为空"时看 rawSample 判断端点格式）
		logx.Debug("llm", "流式结束", "receivedDelta", receivedDelta, "hasFallback", messageFallback != "", "fallbackChars", len(messageFallback), "rawSample", rawSample)
		close(ch)
	}()

	return ch, nil
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

// errStreamDone 流式读取终止信号（取消或 [DONE]）
var errStreamDone = errors.New("[llm] 流式读取结束")

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

// ChatJSON 聊天调用并解析 JSON 响应
func (m *Module) ChatJSON(system, user, schemaDesc string, result interface{}) error {
	content, err := m.Chat(system, user, &contract.ChatOptions{JSONMode: true, Temperature: 0.2})
	if err != nil {
		return err
	}

	if err := json.Unmarshal([]byte(content), result); err != nil {
		return fmt.Errorf("[llm] 解析 JSON 响应失败: %w\n原文: %s", err, content)
	}
	return nil
}

// embedRequest 嵌入请求体
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
	// Dimensions 指定输出维度（Qwen3-Embedding 等支持 Matryoshka 裁剪）。
	// 不传时模型返回默认维度（如 Qwen3-Embedding-8B 为 4096），
	// 与配置的 embed.dimensions（默认 1024）不一致会导致索引校验失败、问答检索不到任何文件（修复）。
	Dimensions int `json:"dimensions,omitempty"`
}

// embedResponse 嵌入响应体
type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage *struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

// Embed 批量嵌入（每批 ≤16 段）
func (m *Module) Embed(texts []string) ([][]float32, error) {
	baseURL, apiKey, model, dims := m.config.GetEmbedConfig()
	apiKey = m.credKey("embed", apiKey)
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("[llm] 嵌入端点未配置（致命）")
	}

	url := strings.TrimRight(baseURL, "/") + "/embeddings"
	batchSize := 16

	var allVectors [][]float32

	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		reqBody := embedRequest{
			Model:      model,
			Input:      batch,
			Dimensions: dims, // 显式指定维度，避免模型默认维度与配置不一致
		}

		data, err := m.retry(func() ([]byte, error) {
			return m.doRequest("POST", url, reqBody, apiKey)
		})
		if err != nil {
			return nil, err
		}

		var resp embedResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("[llm] 解析嵌入响应失败: %w", err)
		}

		// 按 data[].index 归位：OpenAI 兼容端点的 data[] 顺序不保证与 input 一致，
		// 直接顺序 append 会导致向量与文本错位（修复审计发现）。
		batchVecs := make([][]float32, len(batch))
		for _, d := range resp.Data {
			if d.Index < 0 || d.Index >= len(batchVecs) {
				return nil, fmt.Errorf("[llm] 嵌入响应 index %d 越界", d.Index)
			}
			vec := make([]float32, len(d.Embedding))
			for j, v := range d.Embedding {
				vec[j] = float32(v)
			}
			batchVecs[d.Index] = vec
		}
		// 校验每段文本都有向量，缺失则报错而不是静默丢向量（修复审计发现）
		for i, v := range batchVecs {
			if v == nil {
				return nil, fmt.Errorf("[llm] 嵌入响应缺少第 %d 段向量", i)
			}
		}
		allVectors = append(allVectors, batchVecs...)
	}

	return allVectors, nil
}

// retrievalInstruction 检索查询指令前缀（Qwen3-Embedding / bge 官方均建议：
// 查询侧加上"为检索生成表示"指令，文档侧不加，可显著提升查询-文档相似度）
const retrievalInstruction = "为这个句子生成表示以用于检索相关文章："

// EmbedQuery 嵌入单条查询（自动加检索指令前缀）。
// 文档索引用 Embed（原文），查询用 EmbedQuery（带指令），二者维度一致、方向对齐。
func (m *Module) EmbedQuery(text string) ([]float32, error) {
	vecs, err := m.Embed([]string{retrievalInstruction + text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("[llm] 嵌入查询返回空")
	}
	return vecs[0], nil
}

// TestChat 测试聊天端点（使用已保存配置）
func (m *Module) TestChat() error {
	_, err := m.Chat("你是一个助手。", "回复ok即可。", &contract.ChatOptions{MaxTokens: 10, Temperature: 0.1})
	return err
}

// TestEmbed 测试嵌入端点（使用已保存配置）
func (m *Module) TestEmbed() error {
	_, err := m.Embed([]string{"测试"})
	return err
}

// TestChatWith 用临时配置测试聊天端点，不依赖已保存配置。
// 当未传入 apiKey 时回退到已保存的密钥，避免空密钥导致 401。
func (m *Module) TestChatWith(baseURL, apiKey, model string, temperature float64) error {
	if baseURL == "" || model == "" {
		return fmt.Errorf("[llm] 请先填写聊天接口地址和模型")
	}
	if apiKey == "" {
		_, savedKey, _, _ := m.config.GetLLMConfig()
		apiKey = m.credKey("llm", savedKey)
	}
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: "你是一个助手。"},
			{Role: "user", Content: "回复ok即可。"},
		},
		Temperature: temperature,
		MaxTokens:   10,
	}
	data, err := m.retry(func() ([]byte, error) {
		return m.doRequest("POST", url, reqBody, apiKey)
	})
	if err != nil {
		return err
	}
	var resp chatResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("[llm] 解析聊天响应失败: %w", err)
	}
	if len(resp.Choices) == 0 {
		return fmt.Errorf("[llm] 聊天响应无 choices")
	}
	return nil
}

// TestEmbedWith 用临时配置测试嵌入端点，不依赖已保存配置。
// 当未传入 apiKey 时回退到已保存的密钥，避免空密钥导致 401。
func (m *Module) TestEmbedWith(baseURL, apiKey, model string) error {
	if baseURL == "" || model == "" {
		return fmt.Errorf("[llm] 请先填写嵌入接口地址和模型")
	}
	if apiKey == "" {
		_, savedKey, _, _ := m.config.GetEmbedConfig()
		apiKey = m.credKey("embed", savedKey)
	}
	url := strings.TrimRight(baseURL, "/") + "/embeddings"
	reqBody := embedRequest{Model: model, Input: []string{"测试"}}
	data, err := m.retry(func() ([]byte, error) {
		return m.doRequest("POST", url, reqBody, apiKey)
	})
	if err != nil {
		return err
	}
	var resp embedResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("[llm] 解析嵌入响应失败: %w", err)
	}
	if len(resp.Data) == 0 {
		return fmt.Errorf("[llm] 嵌入响应无数据")
	}
	return nil
}

// ListModels 列出端点支持的模型（GET /models，OpenAI 兼容）。
// kind 用于回退正确的已保存密钥（chat→LLM、embed→嵌入、rerank→重排），
// 修复：此前统一回退 LLM 密钥，在嵌入/重排端点（如 SiliconFlow）上会 401 误报。
// 供设置页「获取模型」按钮使用：拿到列表后模型字段从下拉中选择。
func (m *Module) ListModels(kind, baseURL, apiKey string) ([]string, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("[llm] 请先填写接口地址")
	}
	if apiKey == "" {
		switch kind {
		case "embed":
			_, savedKey, _, _ := m.config.GetEmbedConfig()
			apiKey = m.credKey("embed", savedKey)
		case "rerank":
			_, savedKey, _ := m.config.GetRerankConfig()
			apiKey = m.credKey("rerank", savedKey)
		default:
			_, savedKey, _, _ := m.config.GetLLMConfig()
			apiKey = m.credKey("llm", savedKey)
		}
	}

	url := strings.TrimRight(baseURL, "/") + "/models"

	data, err := m.doRequest("GET", url, nil, apiKey)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("[llm] 解析模型列表失败: %w", err)
	}
	models := make([]string, 0, len(resp.Data))
	for _, d := range resp.Data {
		if d.ID != "" {
			models = append(models, d.ID)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("[llm] 该端点未返回可用模型")
	}
	return models, nil
}

// ──────────────────── 重排（Rerank）────────────────────

// rerankRequest 重排请求体（Jina / Cohere / SiliconFlow bge-reranker 兼容）
type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

// rerankResponse 重排响应体
type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// Rerank 用交叉编码器对 (query, documents) 逐对打分，返回与 documents 顺序一致的分数。
// 端点未配置时返回错误，调用方据此回退到向量相似度排序（重排是可选增强，非硬依赖）。
func (m *Module) Rerank(query string, documents []string) ([]float64, error) {
	baseURL, apiKey, model := m.config.GetRerankConfig()
	apiKey = m.credKey("rerank", apiKey)
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("[llm] 重排端点未配置")
	}
	if len(documents) == 0 {
		return []float64{}, nil
	}
	logx.Debug("llm", "重排开始", "candidates", len(documents))

	url := strings.TrimRight(baseURL, "/") + "/rerank"
	const batchSize = 32 // 交叉编码器有数量上限，分批调用
	scores := make([]float64, len(documents))

	for i := 0; i < len(documents); i += batchSize {
		end := i + batchSize
		if end > len(documents) {
			end = len(documents)
		}
		// 截断单条文档：重排只关心相关性信号，超长文本会增大请求体与超限风险
		batch := make([]string, 0, end-i)
		for _, doc := range documents[i:end] {
			batch = append(batch, truncateRunes(doc, maxRerankRunes))
		}

		reqBody := rerankRequest{Model: model, Query: query, Documents: batch}
		data, err := m.retry(func() ([]byte, error) {
			return m.doRequest("POST", url, reqBody, apiKey)
		})
		if err != nil {
			logx.Warn("llm", "重排失败，调用方回退向量分数", "err", err.Error())
			return nil, fmt.Errorf("[llm] 重排请求失败: %w", err)
		}

		var resp rerankResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			logx.Warn("llm", "重排响应解析失败", "err", err.Error())
			return nil, fmt.Errorf("[llm] 解析重排响应失败: %w", err)
		}

		// 按 results[].index 归位（index 为批次内下标）
		found := 0
		for _, r := range resp.Results {
			if r.Index < 0 || r.Index >= len(batch) {
				continue
			}
			scores[i+r.Index] = r.RelevanceScore
			found++
		}
		if found < len(batch) {
			// 缺失分数会导致错位，宁可报错让调用方回退
			logx.Warn("llm", "重排响应缺少分数，回退向量分数", "want", len(batch), "got", found)
			return nil, fmt.Errorf("[llm] 重排响应缺少 %d 条文档分数", len(batch)-found)
		}
	}

	return scores, nil
}

// maxRerankRunes 重排单条文档截断上限（按 rune）：
// 交叉编码器只取相关性信号，超长文本没必要完整送入，截断可显著降低请求体与超限概率。
const maxRerankRunes = 800

// truncateRunes 按 rune 截断文本，避免切碎多字节字符
func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

// TestRerankWith 用临时配置测试重排端点，不依赖已保存配置。
func (m *Module) TestRerankWith(baseURL, apiKey, model string) error {
	if baseURL == "" || model == "" {
		return fmt.Errorf("[llm] 请先填写重排接口地址和模型")
	}
	if apiKey == "" {
		_, savedKey, _ := m.config.GetRerankConfig()
		apiKey = m.credKey("rerank", savedKey)
	}
	url := strings.TrimRight(baseURL, "/") + "/rerank"
	reqBody := rerankRequest{Model: model, Query: "测试", Documents: []string{"测试文档一", "测试文档二"}}
	data, err := m.retry(func() ([]byte, error) {
		return m.doRequest("POST", url, reqBody, apiKey)
	})
	if err != nil {
		return err
	}
	var resp rerankResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("[llm] 解析重排响应失败: %w", err)
	}
	if len(resp.Results) == 0 {
		return fmt.Errorf("[llm] 重排响应无结果")
	}
	return nil
}
