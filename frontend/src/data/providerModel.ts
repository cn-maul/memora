// 服务商/模型状态机（Phase 3 / P2-11）：Provider preset、远程模型、错误策略的唯一归属。
// SettingsPage 与 OnboardingWizard 不再各自实现「选服务商 → 填地址/模型/维度 → 获取模型 → 测试」，
// 统一走这里的 createProviderController；每个区块（chat/embed/rerank）是一个带自身状态的控制器。
//
// 两页仅在「策略」上通过 options 表达差异，行为逐一对应（见各调用处），不改变任何线格式与文案。
import { reactive } from 'vue'
import {
  CUSTOM_PROVIDER_ID,
  PROVIDER_PRESETS,
  providerForUrl,
  type ProviderPreset,
} from '@/data/providers'
import {
  fetchModels as apiFetchModels,
  testLLM,
  translateApiError,
  type LLMTestParams,
} from '@/api/client'

export type ModelKind = 'chat' | 'embed' | 'rerank'

// 服务商预设里与某 kind 相关的字段（单一取用入口）
export interface PresetFields {
  baseUrl: string
  models: string[]
  dimensions: number
}

// 各 kind 默认值（与历史行为一致：chat 温度 0.7、embed 维度 1024）
export interface KindDefaults {
  baseUrl: string
  model: string
  dimensions: number
  temperature: number
}

export const KIND_DEFAULTS: Record<ModelKind, KindDefaults> = {
  chat: { baseUrl: '', model: '', dimensions: 0, temperature: 0.7 },
  embed: { baseUrl: '', model: '', dimensions: 1024, temperature: 0 },
  rerank: { baseUrl: '', model: '', dimensions: 0, temperature: 0 },
}

// 组装 { baseUrl, apiKey, model, temperature/dimensions } 区块（init 载荷）
export interface ProviderSection {
  baseUrl?: string
  apiKey?: string
  model?: string
  temperature?: number
  dimensions?: number
}

// 「获取模型」的校验/兜底文案（各页可按自身文案配置，未配置项跳过校验）
export interface FetchModelsConfig {
  requireBaseUrl?: string
  requireApiKey?: string
  emptyMessage?: string
  fallbackError?: string
}

export interface BuildSectionOptions {
  // chat 区块是否携带 temperature（设置页 init 带；向导 init 不带，保持原线格式）
  includeTemperature?: boolean
  // baseUrl 为空时是否省略该键（设置页 rerank init 用 || undefined）
  omitEmptyBaseUrl?: boolean
}

// 单个区块的持久状态（控制器内部经 Vue reactive 托管，模板可直接 v-model）
export interface ProviderSectionState {
  providerId: string
  baseUrl: string
  apiKey: string
  model: string
  dimensions: number
  temperature: number
  fetchedModels: string[]
  modelsError: string
}

export interface ProviderControllerOptions {
  // 默认模型（如设置页 rerank 默认 Pro/BAAI/bge-reranker-v2-m3）
  defaultModel?: string
  // 选预设时是否预填第一个预设模型（设置页 true；向导 false —— 向导引导用户「获取模型」）
  fillModelOnPreset?: boolean
  // 选预设时是否清空已拉取的远程模型与错误（向导 true；设置页 false 保持历史行为）
  clearRemoteOnPreset?: boolean
}

export interface ProviderController {
  kind: ModelKind
  state: ProviderSectionState
  // 当前 providerId 命中的预设（自定义时 undefined）
  preset: ProviderPreset | undefined
  // 当前预设里该 kind 的模型列表（自定义时为空）
  presetModels: string[]
  // 选预设 → 填地址/模型/维度（单一事实来源；自定义项只更新 providerId 不改字段）
  applyPreset(providerId: string): void
  // 按当前地址反推命中的预设（自定义显示用）
  detectFromUrl(): void
  // 远程模型列表（含校验文案 + 统一 translateApiError 错误策略；成功写 fetchedModels）
  fetchModels(config?: FetchModelsConfig): Promise<string[]>
  // 测试连接（统一文案：✓ 测试通过 / ✗ 翻译后错误）
  testConnection(): Promise<string>
  // 组装 init 区块对象
  buildSection(opts?: BuildSectionOptions): ProviderSection
  // 模型下拉候选：远程列表 + 预设模型 + 当前值兜底（设置页用）
  modelOptions(): string[]
  // 是否用下拉（远程有列表或预设带模型）；否则用手动输入框（设置页用）
  useSelect(): boolean
}

export function presetForId(id: string): ProviderPreset | undefined {
  return PROVIDER_PRESETS.find((p) => p.id === id)
}

