<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { useSettingsStore } from '@/stores/settings'
import { useWorkspaceStore } from '@/stores/workspace'
import { testMarkitdown, testLLM, browsePickDir, detectPython, type PythonDetectResult } from '@/api/client'
import Icon, { type IconName } from '@/components/Icon.vue'

const settings = useSettingsStore()
const ws = useWorkspaceStore()
const router = useRouter()

// ───── 表单状态 ─────
const llmBaseUrl = ref('')
const llmModel = ref('')
const llmTemperature = ref(0.7)
const llmApiKey = ref('')

const embedBaseUrl = ref('')
const embedModel = ref('')
const embedDimensions = ref(1024)
const embedApiKey = ref('')

const rerankBaseUrl = ref('')
const rerankModel = ref('Pro/BAAI/bge-reranker-v2-m3')
const rerankApiKey = ref('')

const pythonPath = ref('')
const command = ref('')
const markitdownCmd = ref('')
const pythonDetected = ref<PythonDetectResult | null>(null)
const pythonDetecting = ref(false)
const pythonDetectError = ref('')

const workspacePath = ref('')
const scanIntervalSec = ref(8)
const recentWindowHours = ref(24)

const testing = ref('')
const testChatResult = ref('')
const testEmbedResult = ref('')
const testRerankResult = ref('')
const testMarkitdownResult = ref('')
const saving = ref(false)
const savedMsg = ref('')
const saveError = ref('')
const initing = ref(false)
const initMsg = ref('')
const formReady = ref(false)

// ───── 侧边栏导航 ─────
interface NavItem {
  id: string
  label: string
  icon: IconName
}

interface NavSection {
  label: string
  items: NavItem[]
}

const navSections: NavSection[] = [
  {
    label: '工作区',
    items: [
      { id: 'sec-workspace', label: '工作区', icon: 'folder' },
      { id: 'sec-markitdown', label: '文档提取', icon: 'file' },
      { id: 'sec-index', label: '索引', icon: 'search' },
    ],
  },
  {
    label: '模型',
    items: [
      { id: 'sec-embed', label: '嵌入模型', icon: 'search' },
      { id: 'sec-rerank', label: '重排模型', icon: 'search' },
      { id: 'sec-llm', label: '大语言模型', icon: 'chat' },
    ],
  },
]

const searchQuery = ref('')
const activeId = ref('sec-workspace')

const filteredNavSections = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return navSections
  return navSections
    .map((s) => ({
      ...s,
      items: s.items.filter((it) => it.label.toLowerCase().includes(q)),
    }))
    .filter((s) => s.items.length > 0)
})

function scrollTo(id: string) {
  activeId.value = id
  const el = document.getElementById(id)
  if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

function goBack() {
  if (window.history.length > 1) router.back()
  else router.push('/files')
}

// ───── 生命周期：加载设置 + 初始化观察器 ─────
let observer: IntersectionObserver | null = null

onMounted(async () => {
  await settings.fetch()
  await ws.fetchInfo()
  if (settings.error) {
    formReady.value = false
    return
  }
  loadFromSettings()
  formReady.value = true

  await nextTick()
  observer = new IntersectionObserver(
    (entries) => {
      const visible = entries
        .filter((e) => e.isIntersecting)
        .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0]
      if (visible) activeId.value = visible.target.id
    },
    { rootMargin: '-20% 0px -60% 0px', threshold: 0 },
  )
  document
    .querySelectorAll<HTMLElement>('.settings-section[id]')
    .forEach((el) => observer!.observe(el))

  runPythonDetect()
})

onUnmounted(() => {
  observer?.disconnect()
})

async function retryLoadSettings() {
  await settings.fetch()
  if (settings.error) {
    formReady.value = false
    return
  }
  loadFromSettings()
  formReady.value = true
}

