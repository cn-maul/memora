<script setup lang="ts">
// 首次使用向导：选文件夹 → 连接 AI（可跳过）→ 开始使用
// 目标：小白在 3 步内成功用起来，无需理解 base_url/模型名等概念。
import { ref, computed } from 'vue'
import { useWorkspaceStore } from '@/stores/workspace'
import { browsePickDir, fetchModels, translateApiError } from '@/api/client'
import { PROVIDER_PRESETS, CUSTOM_PROVIDER_ID } from '@/data/providers'
import Icon from '@/components/Icon.vue'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{ (e: 'done'): void }>()

const ws = useWorkspaceStore()

const step = ref(1)
const starting = ref(false)
const startError = ref('')
const started = ref(false)

// ── 步骤 1：文件夹 ──
const workspacePath = ref('')
const pickingDir = ref(false)
const pickMsg = ref('')

async function pickFolder() {
  pickingDir.value = true
  pickMsg.value = ''
  try {
    const res = await browsePickDir(workspacePath.value || undefined)
    if (!res.cancelled && res.path) workspacePath.value = res.path
  } catch (e: any) {
    pickMsg.value = translateApiError(e?.message)
  } finally {
    pickingDir.value = false
  }
}

// 关闭向导：正在初始化时不允许关闭（避免打断保存流程）
function closeWizard() {
  if (starting.value) return
  emit('done')
}

// ── 步骤 2：连接 AI（可选）──
// 每个模型块：服务商下拉 + API Key（地址/模型自动填）
const llmProviderId = ref(CUSTOM_PROVIDER_ID)
const llmApiKey = ref('')
const llmModel = ref('')
const llmBaseUrl = ref('')

const embedProviderId = ref(CUSTOM_PROVIDER_ID)
const embedApiKey = ref('')
const embedModel = ref('')
const embedBaseUrl = ref('')
const embedDimensions = ref(1024)

const rerankProviderId = ref(CUSTOM_PROVIDER_ID)
const rerankApiKey = ref('')
const rerankModel = ref('')
const rerankBaseUrl = ref('')

function applyProvider(kind: 'chat' | 'embed' | 'rerank', id: string) {
  const p = PROVIDER_PRESETS.find((x) => x.id === id)
  if (!p) return
  // 只填接口地址与默认维度；模型由「获取模型」拉取真实列表后选择（避免预设与账户可用模型不符）
  if (kind === 'chat') {
    if (p.chatBaseUrl) llmBaseUrl.value = p.chatBaseUrl
    llmModel.value = ''
    llmFetchedModels.value = []
    llmModelsError.value = ''
  } else if (kind === 'embed') {
    if (p.embedBaseUrl) embedBaseUrl.value = p.embedBaseUrl
    if (p.embedDim) embedDimensions.value = p.embedDim
    embedModel.value = ''
    embedFetchedModels.value = []
    embedModelsError.value = ''
  } else {
    if (p.rerankBaseUrl) rerankBaseUrl.value = p.rerankBaseUrl
    rerankModel.value = ''
    rerankFetchedModels.value = []
    rerankModelsError.value = ''
  }
}

// 获取模型：填好密钥后拉取该服务的真实模型列表
const llmFetchedModels = ref<string[]>([])
const embedFetchedModels = ref<string[]>([])
const rerankFetchedModels = ref<string[]>([])
const fetchingModels = ref<'chat' | 'embed' | 'rerank' | ''>('')
const llmModelsError = ref('')
const embedModelsError = ref('')
const rerankModelsError = ref('')

async function fetchWizardModels(kind: 'chat' | 'embed' | 'rerank') {
  const baseUrl = kind === 'chat' ? llmBaseUrl.value : kind === 'embed' ? embedBaseUrl.value : rerankBaseUrl.value
  const apiKey = kind === 'chat' ? llmApiKey.value : kind === 'embed' ? embedApiKey.value : rerankApiKey.value
  const errRef = kind === 'chat' ? llmModelsError : kind === 'embed' ? embedModelsError : rerankModelsError
  const hitRef = kind === 'chat' ? llmFetchedModels : kind === 'embed' ? embedFetchedModels : rerankFetchedModels
  errRef.value = ''
  if (!baseUrl.trim()) {
    errRef.value = '请先选择服务商'
    return
  }
  if (!apiKey.trim()) {
    errRef.value = '请先填入 API Key 再获取模型'
    return
  }
  fetchingModels.value = kind
  try {
    hitRef.value = await fetchModels({ kind, baseUrl: baseUrl.trim(), apiKey: apiKey.trim() || undefined })
    if (hitRef.value.length === 0) errRef.value = '该服务没有返回可用模型，请检查服务地址和密钥'
  } catch (e: any) {
    errRef.value = translateApiError(e?.message) || '获取模型失败'
  } finally {
    fetchingModels.value = ''
  }
}

