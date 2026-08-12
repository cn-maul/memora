package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"memora/internal/logx"
)

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