function loadFromSettings() {
  const s = settings.settings
  llmBaseUrl.value = s.llm?.baseUrl || s.llmBaseUrl || ''
  llmModel.value = s.llm?.model || s.llmModel || ''
  // 用 ?? 而非 ||：temperature=0 是合法值（确定性输出），|| 会回退到默认值（修复 L-2）
  llmTemperature.value = s.llm?.temperature ?? s.llmTemperature ?? 0.7
  embedBaseUrl.value = s.embed?.baseUrl || s.embedBaseUrl || ''
  embedModel.value = s.embed?.model || s.embedModel || ''
  embedDimensions.value = s.embed?.dimensions ?? s.embedDimensions ?? 1024
  rerankBaseUrl.value = s.rerank?.baseUrl || ''
  rerankModel.value = s.rerank?.model || 'Pro/BAAI/bge-reranker-v2-m3'
  pythonPath.value = s.markitdown?.pythonPath || s.markitdownPythonPath || ''
  command.value = s.markitdown?.command || s.markitdownCommand || ''
  markitdownCmd.value = s.markitdown?.markitdownCmd || ''
  scanIntervalSec.value = s.index?.scanIntervalSec ?? 8
  recentWindowHours.value = s.recent?.windowHours ?? 24
  workspacePath.value = s.workspace?.path || s.workspacePath || ws.info?.workspacePath || ''
}

async function handleSaveSecrets() {
  saving.value = true
  saveError.value = ''
  try {
    await settings.saveSecrets(llmApiKey.value || undefined, embedApiKey.value || undefined, rerankApiKey.value || undefined)
    llmApiKey.value = ''
    embedApiKey.value = ''
    rerankApiKey.value = ''
    savedMsg.value = '密钥已保存'
    setTimeout(() => (savedMsg.value = ''), 3000)
  } catch (e: any) {
    saveError.value = e?.message || '保存密钥失败'
    setTimeout(() => (saveError.value = ''), 5000)
  } finally {
    saving.value = false
  }
}

async function handleSaveSettings() {
  saving.value = true
  saveError.value = ''
  try {
    const restartList = await settings.save({
      'llm.baseUrl': llmBaseUrl.value,
      'llm.model': llmModel.value,
      'llm.temperature': llmTemperature.value,
      'embed.baseUrl': embedBaseUrl.value,
      'embed.model': embedModel.value,
      'embed.dimensions': embedDimensions.value,
      'rerank.baseUrl': rerankBaseUrl.value,
      'rerank.model': rerankModel.value,
      'markitdown.pythonPath': pythonPath.value,
'markitdown.command': command.value,
	      'markitdown.markitdownCmd': markitdownCmd.value,
	      'index.scanIntervalSec': scanIntervalSec.value,
	      'recent.windowHours': recentWindowHours.value,
	      'workspace.path': workspacePath.value ?? undefined,
    })
    // 顺带保存表单里填写的密钥（非空才覆盖，保持"留空不修改"语义）。
    // 修复：此前"保存设置"不保存 API Key，用户测试通过但真正请求仍用旧 key 导致 401。
    const hasKeys = llmApiKey.value || embedApiKey.value || rerankApiKey.value
    if (hasKeys) {
      await settings.saveSecrets(
        llmApiKey.value || undefined,
        embedApiKey.value || undefined,
        rerankApiKey.value || undefined,
      )
      llmApiKey.value = ''
      embedApiKey.value = ''
      rerankApiKey.value = ''
    }
    savedMsg.value =
      restartList && restartList.length > 0 ? '已保存（部分配置需重启应用后生效）' : '已保存'
    setTimeout(() => (savedMsg.value = ''), 5000)
  } catch (e: any) {
    saveError.value = e?.message || '保存设置失败'
    setTimeout(() => (saveError.value = ''), 5000)
  } finally {
    saving.value = false
  }
}

async function handleInitWorkspace() {
  if (!workspacePath.value) {
    initMsg.value = '请先填写工作区路径'
    return
  }
  initing.value = true
  initMsg.value = ''
  try {
    await ws.init({
      workspacePath: workspacePath.value,
      markitdown: {
        pythonPath: pythonPath.value || undefined,
        command: command.value || undefined,
      },
      llm: {
        baseUrl: llmBaseUrl.value,
        apiKey: llmApiKey.value || undefined,
        model: llmModel.value || undefined,
        temperature: llmTemperature.value || undefined,
      },
      embed: {
        baseUrl: embedBaseUrl.value,
        apiKey: embedApiKey.value || undefined,
        model: embedModel.value || undefined,
        dimensions: embedDimensions.value || undefined,
      },
      rerank: {
        baseUrl: rerankBaseUrl.value || undefined,
        apiKey: rerankApiKey.value || undefined,
        model: rerankModel.value || undefined,
      },
    })
    initMsg.value = ws.initialized ? '✓ 工作区已初始化并开始索引' : '✓ 配置已应用'
  } catch (e: any) {
    initMsg.value = `✗ ${e.message}`
  } finally {
    initing.value = false
  }
}

