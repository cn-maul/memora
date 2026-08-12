package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

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

// TestEmbed 测试嵌入端点（使用已保存配置）
func (m *Module) TestEmbed() error {
	_, err := m.Embed([]string{"测试"})
	return err
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
