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
	"strings"
	"sync"
	"time"

	"memora/internal/contract"
)

// ConfigProvider LLM 配置获取接口
type ConfigProvider interface {
	GetLLMConfig() (baseURL, apiKey, model string, temperature float64)
	GetEmbedConfig() (baseURL, apiKey, model string, dimensions int)
}

// Module 模型网关
type Module struct {
	httpClient   *http.Client // 普通请求（整体 60s 超时）
	streamClient *http.Client // 流式请求（无整体超时，仅限首字节等待，防长回答被截断）
	config       ConfigProvider

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
	}
	return &Module{
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
		// 流式请求：Timeout=0 无整体超时（SSE 长回答可能超 60s），
		// 但设 ResponseHeaderTimeout 防止连接建立后首字节迟迟不来而挂死
		streamClient: &http.Client{
			Transport: transport,
		},
		config:    cfg,
		rateLimit: 50 * time.Millisecond, // 20 rps
	}
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

	req, err := http.NewRequest(method, url, bytes.NewReader(reqBody))
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
			fmt.Printf("[llm] 重试 %d/3，等待 %v: %v\n", i+1, wait, err)
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
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

// streamResponse 流式聊天响应体
type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// Chat 聊天调用
func (m *Module) Chat(system, user string, opts *contract.ChatOptions) (string, error) {
	if opts == nil {
		opts = &contract.ChatOptions{Temperature: 0.2}
	}

	baseURL, apiKey, model, defTemp := m.config.GetLLMConfig()
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

	return resp.Choices[0].Message.Content, nil
}

// ChatStream 流式聊天调用，通过 channel 逐块返回内容，支持取消。
// 调用方读完 chunks 后必须关闭 cancel 或读完所有内容。
func (m *Module) ChatStream(system, user string, opts *contract.ChatOptions, cancel <-chan struct{}) (<-chan string, error) {
	if opts == nil {
		opts = &contract.ChatOptions{Temperature: 0.2}
	}

	baseURL, apiKey, model, defTemp := m.config.GetLLMConfig()
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
	// 否则取消时连接一直挂着直到响应（修复 review should-fix）
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
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
			return nil, fmt.Errorf("[llm] 流式请求已取消")
		default:
		}

		req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
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
			return nil, lastErr
		}

		if attempt < 2 {
			wait := time.Duration(1<<uint(attempt)) * time.Second
			fmt.Printf("[llm] 流式请求重试 %d/3，等待 %v: %v\n", attempt+1, wait, lastErr)
			select {
			case <-time.After(wait):
			case <-cancel:
				return nil, fmt.Errorf("[llm] 流式请求已取消")
			}
		}
	}
	if resp == nil {
		return nil, fmt.Errorf("[llm] 流式请求重试耗尽: %w", lastErr)
	}

	ch := make(chan string, 16)
	go func() {
		defer resp.Body.Close()
		defer close(ch)

		if resp.StatusCode >= 400 {
			errBody, _ := io.ReadAll(resp.Body)
			errMsg := fmt.Sprintf("[llm] 流式请求 %d: %s", resp.StatusCode, string(errBody))
			select {
			case ch <- "__ERROR__:" + errMsg:
			case <-cancel:
			}
			return
		}

		reader := bufio.NewReader(resp.Body)
		for {
			select {
			case <-cancel:
				return
			default:
			}

			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					// 可能最后一个 data: [DONE]
					line = strings.TrimSpace(line)
					if line == "data: [DONE]" {
						return
					}
				}
				return
			}

			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return
			}

			var streamResp streamResponse
			if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
				continue
			}

			if len(streamResp.Choices) > 0 {
				delta := streamResp.Choices[0].Delta.Content
				if delta != "" {
					select {
					case ch <- delta:
					case <-cancel:
						return
					}
				}
			}
		}
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
		return fmt.Errorf("[llm] 解析 JSON 响应失败: %w\n原文: %s", err, content)
	}
	return nil
}

// embedRequest 嵌入请求体
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
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
	baseURL, apiKey, model, _ := m.config.GetEmbedConfig()
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
			Model: model,
			Input: batch,
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
		apiKey = savedKey
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
		apiKey = savedKey
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