const pickingDir = ref(false)
const pickMsg = ref('')
const pickingMd = ref(false)
const mdPickError = ref('')
async function pickWorkspaceDir() {
  pickingDir.value = true
  pickMsg.value = ''
  try {
    const res = await browsePickDir(workspacePath.value || undefined)
    if (!res.cancelled && res.path) {
      workspacePath.value = res.path
    }
  } catch (e: any) {
    pickMsg.value = `✗ ${e.message}`
    setTimeout(() => (pickMsg.value = ''), 4000)
  } finally {
    pickingDir.value = false
  }
}

const pickingPython = ref(false)
async function pickPythonDir() {
  pickingPython.value = true
  pythonDetectError.value = ''
  try {
    const res = await browsePickDir(undefined)
    if (!res.cancelled && res.path) {
      const dir = res.path
      // 尝试将 dir 视为 python 解释器路径
      const exe = dir.endsWith('.exe') ? dir : `${dir}\\python.exe`
      pythonPath.value = exe
      pythonDetected.value = { path: exe, ok: true, version: '' }
    }
  } catch (e: any) {
    pythonDetectError.value = `✗ ${e.message}`
  } finally {
    pickingPython.value = false
  }
}

async function pickMarkitdownExe() {
  pickingMd.value = true
  mdPickError.value = ''
  try {
    const res = await browsePickDir(undefined)
    if (!res.cancelled && res.path) {
      markitdownCmd.value = res.path
    }
  } catch (e: any) {
    mdPickError.value = `✗ ${e.message}`
  } finally {
    pickingMd.value = false
  }
}

async function runPythonDetect() {
  pythonDetecting.value = true
  pythonDetectError.value = ''
  pythonDetected.value = null
  try {
    const res = await detectPython()
    if (res.ok) {
      pythonDetected.value = res
      if (!pythonPath.value) {
        pythonPath.value = res.path
        command.value = `python -m markitdown "{file}"`
      }
      // 后端已探测 markitdown 路径，直接填入
      if (!markitdownCmd.value && res.markitdownCmd) {
        markitdownCmd.value = res.markitdownCmd
      }
    } else {
      pythonDetectError.value = res.error || '未检测到 Python'
    }
  } catch (e: any) {
    pythonDetectError.value = `✗ ${e.message}`
  } finally {
    pythonDetecting.value = false
  }
}

async function handleTest(type: string) {
  testing.value = type
  if (type === 'chat') testChatResult.value = ''
  else if (type === 'embed') testEmbedResult.value = ''
  else if (type === 'rerank') testRerankResult.value = ''
  else testMarkitdownResult.value = ''
  try {
    if (type === 'markitdown') {
      const res = await testMarkitdown(pythonPath.value || 'python3', command.value || 'markitdown')
      testMarkitdownResult.value = res.ok ? '✓ 测试通过' : `✗ ${res.message}`
    } else if (type === 'embed') {
      const res = await testLLM({
        type: 'embed',
        baseUrl: embedBaseUrl.value,
        model: embedModel.value,
        apiKey: embedApiKey.value || undefined,
      })
      testEmbedResult.value = res.ok ? '✓ 测试通过' : `✗ ${res.message}`
    } else if (type === 'rerank') {
      const res = await testLLM({
        type: 'rerank',
        baseUrl: rerankBaseUrl.value,
        model: rerankModel.value,
        apiKey: rerankApiKey.value || undefined,
      })
      testRerankResult.value = res.ok ? '✓ 测试通过' : `✗ ${res.message}`
    } else {
      const res = await testLLM({
        type: 'chat',
        baseUrl: llmBaseUrl.value,
        model: llmModel.value,
        apiKey: llmApiKey.value || undefined,
        temperature: llmTemperature.value,
      })
      testChatResult.value = res.ok ? '✓ 测试通过' : `✗ ${res.message}`
    }
  } catch (e: any) {
    const msg = `✗ ${e.message}`
    if (type === 'chat') testChatResult.value = msg
    else if (type === 'embed') testEmbedResult.value = msg
    else if (type === 'rerank') testRerankResult.value = msg
    else testMarkitdownResult.value = msg
  } finally {
    testing.value = ''
  }
}
</script>

