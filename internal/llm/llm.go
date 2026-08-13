// Package llm 模型网关模块
// 统一封装 OpenAI 兼容端点的聊天和嵌入调用：限频/重试/密钥
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"memora/internal/contract"
	"memora/internal/credstore"
	"memora/internal/logx"
)

// rateLimiter 按服务独立限频器，互不阻塞（A4：重建索引时嵌入流量不再挤占聊天）
type rateLimiter struct {
	mu          sync.Mutex
	lastReqTime time.Time
	rate        time.Duration
}

func (r *rateLimiter) wait() {
	r.mu.Lock()
	defer r.mu.Unlock()
	elapsed := time.Since(r.lastReqTime)
	if elapsed < r.rate {
		time.Sleep(r.rate - elapsed)
	}
	r.lastReqTime = time.Now()
}

// Module 模型网关
type Module struct {
	httpClient   *http.Client // 普通请求（整体 60s 超时）
	streamClient *http.Client // 流式请求（无整体超时，仅限首字节等待，防长回答被截断）
	config       ConfigProvider
	credStore    credstore.Store // 凭据存储（DPAPI 加密），未注入时回退 config 明文

	// A4：按服务拆分独立限频器，互不阻塞
	chatLimiter   rateLimiter
	embedLimiter  rateLimiter
	rerankLimiter rateLimiter
}

// New 创建模型网关模块
// 连接池与超时（Dial/TLS/响应头）由 http_client.go 的 newTransport/newHTTPClient/newStreamClient 构造；
// 流式与非流式共享同一 Transport（连接池复用，响应头超时同样兜底流式首字节等待）。
func New(cfg ConfigProvider) *Module {
	transport := newTransport()
	return &Module{
		httpClient:   newHTTPClient(transport),
		streamClient: newStreamClient(transport),
		config:       cfg,
		chatLimiter:  rateLimiter{rate: 50 * time.Millisecond},
		// 嵌入/重排是批量高频调用（索引 & 全局检索），放宽限频避免拖累吞吐
		embedLimiter:  rateLimiter{rate: 20 * time.Millisecond},
		rerankLimiter: rateLimiter{rate: 20 * time.Millisecond},
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

	data, err := m.retry(&m.chatLimiter, func() ([]byte, error) {
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

		m.chatLimiter.wait()

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

// ChatJSON 聊天调用并解析 JSON 响应
func (m *Module) ChatJSON(system, user, schemaDesc string, result interface{}) error {
	content, err := m.Chat(system, user, &contract.ChatOptions{JSONMode: true, Temperature: 0.2})
	if err != nil {
		return err
	}

	if err := json.Unmarshal([]byte(content), result); err != nil {
		// C3：宽松 JSON 清洗——提取首个 { ... } 块重试解析，
		// 应对模型在 JSON 外包裹 Markdown 或多余前缀导致解析失败的情况
		if cleaned, ok := extractJSONBlock(content); ok {
			if perr := json.Unmarshal([]byte(cleaned), result); perr == nil {
				return nil
			}
		}
		return fmt.Errorf("[llm] 解析 JSON 响应失败: %w\n原文: %s", err, content)
	}
	return nil
}

// extractJSONBlock 从含 Markdown/多余前缀的文本中提取首个 { ... } 块。
// 返回 ok=false 表示未找到合法闭合块。
var jsonBlockRe = regexp.MustCompile(`[{][^{].*?[}]`)

func extractJSONBlock(s string) (string, bool) {
	m := jsonBlockRe.FindStringIndex(s)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(s[m[0]:m[1]]), true
}

// TestChat 测试聊天端点（使用已保存配置）
func (m *Module) TestChat() error {
	_, err := m.Chat("你是一个助手。", "回复ok即可。", &contract.ChatOptions{MaxTokens: 10, Temperature: 0.1})
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
	data, err := m.retry(&m.chatLimiter, func() ([]byte, error) {
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