export function presetFieldsFor(preset: ProviderPreset, kind: ModelKind): PresetFields {
  switch (kind) {
    case 'chat':
      return { baseUrl: preset.chatBaseUrl, models: preset.chatModels, dimensions: 0 }
    case 'embed':
      return { baseUrl: preset.embedBaseUrl, models: preset.embedModels, dimensions: preset.embedDim }
    case 'rerank':
      return { baseUrl: preset.rerankBaseUrl, models: preset.rerankModels, dimensions: 0 }
  }
}

// 模型下拉选项：远程列表/预设模型 + 当前已填但不在列表里的值（自定义模型兜底）
export function modelOptions(models: string[], current: string): string[] {
  const opts = [...models]
  if (current && !opts.includes(current)) opts.unshift(current)
  return opts
}

export function createProviderController(kind: ModelKind, options: ProviderControllerOptions = {}): ProviderController {
  const defaults = KIND_DEFAULTS[kind]
  const state = reactive<ProviderSectionState>({
    providerId: CUSTOM_PROVIDER_ID,
    baseUrl: defaults.baseUrl,
    apiKey: '',
    model: options.defaultModel ?? defaults.model,
    dimensions: defaults.dimensions,
    temperature: defaults.temperature,
    fetchedModels: [],
    modelsError: '',
  })

  const controller: ProviderController = {
    kind,
    state,
    get preset() {
      return presetForId(state.providerId)
    },
    get presetModels() {
      const p = presetForId(state.providerId)
      return p ? presetFieldsFor(p, kind).models : []
    },
    applyPreset(providerId: string) {
      state.providerId = providerId
      const p = presetForId(providerId)
      if (!p) return
      const { baseUrl, models, dimensions } = presetFieldsFor(p, kind)
      if (baseUrl) state.baseUrl = baseUrl
      if (kind === 'embed' && dimensions) state.dimensions = dimensions
      if (options.fillModelOnPreset === true) {
        if (models.length) state.model = models[0]
      } else {
        state.model = ''
      }
      if (options.clearRemoteOnPreset) {
        state.fetchedModels = []
        state.modelsError = ''
      }
    },
    detectFromUrl() {
      state.providerId = providerForUrl(state.baseUrl, kind)
    },
    async fetchModels(config: FetchModelsConfig = {}) {
      state.modelsError = ''
      if (config.requireBaseUrl && !state.baseUrl.trim()) {
        state.modelsError = config.requireBaseUrl
        return []
      }
      if (config.requireApiKey && !state.apiKey.trim()) {
        state.modelsError = config.requireApiKey
        return []
      }
      try {
        const models = await apiFetchModels({
          kind: controller.kind,
          baseUrl: state.baseUrl.trim(),
          apiKey: state.apiKey.trim() || undefined,
        })
        state.fetchedModels = models
        if (models.length === 0 && config.emptyMessage) {
          state.modelsError = config.emptyMessage
        }
        return models
      } catch (e: any) {
        state.modelsError = translateApiError(e?.message) || config.fallbackError || '获取模型失败'
        return []
      }
    },
    async testConnection(): Promise<string> {
      try {
        const params: LLMTestParams = {
          type: controller.kind,
          baseUrl: state.baseUrl,
          model: state.model,
          apiKey: state.apiKey || undefined,
        }
        if (kind === 'chat') params.temperature = state.temperature
        const res = await testLLM(params)
        return res.ok ? '✓ 测试通过' : `✗ ${translateApiError(res.message)}`
      } catch (e: any) {
        return `✗ ${translateApiError(e?.message)}`
      }
    },
    buildSection(opts: BuildSectionOptions = {}): ProviderSection {
      const section: ProviderSection = {
        baseUrl: opts.omitEmptyBaseUrl ? state.baseUrl || undefined : state.baseUrl,
        apiKey: state.apiKey || undefined,
        model: state.model || undefined,
      }
      if (kind === 'chat') {
        if (opts.includeTemperature) section.temperature = state.temperature || undefined
      } else if (kind === 'embed') {
        section.dimensions = state.dimensions || undefined
      }
      return section
    },
    modelOptions() {
      return modelOptions([...state.fetchedModels, ...controller.presetModels], state.model)
    },
    useSelect() {
      return state.fetchedModels.length > 0 || controller.presetModels.length > 0
    },
  }

  return controller
}

// 服务商数据与常量统一经本模块再导出（两页只从一个入口取）
export { CUSTOM_PROVIDER_ID, PROVIDER_PRESETS, providerForUrl }
export type { ProviderPreset }