<template>
  <div class="settings-layout">
    <!-- ───────── 左侧导航 ───────── -->
    <aside class="settings-sidebar">
      <button class="back-btn" @click="goBack">
        <Icon name="arrow-left" :size="14" />
        <span>返回应用</span>
      </button>

      <div class="sidebar-search">
        <Icon name="search" :size="14" class="sidebar-search__icon" />
        <input
          v-model="searchQuery"
          class="sidebar-search__input"
          placeholder="搜索设置…"
        />
      </div>

      <nav class="settings-nav">
        <div v-for="section in filteredNavSections" :key="section.label" class="nav-group">
          <div class="nav-group__label">{{ section.label }}</div>
          <button
            v-for="item in section.items"
            :key="item.id"
            class="nav-item"
            :class="{ 'nav-item--active': activeId === item.id }"
            @click="scrollTo(item.id)"
          >
            <Icon :name="item.icon" :size="15" class="nav-item__icon" />
            <span class="nav-item__label">{{ item.label }}</span>
          </button>
        </div>
        <div
          v-if="filteredNavSections.length === 0"
          class="nav-empty"
        >
          没有匹配的设置项
        </div>
      </nav>
    </aside>

    <!-- ───────── 主内容 ───────── -->
    <main class="settings-main">
      <div class="settings-main__scroll">
        <header class="page-header">
          <div>
            <h2>设置</h2>
            <p class="page-sub">工作区、文档提取、嵌入与大语言模型</p>
          </div>
          <div v-if="savedMsg" class="saved-toast">
            <Icon name="check" :size="14" />
            {{ savedMsg }}
          </div>
          <div v-else-if="saveError" class="saved-toast saved-toast--err">
            <Icon name="x" :size="14" />
            {{ saveError }}
          </div>
        </header>

        <div v-if="settings.loading" class="loading">加载中…</div>

        <div v-else-if="settings.error" class="settings-error card">
          <span>设置加载失败：{{ settings.error }}</span>
          <button class="btn btn-ghost btn-sm" @click="retryLoadSettings">
            <Icon name="refresh" :size="14" />
            重试
          </button>
        </div>

        <div v-else class="settings-content">
          <!-- ── 工作区 ── -->
          <section id="sec-workspace" class="settings-section">
            <h3 class="settings-section__title">工作区</h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">路径</div>
                  <div class="settings-row__desc">选择要纳入管理的文档目录，初始化后自动开始索引</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="workspacePath"
                    class="input"
                    placeholder="D:/docs"
                  />
                  <button class="btn btn-ghost btn-sm" :disabled="pickingDir" @click="pickWorkspaceDir">
                    {{ pickingDir ? '选择中…' : '选择目录' }}
                  </button>
                </div>
              </div>
              <span v-if="pickMsg" class="settings-row__error">{{ pickMsg }}</span>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">状态</div>
                  <div class="settings-row__desc">工作区是否已初始化（Git 仓库 + 数据库就绪）</div>
                </div>
                <div class="settings-row__control">
                  <span v-if="ws.info" class="ws-status" :class="{ 'ws-status--ready': ws.initialized }">
                    <span class="ws-status__dot"></span>
                    {{ ws.initialized ? '已初始化' : '未初始化' }}
                  </span>
                </div>
              </div>

              <div class="settings-row settings-row--action">
                <div class="settings-row__text"></div>
                <div class="settings-row__control settings-row__control--action">
                  <button class="btn btn-primary btn-sm" :disabled="initing" @click="handleInitWorkspace">
                    {{ initing ? '初始化中…' : '初始化 / 应用工作区' }}
                  </button>
                </div>
              </div>
              <div v-if="initMsg" class="settings-row__msg" :class="{ 'settings-row__msg--ok': initMsg.startsWith('✓') }">
                {{ initMsg }}
              </div>
            </div>
          </section>

          <!-- ── MarkItDown ── -->
          <section id="sec-markitdown" class="settings-section">
            <h3 class="settings-section__title">文档提取</h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">Python 路径</div>
                  <div class="settings-row__desc">指向可调用 markitdown 的 Python 解释器</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <div v-if="pythonDetected && pythonDetected.ok" class="python-detected-chip">
                    <span class="python-detected-chip__dot python-detected-chip__dot--ok"></span>
                    <span class="python-detected-chip__info">
                      <span class="python-detected-chip__version">Python {{ pythonDetected.version }}</span>
                      <span class="python-detected-chip__path" :title="pythonDetected.path">{{ pythonDetected.path }}</span>
                    </span>
                    <button class="python-detected-chip__refresh" title="重新检测" @click="runPythonDetect">
                      <Icon name="refresh" :size="12" />
                    </button>
                  </div>
                  <div v-else-if="pythonDetecting" class="python-detected-chip">
                    <span class="python-detected-chip__dot python-detected-chip__dot--busy"></span>
                    <span class="python-detected-chip__version">正在检测…</span>
                  </div>
                  <div v-else class="python-detected-chip">
                    <span class="python-detected-chip__dot python-detected-chip__dot--err"></span>
                    <span class="python-detected-chip__version">未检测到 Python</span>
                    <button class="python-detected-chip__refresh" title="重新检测" @click="runPythonDetect">
                      <Icon name="refresh" :size="12" />
                    </button>
                  </div>
                  <input
                    v-model="pythonPath"
                    class="input"
                    placeholder="python3"
                  />
                  <button
                    class="btn btn-ghost btn-sm"
                    :disabled="pickingPython"
                    @click="pickPythonDir"
                  >
                    {{ pickingPython ? '选择中…' : '手动选择' }}
                  </button>
                </div>
              </div>
              <span v-if="pythonDetectError" class="settings-row__error">{{ pythonDetectError }}</span>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">MarkItDown 路径</div>
                  <div class="settings-row__desc">直接指定 markitdown 可执行文件路径（优先于 Python 路径），留空则使用 Python 命令</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="markitdownCmd"
                    class="input"
                    placeholder="如 C:\Python312\Scripts\markitdown.exe，或 python -m markitdown"
                  />
                  <button
                    class="btn btn-ghost btn-sm"
                    :disabled="pickingMd"
                    @click="pickMarkitdownExe"
                  >
                    {{ pickingMd ? '选择中…' : '浏览' }}
                  </button>
                </div>
              </div>
              <span v-if="mdPickError" class="settings-row__error">{{ mdPickError }}</span>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">命令模板</div>
                  <div class="settings-row__desc">使用 <code>{file}</code> 占位表示待提取文件路径</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="command"
                    class="input"
                    placeholder='python -m markitdown "{file}"'
                  />
                </div>
              </div>

              <div class="settings-row settings-row--action">
                <div class="settings-row__text"></div>
                <div class="settings-row__control settings-row__control--action">
                  <button
                    class="btn btn-ghost btn-sm"
                    :disabled="testing === 'markitdown'"
                    @click="handleTest('markitdown')"
                  >
                    {{ testing === 'markitdown' ? '测试中…' : '测试 MarkItDown' }}
                  </button>
                  <span
                    v-if="testMarkitdownResult"
                    class="settings-row__msg settings-row__msg--inline"
                    :class="{
                      'settings-row__msg--ok': testMarkitdownResult.startsWith('✓'),
                    }"
                  >
                    {{ testMarkitdownResult }}
                  </span>
                </div>
              </div>
            </div>
          </section>

          <!-- ── 索引 ── -->
          <section id="sec-index" class="settings-section">
            <h3 class="settings-section__title">索引</h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">扫描间隔</div>
                  <div class="settings-row__desc">自动检测新文件并加入索引队列的间隔（秒），修改后立即生效</div>
                </div>
                <div class="settings-row__control settings-row__control--narrow">
                  <input
                    v-model.number="scanIntervalSec"
                    class="input"
                    type="number"
                    min="2"
                    max="120"
                  />
                </div>
              </div>
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">最近文件时间窗</div>
                  <div class="settings-row__desc">「最近文件」页展示的时间范围，修改后实时生效</div>
                </div>
                <div class="settings-row__control settings-row__control--narrow">
                  <select v-model.number="recentWindowHours" class="select">
                    <option :value="5">最近 5 小时</option>
                    <option :value="24">最近 24 小时</option>
                    <option :value="168">最近 7 天</option>
                    <option :value="0">全部</option>
                  </select>
                </div>
              </div>
            </div>
          </section>

          <!-- ── Embedding ── -->
          <section id="sec-embed" class="settings-section">
            <h3 class="settings-section__title">嵌入模型</h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">接口地址</div>
                  <div class="settings-row__desc">OpenAI 兼容的 <code>/embeddings</code> 端点</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="embedBaseUrl"
                    class="input"
                    placeholder="https://api.openai.com/v1"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">API Key</div>
                  <div class="settings-row__desc">留空不修改已保存的密钥</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="embedApiKey"
                    class="input"
                    type="password"
                    placeholder="留空不修改"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">模型</div>
                  <div class="settings-row__desc">用于文档语义向量化的嵌入模型名</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="embedModel"
                    class="input"
                    placeholder="text-embedding-3-small"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">向量维度</div>
                  <div class="settings-row__desc">必须与所选模型输出的维度一致（修改后请重建索引）</div>
                </div>
                <div class="settings-row__control settings-row__control--narrow">
                  <input
                    v-model.number="embedDimensions"
                    class="input"
                    type="number"
                    min="64"
                  />
                </div>
              </div>

              <div class="settings-row settings-row--action">
                <div class="settings-row__text"></div>
                <div class="settings-row__control settings-row__control--action">
                  <button
                    class="btn btn-ghost btn-sm"
                    :disabled="testing === 'embed'"
                    @click="handleTest('embed')"
                  >
                    {{ testing === 'embed' ? '测试中…' : '测试 Embed' }}
                  </button>
                  <span
                    v-if="testEmbedResult"
                    class="settings-row__msg settings-row__msg--inline"
                    :class="{ 'settings-row__msg--ok': testEmbedResult.startsWith('✓') }"
                  >
                    {{ testEmbedResult }}
                  </span>
                </div>
              </div>
            </div>
          </section>

          <!-- ── Rerank ── -->
          <section id="sec-rerank" class="settings-section">
            <h3 class="settings-section__title">重排模型</h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">接口地址</div>
                  <div class="settings-row__desc">Rerank 端点（Jina / Cohere / SiliconFlow 兼容），留空则不启用重排</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="rerankBaseUrl"
                    class="input"
                    placeholder="https://api.siliconflow.cn/v1"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">API Key</div>
                  <div class="settings-row__desc">留空不修改已保存的密钥</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="rerankApiKey"
                    class="input"
                    type="password"
                    placeholder="留空不修改"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">模型</div>
                  <div class="settings-row__desc">交叉编码器重排模型，用于问答与搜索的候选精排</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="rerankModel"
                    class="input"
                    placeholder="Pro/BAAI/bge-reranker-v2-m3"
                  />
                </div>
              </div>

              <div class="settings-row settings-row--action">
                <div class="settings-row__text"></div>
                <div class="settings-row__control settings-row__control--action">
                  <button
                    class="btn btn-ghost btn-sm"
                    :disabled="testing === 'rerank'"
                    @click="handleTest('rerank')"
                  >
                    {{ testing === 'rerank' ? '测试中…' : '测试 Rerank' }}
                  </button>
                  <span
                    v-if="testRerankResult"
                    class="settings-row__msg settings-row__msg--inline"
                    :class="{ 'settings-row__msg--ok': testRerankResult.startsWith('✓') }"
                  >
                    {{ testRerankResult }}
                  </span>
                </div>
              </div>
            </div>
          </section>

          <!-- ── LLM ── -->
          <section id="sec-llm" class="settings-section">
            <h3 class="settings-section__title">大语言模型</h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">接口地址</div>
                  <div class="settings-row__desc">OpenAI 兼容的 <code>/chat/completions</code> 端点</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="llmBaseUrl"
                    class="input"
                    placeholder="https://api.openai.com/v1"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">API Key</div>
                  <div class="settings-row__desc">留空不修改已保存的密钥</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="llmApiKey"
                    class="input"
                    type="password"
                    placeholder="留空不修改"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">模型</div>
                  <div class="settings-row__desc">用于自动打标、提交摘要与文档问答</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="llmModel"
                    class="input"
                    placeholder="gpt-4o-mini"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">Temperature</div>
                  <div class="settings-row__desc">值越低越稳定，越高越发散（0–2）</div>
                </div>
                <div class="settings-row__control settings-row__control--narrow">
                  <input
                    v-model.number="llmTemperature"
                    class="input"
                    type="number"
                    step="0.1"
                    min="0"
                    max="2"
                  />
                </div>
              </div>

              <div class="settings-row settings-row--action">
                <div class="settings-row__text"></div>
                <div class="settings-row__control settings-row__control--action">
                  <button
                    class="btn btn-ghost btn-sm"
                    :disabled="testing === 'chat'"
                    @click="handleTest('chat')"
                  >
                    {{ testing === 'chat' ? '测试中…' : '测试 Chat' }}
                  </button>
                  <span
                    v-if="testChatResult"
                    class="settings-row__msg settings-row__msg--inline"
                    :class="{ 'settings-row__msg--ok': testChatResult.startsWith('✓') }"
                  >
                    {{ testChatResult }}
                  </span>
                </div>
              </div>
            </div>
          </section>

          <!-- ── 底部操作 ── -->
          <div class="settings-footer">
            <button
              class="btn btn-primary"
              :disabled="saving || !formReady"
              @click="handleSaveSettings"
            >
              <Icon name="check" :size="14" />
              {{ saving ? '保存中…' : '保存设置' }}
            </button>
            <button
              class="btn btn-ghost"
              :disabled="saving || !formReady"
              @click="handleSaveSecrets"
            >
              仅保存密钥
            </button>
          </div>
        </div>
      </div>
    </main>
  </div>