function modelOptions(models: string[], current: string): string[] {
  const opts = [...models]
  if (current && !opts.includes(current)) opts.unshift(current)
  return opts
}

// AI 是否配置了任意一项（用于步骤 2 的提示）
const hasAnyAI = computed(
  () => !!llmBaseUrl.value || !!embedBaseUrl.value || !!rerankBaseUrl.value,
)

// ── 步骤 3：开始使用 ──
async function start() {
  if (!workspacePath.value.trim()) {
    startError.value = '请先选择要管理的文件夹'
    return
  }
  starting.value = true
  startError.value = ''
  try {
    await ws.init({
      workspacePath: workspacePath.value.trim(),
      llm: llmBaseUrl.value
        ? { baseUrl: llmBaseUrl.value, apiKey: llmApiKey.value || undefined, model: llmModel.value || undefined }
        : undefined,
      embed: embedBaseUrl.value
        ? { baseUrl: embedBaseUrl.value, apiKey: embedApiKey.value || undefined, model: embedModel.value || undefined, dimensions: embedDimensions.value || undefined }
        : undefined,
      rerank: rerankBaseUrl.value
        ? { baseUrl: rerankBaseUrl.value, apiKey: rerankApiKey.value || undefined, model: rerankModel.value || undefined }
        : undefined,
    })
    started.value = true
  } catch (e: any) {
    startError.value = e.message || '初始化失败'
  } finally {
    starting.value = false
  }
}

