// 设置页「模型/服务商」区块的业务 composable（P2-05 抽取）。
// 三个模型控制器（chat/embed/rerank）统一由 createProviderController 提供（见 data/providerModel），
// 本 composable 仅负责协调：模型下拉候选、是否用下拉、「获取模型」的公共状态（按区块独立展示错误）。
import { ref, computed } from 'vue'
import { createProviderController, type ModelKind, type ProviderController } from '@/data/providerModel'

// 三个模型区块控制器 + 统一索引（供「获取模型」按 kind 分发）
export interface ProviderControllers {
  chat: ProviderController
  embed: ProviderController
  rerank: ProviderController
  controllers: Record<ModelKind, ProviderController>
}

export function useProviderSettings() {
  // 三个模型区块统一由共享状态机托管（服务商切换 / 获取模型 / 测试 / 载荷组装见 providerModel）。
  // 设置页策略：选预设时预填首个预设模型 + 维度（fillModelOnPreset），不清已拉取的远程模型。
  const chat = createProviderController('chat', { fillModelOnPreset: true })
  const embed = createProviderController('embed', { fillModelOnPreset: true })
  const rerank = createProviderController('rerank', {
    fillModelOnPreset: true,
    defaultModel: 'Pro/BAAI/bge-reranker-v2-m3',
  })
  const controllers: Record<ModelKind, typeof chat> = { chat, embed, rerank }
  const fetchingModels = ref<ModelKind | ''>('') // '' | 'chat' | 'embed' | 'rerank'

  // 模型下拉选项：预设模型 + 已拉取的远程模型 + 当前已填值兜底（自定义模型）
  const llmModelOptions = computed(() => chat.modelOptions())
  const embedModelOptions = computed(() => embed.modelOptions())
  const rerankModelOptions = computed(() => rerank.modelOptions())
  // 有预设/远程模型就用下拉，否则手动输入
  const llmUseSelect = computed(() => chat.useSelect())
  const embedUseSelect = computed(() => embed.useSelect())
  const rerankUseSelect = computed(() => rerank.useSelect())

  async function handleFetchModels(kind: ModelKind) {
    fetchingModels.value = kind
    // 错误按区块独立展示，互不影响：先统一清空三块，再只写入目标块
    chat.state.modelsError = ''
    embed.state.modelsError = ''
    rerank.state.modelsError = ''
    try {
      await controllers[kind].fetchModels({})
    } finally {
      fetchingModels.value = ''
    }
  }

  return {
    chat,
    embed,
    rerank,
    controllers,
    fetchingModels,
    handleFetchModels,
    llmModelOptions,
    embedModelOptions,
    rerankModelOptions,
    llmUseSelect,
    embedUseSelect,
    rerankUseSelect,
  }
}