</template>

<style scoped>
.settings-layout {
  display: flex;
  height: 100%;
  overflow: hidden;
  background: var(--c-bg-page);
}

/* ───────── 左侧导航 ───────── */
.settings-sidebar {
  width: 244px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  padding: 14px 10px 16px;
  background: var(--c-bg-page);
  border-right: 1px solid var(--c-border);
  overflow-y: auto;
}

.back-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 8px;
  font-size: 13px;
  color: var(--c-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--r-md);
  cursor: pointer;
  transition: background 0.12s, color 0.12s;
  align-self: flex-start;
  margin-bottom: 10px;
}
.back-btn:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}

.sidebar-search {
  position: relative;
  margin-bottom: 14px;
}
.sidebar-search__icon {
  position: absolute;
  left: 10px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--c-text-tertiary);
  pointer-events: none;
}
.sidebar-search__input {
  width: 100%;
  padding: 7px 10px 7px 30px;
  background: var(--c-bg-panel);
  border: 1px solid var(--c-border);
  border-radius: var(--r-md);
  color: var(--c-text-primary);
  font-size: 13px;
  outline: none;
  transition: border-color 0.15s;
  font-family: inherit;
  box-sizing: border-box;
}
.sidebar-search__input::placeholder {
  color: var(--c-text-tertiary);
}
.sidebar-search__input:focus {
  border-color: var(--c-border-strong);
}

