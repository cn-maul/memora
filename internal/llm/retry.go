package llm

import (
	"fmt"
	"strings"
	"time"

	"memora/internal/logx"
)

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