function next() {
  if (step.value === 1 && !workspacePath.value.trim()) {
    pickMsg.value = '请先选择要管理的文件夹'
    return
  }
  startError.value = ''
  step.value++
}
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="obw-mask" @click.self="closeWizard">
      <div class="obw-card">
        <button class="obw-close" title="关闭向导" @click="closeWizard">
          <Icon name="x" :size="16" />
        </button>
        <!-- 步骤指示 -->
        <div class="obw-head">
          <div class="obw-logo">
            <Icon name="memory" :size="22" />
          </div>
          <div class="obw-steps">
            <span
              v-for="(label, i) in ['选择文件夹', '连接 AI（可选）', '开始使用']"
              :key="label"
              class="obw-step"
              :class="{ 'obw-step--active': step === i + 1, 'obw-step--done': step > i + 1 }"
            >
              <span class="obw-step__dot">{{ step > i + 1 ? '✓' : i + 1 }}</span>
              {{ label }}
            </span>
          </div>
        </div>

        <!-- 步骤 1：选择文件夹 -->
        <div v-if="step === 1" class="obw-body">
          <h3 class="obw-title">把要管理的文档放进哪个文件夹？</h3>
          <p class="obw-desc">
            选中后，这个文件夹里的文件会被<b>自动记录版本</b>：改了、删了都能随时找回。随时可换。
          </p>
          <div class="obw-dir-row">
            <input v-model="workspacePath" class="input" placeholder="选择一个文件夹，如 D:/我的文档" />
            <button class="btn btn-primary" :disabled="pickingDir" @click="pickFolder">
              {{ pickingDir ? '选择中…' : '选择文件夹' }}
            </button>
          </div>
          <span v-if="pickMsg" class="obw-err">{{ pickMsg }}</span>
          <span v-if="startError" class="obw-err">{{ startError }}</span>
        </div>

        <!-- 步骤 2：连接 AI（可选） -->
        <div v-else-if="step === 2" class="obw-body">
          <h3 class="obw-title">连接 AI，让文档「能用起来」（可选）</h3>
          <p class="obw-desc">
            连接后可以<b>按内容搜索文档、文档问答、自动标签</b>。不连接也能用文件浏览和版本找回，随时可在设置里补。
          </p>

          <!-- 聊天 AI -->
          <div class="obw-ai-block">
            <div class="obw-ai-block__head">
              <Icon name="chat" :size="14" />
              <span>大语言模型（对话问答 / 自动标签）</span>
            </div>
            <div class="obw-ai-block__row">
              <select
                :value="llmProviderId"
                class="select obw-provider"
                @change="(e) => { llmProviderId = (e.target as HTMLSelectElement).value; applyProvider('chat', llmProviderId) }"
              >
                <option v-for="p in PROVIDER_PRESETS" :key="p.id" :value="p.id">{{ p.name }}</option>
                <option :value="CUSTOM_PROVIDER_ID">自定义</option>
              </select>
              <input
                v-model="llmApiKey"
                class="input obw-key"
                type="password"
                placeholder="API Key（粘贴）"
              />
              <button
                class="btn btn-ghost btn-sm obw-fetch"
                :disabled="fetchingModels === 'chat'"
                @click="fetchWizardModels('chat')"
              >
                {{ fetchingModels === 'chat' ? '获取中…' : '获取模型' }}
              </button>
            </div>
            <select
              v-if="llmFetchedModels.length"
              v-model="llmModel"
              class="select obw-model"
            >
              <option v-for="m in modelOptions(llmFetchedModels, llmModel)" :key="m" :value="m">{{ m }}</option>
            </select>
            <span v-if="llmModelsError" class="obw-err">{{ llmModelsError }}</span>
          </div>

          <!-- 内容整理 -->
          <div class="obw-ai-block">
            <div class="obw-ai-block__head">
              <Icon name="search" :size="14" />
              <span>嵌入模型（按内容搜索）</span>
            </div>
            <div class="obw-ai-block__row">
              <select
                :value="embedProviderId"
                class="select obw-provider"
                @change="(e) => { embedProviderId = (e.target as HTMLSelectElement).value; applyProvider('embed', embedProviderId) }"
              >
                <option v-for="p in PROVIDER_PRESETS" :key="p.id" :value="p.id">{{ p.name }}</option>
                <option :value="CUSTOM_PROVIDER_ID">自定义</option>
              </select>
              <input
                v-model="embedApiKey"
                class="input obw-key"
                type="password"
                placeholder="API Key（粘贴）"
              />
              <button
                class="btn btn-ghost btn-sm obw-fetch"
                :disabled="fetchingModels === 'embed'"
                @click="fetchWizardModels('embed')"
              >
                {{ fetchingModels === 'embed' ? '获取中…' : '获取模型' }}
              </button>
            </div>
            <select
              v-if="embedFetchedModels.length"
              v-model="embedModel"
              class="select obw-model"
            >
              <option v-for="m in modelOptions(embedFetchedModels, embedModel)" :key="m" :value="m">{{ m }}</option>
            </select>
            <span v-if="embedModelsError" class="obw-err">{{ embedModelsError }}</span>
          </div>

          <!-- 排序优化（可选） -->
          <details class="obw-ai-advanced">
            <summary>重排模型（可选，一般可跳过）</summary>
            <div class="obw-ai-block__row" style="margin-top: 8px">
              <select
                :value="rerankProviderId"
                class="select obw-provider"
                @change="(e) => { rerankProviderId = (e.target as HTMLSelectElement).value; applyProvider('rerank', rerankProviderId) }"
              >
                <option v-for="p in PROVIDER_PRESETS" :key="p.id" :value="p.id">{{ p.name }}</option>
                <option :value="CUSTOM_PROVIDER_ID">自定义</option>
              </select>
              <input
                v-model="rerankApiKey"
                class="input obw-key"
                type="password"
                placeholder="API Key（粘贴）"
              />
              <button
                class="btn btn-ghost btn-sm obw-fetch"
                :disabled="fetchingModels === 'rerank'"
                @click="fetchWizardModels('rerank')"
              >
                {{ fetchingModels === 'rerank' ? '获取中…' : '获取模型' }}
              </button>
            </div>
            <select
              v-if="rerankFetchedModels.length"
              v-model="rerankModel"
              class="select obw-model"
            >
              <option v-for="m in modelOptions(rerankFetchedModels, rerankModel)" :key="m" :value="m">{{ m }}</option>
            </select>
            <span v-if="rerankModelsError" class="obw-err">{{ rerankModelsError }}</span>
          </details>

          <p v-if="hasAnyAI" class="obw-ok">✓ 已填写 AI 信息，可直接下一步</p>
        </div>

        <!-- 步骤 3：开始使用 -->
        <div v-else class="obw-body">
          <template v-if="!started">
            <h3 class="obw-title">准备好开始了吗？</h3>
            <ul class="obw-summary">
              <li><Icon name="folder" :size="14" /> 管理文件夹：<b>{{ workspacePath }}</b></li>
              <li>
                <Icon name="chat" :size="14" />
                AI 助手：{{ llmBaseUrl ? '已连接' : '未连接（可稍后在设置里补）' }}
              </li>
              <li>
                <Icon name="search" :size="14" />
                内容搜索：{{ embedBaseUrl ? '已连接' : '未连接（可稍后在设置里补）' }}
              </li>
            </ul>
            <p class="obw-desc">
              点击「开始使用」后，系统会接管该文件夹：文件自动记录版本、改动后稍等自动保存新版本。
            </p>
            <span v-if="startError" class="obw-err">{{ startError }}</span>
          </template>
          <template v-else>
            <div class="obw-done">
              <span class="obw-done__icon">🎉</span>
              <h3 class="obw-title">已开始使用！</h3>
              <p class="obw-desc">
                你的文件会自动记录版本，改动后稍等片刻即生成新版本，随时可在左侧「版本记录」找回。
                {{ hasAnyAI ? 'AI 功能已就绪，可以开始按内容搜索和问答了。' : '连接 AI 后即可按内容搜索和问答，随时可在设置里补。' }}
              </p>
            </div>
          </template>
        </div>

        <!-- 底部按钮 -->
        <div class="obw-foot">
          <button v-if="step > 1 && !started" class="btn btn-ghost" @click="step--">
            <Icon name="arrow-left" :size="14" /> 上一步
          </button>
          <span class="obw-foot__spacer"></span>
          <button v-if="step === 2 && !started" class="btn btn-ghost" @click="step++">
            暂时跳过
          </button>
          <button v-if="step === 1" class="btn btn-primary" @click="next">
            下一步 <Icon name="arrow-right" :size="14" />
          </button>
          <button v-if="step === 2 && !started" class="btn btn-primary" @click="next">
            下一步 <Icon name="arrow-right" :size="14" />
          </button>
          <button v-if="step === 3 && !started" class="btn btn-primary" :disabled="starting" @click="start">
            {{ starting ? '正在准备…' : '开始使用' }}
          </button>
          <button v-if="started" class="btn btn-primary" @click="emit('done')">
            完成，去逛逛
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.obw-mask {
  position: fixed;
  inset: 0;
  z-index: 2000;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}