.settings-nav {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.nav-group {
  display: flex;
  flex-direction: column;
}
.nav-group__label {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.06em;
  color: var(--c-text-tertiary);
  padding: 0 10px 6px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 10px;
  font-size: 13.5px;
  color: var(--c-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--r-md);
  cursor: pointer;
  text-align: left;
  transition: background 0.12s, color 0.12s;
  font-family: inherit;
  width: 100%;
}
.nav-item:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}
.nav-item--active {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}
.nav-item__icon {
  color: var(--c-icon-secondary);
  flex-shrink: 0;
}
.nav-item--active .nav-item__icon {
  color: var(--c-text-primary);
}
.nav-item__label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.nav-empty {
  font-size: 12.5px;
  color: var(--c-text-tertiary);
  padding: 8px 10px;
  text-align: center;
}

/* ───────── 主内容区 ───────── */
.settings-main {
  flex: 1;
  min-width: 0;
  overflow: hidden;
}
.settings-main__scroll {
  height: 100%;
  overflow-y: auto;
  padding: 28px 36px 40px;
}

.page-header {
  margin-bottom: 18px;
}
.page-header h2 {
  font-size: 20px;
  font-weight: 700;
  margin: 0;
}
.page-sub {
  font-size: 13px;
  color: var(--c-text-tertiary);
  margin: 2px 0 0;
}

