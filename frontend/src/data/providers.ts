// 常见 AI 服务商预设：选服务商自动填接口地址与模型，小白无需理解 base_url / 模型名。
// 每个服务商可按「聊天 / 嵌入 / 重排」三类分别填写；某类不支持时留空数组，用户手动填。

export interface ProviderPreset {
  id: string
  name: string
  chatBaseUrl: string
  chatModels: string[]
  embedBaseUrl: string
  embedModels: string[]
  embedDim: number
  rerankBaseUrl: string
  rerankModels: string[]
}

// 自定义（手动填写）的虚拟项 id
export const CUSTOM_PROVIDER_ID = '__custom__'

export const PROVIDER_PRESETS: ProviderPreset[] = [
  {
    id: 'deepseek',
    name: 'DeepSeek',
    chatBaseUrl: 'https://api.deepseek.com/v1',
    chatModels: ['deepseek-chat', 'deepseek-reasoner'],
    embedBaseUrl: '',
    embedModels: [],
    embedDim: 0,
    rerankBaseUrl: '',
    rerankModels: [],
  },
  {
    id: 'qwen',
    name: '通义千问',
    chatBaseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
    chatModels: ['qwen-plus', 'qwen-turbo', 'qwen-max'],
    embedBaseUrl: '',
    embedModels: [],
    embedDim: 0,
    rerankBaseUrl: '',
    rerankModels: [],
  },
  {
    id: 'sensenova',
    name: '商汤 SenseNova',
    chatBaseUrl: 'https://token.sensenova.cn/v1',
    chatModels: ['sensenova-6.7-flash-lite', 'sensenova-6.7-turbo'],
    embedBaseUrl: '',
    embedModels: [],
    embedDim: 0,
    rerankBaseUrl: '',
    rerankModels: [],
  },
  {
    id: 'siliconflow',
    name: 'SiliconFlow',
    chatBaseUrl: 'https://api.siliconflow.cn/v1',
    chatModels: ['deepseek-ai/DeepSeek-V3', 'Qwen/Qwen3-8B', 'Qwen/Qwen2.5-72B-Instruct', 'Pro/deepseek-ai/DeepSeek-R1'],
    embedBaseUrl: 'https://api.siliconflow.cn/v1',
    embedModels: ['BAAI/bge-m3', 'Qwen/Qwen3-Embedding-8B'],
    embedDim: 1024,
    rerankBaseUrl: 'https://api.siliconflow.cn/v1',
    rerankModels: ['Pro/BAAI/bge-reranker-v2-m3'],
  },
  {
    id: 'openai',
    name: 'OpenAI',
    chatBaseUrl: 'https://api.openai.com/v1',
    chatModels: ['gpt-4o-mini', 'gpt-4o', 'gpt-4.1-mini'],
    embedBaseUrl: 'https://api.openai.com/v1',
    embedModels: ['text-embedding-3-small', 'text-embedding-3-large'],
    embedDim: 1536,
    rerankBaseUrl: '',
    rerankModels: [],
  },
]

// 根据当前接口地址推断命中的服务商；未命中返回 CUSTOM_PROVIDER_ID
export function providerForUrl(baseUrl: string, kind: 'chat' | 'embed' | 'rerank'): string {
  const url = (baseUrl || '').trim().replace(/\/+$/, '')
  if (!url) return CUSTOM_PROVIDER_ID
  const hit = PROVIDER_PRESETS.find((p) => {
    const target = (kind === 'chat' ? p.chatBaseUrl : kind === 'embed' ? p.embedBaseUrl : p.rerankBaseUrl).trim().replace(/\/+$/, '')
    return target !== '' && url === target
  })
  return hit ? hit.id : CUSTOM_PROVIDER_ID
}