.obw-card {
  position: relative;
  width: 560px;
  max-width: 94vw;
  max-height: 90vh;
  overflow-y: auto;
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  border-radius: var(--r-xl);
  box-shadow: 0 16px 48px rgba(0, 0, 0, 0.3);
  padding: 24px 28px;
  display: flex;
  flex-direction: column;
}
.obw-close {
  position: absolute;
  top: 12px;
  right: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--r-md);
  background: none;
  color: var(--c-text-tertiary);
  cursor: pointer;
}
.obw-close:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}
.obw-head {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 20px;
}
.obw-logo {
  width: 40px;
  height: 40px;
  border-radius: var(--r-xl);
  background: var(--c-brand-soft);
  color: var(--c-brand);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.obw-steps {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 12.5px;
  color: var(--c-text-tertiary);
}
.obw-step {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
.obw-step__dot {
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: var(--c-bg-hover);
  color: var(--c-text-secondary);
  font-size: 11px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.obw-step--active {
  color: var(--c-text-primary);
  font-weight: 600;
}
.obw-step--active .obw-step__dot {
  background: var(--c-brand);
  color: var(--c-on-brand);
}
.obw-step--done .obw-step__dot {
  background: var(--c-success);
  color: #fff;
}
.obw-body {
  flex: 1;
}
.obw-title {
  font-size: 17px;
  font-weight: 700;
  color: var(--c-text-primary);
  margin: 0 0 8px;
}
.obw-desc {
  font-size: 13px;
  line-height: 1.7;
  color: var(--c-text-secondary);
  margin: 0 0 16px;
}
.obw-dir-row {
  display: flex;
  gap: 8px;
  align-items: center;
}
.obw-dir-row .input {
  flex: 1;
}
.obw-err {
  display: block;
  margin-top: 8px;
  font-size: 12.5px;
  color: var(--c-danger);
}
.obw-ok {
  margin-top: 10px;
  font-size: 12.5px;
  color: var(--c-success);
}
.obw-ai-block {
  border: 1px solid var(--c-border);
  border-radius: var(--r-md);
  padding: 10px 12px;
  margin-bottom: 10px;
}
.obw-ai-block__head {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--c-text-secondary);
  margin-bottom: 8px;
}
.obw-ai-block__row {
  display: flex;
  gap: 8px;
  align-items: center;
}
.obw-provider {
  flex: 1.2;
}
.obw-fetch {
  flex-shrink: 0;
}
.obw-key {
  flex: 1;
}
.obw-model {
  margin-top: 8px;
  width: 100%;
}
.obw-ai-advanced {
  font-size: 12.5px;
  color: var(--c-text-tertiary);
}
.obw-ai-advanced summary {
  cursor: pointer;
}
.obw-summary {
  list-style: none;
  padding: 0;
  margin: 0 0 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.obw-summary li {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--c-text-secondary);
  background: var(--c-bg-panel);
  border: 1px solid var(--c-border);
  border-radius: var(--r-md);
  padding: 10px 12px;
}
.obw-summary b {
  color: var(--c-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 70%;
}
.obw-done {
  text-align: center;
  padding: 20px 0;
}
.obw-done__icon {
  font-size: 44px;
  display: block;
  margin-bottom: 10px;
}
.obw-foot {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 20px;
  padding-top: 14px;
  border-top: 1px solid var(--c-border);
}
.obw-foot__spacer {
  flex: 1;
}
</style>