.saved-toast {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--c-success);
  font-weight: 500;
  padding: 6px 12px;
  border-radius: var(--r-md);
  background: var(--c-success-soft);
}

.saved-toast--err {
  color: var(--c-danger);
  background: var(--c-danger-soft);
}

.settings-error {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  color: var(--c-danger);
  border-color: var(--c-danger);
}

.settings-content {
  max-width: 880px;
}

.settings-section {
  margin-bottom: 22px;
}
.settings-section__title {
  font-size: 15px;
  font-weight: 700;
  color: var(--c-text-primary);
  margin: 0 0 10px;
}

.settings-card {
  padding: 4px 18px;
}

.settings-row {
  display: flex;
  align-items: center;
  gap: 24px;
  padding: 14px 0;
  border-bottom: 1px solid var(--c-border);
}
.settings-row:last-child {
  border-bottom: none;
}
.settings-row--action {
  border-bottom: none;
  padding-top: 8px;
}

.settings-row__text {
  flex: 1;
  min-width: 0;
}
.settings-row__title {
  font-size: 14px;
  font-weight: 500;
  color: var(--c-text-primary);
  margin-bottom: 2px;
}
.settings-row__desc {
  font-size: 12.5px;
  color: var(--c-text-tertiary);
  line-height: 1.5;
}
.settings-row__desc code {
  font-family: var(--font-mono);
  font-size: 11.5px;
  padding: 1px 5px;
  border-radius: var(--r-xs);
  background: var(--c-bg-elevated);
  color: var(--c-text-secondary);
}

