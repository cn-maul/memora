package llm

// ConfigProvider LLM 配置获取接口
type ConfigProvider interface {
	GetLLMConfig() (baseURL, apiKey, model string, temperature float64)
	GetEmbedConfig() (baseURL, apiKey, model string, dimensions int)
	GetRerankConfig() (baseURL, apiKey, model string)
}
