package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
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

// newTransport 构建共享连接池 Transport（httpClient 与 streamClient 共用）。
func newTransport() *http.Transport {
	return &http.Transport{
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
}

// newHTTPClient 构建普通请求客户端（整体 60s 超时）。
func newHTTPClient(transport *http.Transport) *http.Client {
	return &http.Client{
		Timeout:   requestTimeout, // 非流式整体超时
		Transport: transport,
	}
}

// newStreamClient 构建流式请求客户端：Timeout=0 无整体超时（SSE 长回答可能超 60s）；
// 首字节等待上限由共享 Transport 的 ResponseHeaderTimeout（30s）兜底，
// 流式读侧 idle 上限由读取 goroutine 的 idleReadCloser 兜底（见 stream_decode.go）。
func newStreamClient(transport *http.Transport) *http.Client {
	return &http.Client{
		Transport: transport,
	}
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