.settings-row__control {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 320px;
  max-width: 100%;
}
.settings-row__control--wide {
  flex: 0 0 380px;
}
.settings-row__control--narrow {
  flex: 0 0 120px;
}
.settings-row__control--action {
  flex: 0 0 auto;
}

.settings-row__error {
  display: block;
  padding: 4px 0 10px;
  font-size: 12.5px;
  color: var(--c-danger);
}

.settings-row__msg {
  font-size: 13px;
  color: var(--c-danger);
  padding: 4px 0 6px;
}
.settings-row__msg--ok {
  color: var(--c-success);
}
.settings-row__msg--inline {
  padding: 0;
  font-size: 12.5px;
}

.ws-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--c-text-tertiary);
}
.ws-status__dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--c-text-tertiary);
}
.ws-status--ready {
  color: var(--c-success);
}
.ws-status--ready .ws-status__dot {
  background: var(--c-success);
  box-shadow: 0 0 0 3px var(--c-success-soft);
}

.python-detected-chip {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  padding: 6px 12px;
  border-radius: var(--r-md);
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  font-size: 12px;
  margin-bottom: 8px;
  width: 100%;
  max-width: 100%;
}
.python-detected-chip__dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.python-detected-chip__dot--ok {
  background: var(--c-success);
  box-shadow: 0 0 0 3px var(--c-success-soft);
}
.python-detected-chip__dot--busy {
  background: var(--c-info);
  box-shadow: 0 0 0 3px var(--c-info-soft);
}
.python-detected-chip__dot--err {
  background: var(--c-text-tertiary);
}
.python-detected-chip__refresh {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: var(--r-sm);
  border: none;
  background: transparent;
  color: var(--c-text-tertiary);
  cursor: pointer;
  flex-shrink: 0;
  padding: 0;
  margin: 0;
  transition: background 0.1s, color 0.1s;
}
.python-detected-chip__refresh:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}
.python-detected-chip__info {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  flex: 1;
}
.python-detected-chip__version {
  font-weight: 700;
  color: var(--c-success);
  font-size: 13px;
  white-space: nowrap;
}
.python-detected-chip__path {
  color: var(--c-text-tertiary);
  font-family: var(--font-mono, monospace);
  font-size: 11.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.settings-footer {
  display: flex;
  gap: 8px;
  margin-top: 8px;
  padding-top: 18px;
  border-top: 1px solid var(--c-border);
}

/* ───────── 响应式 ───────── */
@media (max-width: 900px) {
  .settings-row {
    flex-direction: column;
    align-items: stretch;
    gap: 10px;
  }
  .settings-row__control,
  .settings-row__control--wide,
  .settings-row__control--narrow {
    flex: 0 0 auto;
  }
}
</style>