<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { useSettingsStore } from '@/stores/settings'
import { useWorkspaceStore } from '@/stores/workspace'
import Icon, { type IconName } from '@/components/Icon.vue'
import OnboardingWizard from '@/components/OnboardingWizard.vue'
import { useProviderSettings } from '@/composables/useProviderSettings'
import { useSettings } from '@/composables/useSettings'
import { PROVIDER_PRESETS, CUSTOM_PROVIDER_ID } from '@/data/providerModel'

const settings = useSettingsStore()
const ws = useWorkspaceStore()
const router = useRouter()

// ───── 业务状态（P2-05 抽取）─────
// 模型/服务商区块由 useProviderSettings 协调（providerModel 状态机），
// 表单/加载/保存/测试等由 useSettings 持有；本页只编排视图与提示文案。
const providers = useProviderSettings()
const {
  chat,
  embed,
  rerank,
  fetchingModels,
  handleFetchModels,
  llmModelOptions,
  embedModelOptions,
  rerankModelOptions,
  llmUseSelect,
  embedUseSelect,
  rerankUseSelect,
} = providers
const {
  pythonDetected,
  pythonDetecting,
  pythonDetectError,
  markitdownDetected,
  markitdownDetecting,
  markitdownDetectError,
  workspacePath,
  scanIntervalSec,
  recentWindowHours,
  autoCommitEnabled,
  autoCommitDebounceSec,
  testing,
  testChatResult,
  testEmbedResult,
  testRerankResult,
  testMarkitdownResult,
  saving,
  savedMsg,
  saveError,
  initing,
  initMsg,
  formReady,
  showWizard,
  onWizardDone,
  initialize,
  retryLoadSettings,
  runPythonDetect,
  runMarkitdownDetect,
  handleSaveSecrets,
  handleSaveSettings,
  handleInitWorkspace,
  pickPythonDir,
  pickWorkspaceDir,
  pickMarkitdownExe,
  pickingDir,
  pickMsg,
  pickingMd,
  mdPickError,
  pickingPython,
  handleTest,
} = useSettings(providers)

// ───── 侧边栏导航（纯页面 UI 状态）─────
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
    label: '基础',
    items: [
      { id: 'sec-workspace', label: '文件管理', icon: 'folder' },
      { id: 'sec-markitdown', label: '文档提取', icon: 'file' },
      { id: 'sec-index', label: '自动整理', icon: 'search' },
    ],
  },
  {
    label: 'AI 模型',
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

// ───── 生命周期：业务初始化（composable 内）+ 滚动高亮观察器（视图）─────
let observer: IntersectionObserver | null = null

onMounted(async () => {
  await initialize()
  if (!formReady.value) return

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
            <p class="page-sub">管理文件夹、连接 AI、自动整理</p>
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
          <!-- 首次使用引导卡 -->
          <div v-if="!ws.initialized" class="onboard-card card">
            <div class="onboard-card__icon"><Icon name="memory" :size="22" /></div>
            <div class="onboard-card__text">
              <strong>第一次使用？跟着 3 步开始</strong>
              <span>选择文件夹 → 连接 AI（可选）→ 开始使用。整个过程几分钟，随时可以回来改。</span>
            </div>
            <button class="btn btn-primary btn-sm" @click="showWizard = true">
              开始使用 <Icon name="arrow-right" :size="14" />
            </button>
          </div>

          <!-- ── 工作区 ── -->
          <section id="sec-workspace" class="settings-section">
            <h3 class="settings-section__title">工作区</h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">要管理的文件夹</div>
                  <div class="settings-row__desc">这个文件夹里的文件会被自动记录版本、随时找回。选好后请点下方「开始使用 / 应用」按钮才会生效（仅点保存不会应用）</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="workspacePath"
                    class="input"
                    placeholder="选择或粘贴文件夹路径，如 D:/docs"
                  />
                  <button class="btn btn-ghost btn-sm" :disabled="pickingDir" @click="pickWorkspaceDir">
                    {{ pickingDir ? '选择中…' : '选择文件夹' }}
                  </button>
                </div>
              </div>
              <span v-if="pickMsg" class="settings-row__error">{{ pickMsg }}</span>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">状态</div>
                  <div class="settings-row__desc">是否已把该文件夹纳入管理</div>
                </div>
                <div class="settings-row__control">
                  <span v-if="ws.info" class="ws-status" :class="{ 'ws-status--ready': ws.initialized }">
                    <span class="ws-status__dot"></span>
                    {{ ws.initialized ? '已开始使用' : '尚未开始' }}
                  </span>
                </div>
              </div>

              <div class="settings-row settings-row--action">
                <div class="settings-row__text"></div>
                <div class="settings-row__control settings-row__control--action">
                  <button class="btn btn-primary btn-sm" :disabled="initing" @click="handleInitWorkspace">
                    {{ initing ? '处理中…' : (ws.initialized ? '重新应用文件夹' : '开始使用') }}
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
                  <div class="settings-row__desc">本机 Python 解释器，用于运行 MarkItDown</div>
                </div>
                <div class="settings-row__control settings-row__control--wide python-control">
                  <div class="python-control__main">
                    <div v-if="pythonDetected && pythonDetected.ok" class="python-detected-chip">
                      <span class="python-detected-chip__dot python-detected-chip__dot--ok"></span>
                      <span class="python-detected-chip__info">
                        <span class="python-detected-chip__version">
                          <span class="python-detected-chip__version-name">Python</span>
                          <span class="python-detected-chip__version-num">{{ pythonDetected.version }}</span>
                        </span>
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
                    <button
                      class="btn btn-ghost btn-sm python-control__pick"
                      :disabled="pickingPython"
                      @click="pickPythonDir"
                    >
                      {{ pickingPython ? '选择中…' : '手动选择' }}
                    </button>
                  </div>
                </div>
              </div>
              <span v-if="pythonDetectError" class="settings-row__error">{{ pythonDetectError }}</span>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">MarkItDown</div>
                  <div class="settings-row__desc">文档转 Markdown 提取工具</div>
                </div>
                <div class="settings-row__control settings-row__control--wide python-control">
                  <div class="python-control__main">
                    <div v-if="markitdownDetected && markitdownDetected.ok" class="python-detected-chip">
                      <span class="python-detected-chip__dot python-detected-chip__dot--ok"></span>
                      <span class="python-detected-chip__info">
                        <span class="python-detected-chip__version">
                          <span class="python-detected-chip__version-name">MarkItDown</span>
                          <span class="python-detected-chip__version-num">{{ markitdownDetected.version || '已手动选择' }}</span>
                        </span>
                        <span class="python-detected-chip__path" :title="markitdownDetected.path || '随 Python 使用'">{{ markitdownDetected.path || '随 Python 使用' }}</span>
                      </span>
                      <button class="python-detected-chip__refresh" title="重新检测" @click="runMarkitdownDetect">
                        <Icon name="refresh" :size="12" />
                      </button>
                    </div>
                    <div v-else-if="markitdownDetecting" class="python-detected-chip">
                      <span class="python-detected-chip__dot python-detected-chip__dot--busy"></span>
                      <span class="python-detected-chip__version">正在检测…</span>
                    </div>
                    <div v-else class="python-detected-chip">
                      <span class="python-detected-chip__dot python-detected-chip__dot--err"></span>
                      <span class="python-detected-chip__version">未检测到 MarkItDown</span>
                      <button class="python-detected-chip__refresh" title="重新检测" @click="runMarkitdownDetect">
                        <Icon name="refresh" :size="12" />
                      </button>
                    </div>
                    <button
                      class="btn btn-ghost btn-sm python-control__pick"
                      :disabled="pickingMd"
                      @click="pickMarkitdownExe"
                    >
                      {{ pickingMd ? '选择中…' : '手动选择' }}
                    </button>
                  </div>
                </div>
              </div>
              <span v-if="markitdownDetectError" class="settings-row__error">{{ markitdownDetectError }}</span>
              <span v-if="mdPickError" class="settings-row__error">{{ mdPickError }}</span>

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

          <!-- ── 自动整理（索引） ── -->
          <section id="sec-index" class="settings-section">
            <h3 class="settings-section__title">自动整理</h3>
            <div class="settings-card card">
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
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">自动保存版本</div>
                  <div class="settings-row__desc">文件变更时自动提交 Git 历史，方便随时找回旧版本</div>
                </div>
                <div class="settings-row__control settings-row__control--narrow">
                  <label class="switch">
                    <input v-model="autoCommitEnabled" type="checkbox" />
                    <span class="switch__slider"></span>
                  </label>
                </div>
              </div>
              <details class="settings-advanced">
                <summary>高级选项（一般无需修改）</summary>
                <div class="settings-advanced__body">
                  <div class="settings-row">
                    <div class="settings-row__text">
                      <div class="settings-row__title">自动保存间隔（秒）</div>
                      <div class="settings-row__desc">批量改动合并成一次提交的等待时间</div>
                    </div>
                    <div class="settings-row__control settings-row__control--narrow">
                      <input
                        v-model.number="autoCommitDebounceSec"
                        class="input"
                        type="number"
                        min="1"
                        max="3600"
                      />
                    </div>
                  </div>
                  <div class="settings-row">
                    <div class="settings-row__text">
                      <div class="settings-row__title">扫描间隔（秒）</div>
                      <div class="settings-row__desc">自动检测新文件并加入整理队列的间隔</div>
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
                </div>
              </details>
            </div>
          </section>

          <!-- ── 嵌入模型 ── -->
          <section id="sec-embed" class="settings-section">
            <h3 class="settings-section__title">嵌入模型 <span class="section-tag">可选</span></h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">选择服务商</div>
                  <div class="settings-row__desc">用于文档内容向量化、按内容搜索；选好后一般只需填写 API Key</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <select
                    :value="embed.state.providerId"
                    class="select"
                    @change="(e) => embed.applyPreset((e.target as HTMLSelectElement).value)"
                  >
                    <option v-for="p in PROVIDER_PRESETS" :key="p.id" :value="p.id">{{ p.name }}</option>
                    <option :value="CUSTOM_PROVIDER_ID">自定义（手动填写）</option>
                  </select>
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">接口地址</div>
                  <div class="settings-row__desc">OpenAI 兼容的 <code>/embeddings</code> 端点，一般已由服务商自动填好</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="embed.state.baseUrl"
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
                    v-model="embed.state.apiKey"
                    class="input"
                    type="password"
                    placeholder="留空不修改"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">模型</div>
                  <div class="settings-row__desc">点「获取模型」拉取该服务的可用模型，从下拉选择</div>
                </div>
                <div class="settings-row__control settings-row__control--wide python-control">
                  <div class="python-detected-chip model-chip">
                    <span
                      class="python-detected-chip__dot"
                      :class="embed.state.model ? 'python-detected-chip__dot--ok' : 'python-detected-chip__dot--warn'"
                    ></span>
                    <span
                      class="python-detected-chip__version"
                      :class="{ 'python-detected-chip__version--warn': !embed.state.model }"
                      :title="embed.state.model || '未选择模型'"
                    >
                      {{ embed.state.model || '未选择模型' }}
                    </span>
                  </div>
                  <div class="python-control__foot">
                    <select
                      v-if="embedUseSelect"
                      v-model="embed.state.model"
                      class="select"
                    >
                      <option v-for="m in embedModelOptions" :key="m" :value="m">{{ m }}</option>
                    </select>
                    <input
                      v-else
                      v-model="embed.state.model"
                      class="input"
                      placeholder="text-embedding-3-small"
                    />
                    <button
                      class="btn btn-ghost btn-sm"
                      :disabled="fetchingModels === 'embed'"
                      @click="handleFetchModels('embed')"
                    >
                      {{ fetchingModels === 'embed' ? '获取中…' : '获取模型' }}
                    </button>
                  </div>
                </div>
              </div>

              <details class="settings-advanced">
                <summary>高级选项（一般无需修改）</summary>
                <div class="settings-advanced__body">
                  <div class="settings-row">
                    <div class="settings-row__text">
                      <div class="settings-row__title">向量维度</div>
                      <div class="settings-row__desc">与所选模型的输出维度一致；切换模型后请重新整理</div>
                    </div>
                    <div class="settings-row__control settings-row__control--narrow">
                      <input
                        v-model.number="embed.state.dimensions"
                        class="input"
                        type="number"
                        min="64"
                      />
                    </div>
                  </div>
                </div>
              </details>

              <div class="settings-row settings-row--action">
                <div class="settings-row__text"></div>
                <div class="settings-row__control settings-row__control--action">
                  <button
                    class="btn btn-ghost btn-sm"
                    :disabled="testing === 'embed'"
                    @click="handleTest('embed')"
                  >
                    {{ testing === 'embed' ? '测试中…' : '测试连接' }}
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
              <span v-if="embed.state.modelsError" class="settings-row__error">{{ embed.state.modelsError }}</span>
            </div>
          </section>

          <!-- ── 重排模型 ── -->
          <section id="sec-rerank" class="settings-section">
            <h3 class="settings-section__title">重排模型 <span class="section-tag">可选</span></h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">选择服务商</div>
                  <div class="settings-row__desc">用于让问答/搜索的候选结果更准确；留空表示不启用</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <select
                    :value="rerank.state.providerId"
                    class="select"
                    @change="(e) => rerank.applyPreset((e.target as HTMLSelectElement).value)"
                  >
                    <option v-for="p in PROVIDER_PRESETS" :key="p.id" :value="p.id">{{ p.name }}</option>
                    <option :value="CUSTOM_PROVIDER_ID">自定义（手动填写）</option>
                  </select>
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">接口地址</div>
                  <div class="settings-row__desc">Rerank 端点，留空则不启用排序优化</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="rerank.state.baseUrl"
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
                    v-model="rerank.state.apiKey"
                    class="input"
                    type="password"
                    placeholder="留空不修改"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">模型</div>
                  <div class="settings-row__desc">点「获取模型」拉取该服务的可用模型，从下拉选择</div>
                </div>
                <div class="settings-row__control settings-row__control--wide python-control">
                  <div class="python-detected-chip model-chip">
                    <span
                      class="python-detected-chip__dot"
                      :class="rerank.state.model ? 'python-detected-chip__dot--ok' : 'python-detected-chip__dot--warn'"
                    ></span>
                    <span
                      class="python-detected-chip__version"
                      :class="{ 'python-detected-chip__version--warn': !rerank.state.model }"
                      :title="rerank.state.model || '未选择模型'"
                    >
                      {{ rerank.state.model || '未选择模型' }}
                    </span>
                  </div>
                  <div class="python-control__foot">
                    <select
                      v-if="rerankUseSelect"
                      v-model="rerank.state.model"
                      class="select"
                    >
                      <option v-for="m in rerankModelOptions" :key="m" :value="m">{{ m }}</option>
                    </select>
                    <input
                      v-else
                      v-model="rerank.state.model"
                      class="input"
                      placeholder="Pro/BAAI/bge-reranker-v2-m3"
                    />
                    <button
                      class="btn btn-ghost btn-sm"
                      :disabled="fetchingModels === 'rerank'"
                      @click="handleFetchModels('rerank')"
                    >
                      {{ fetchingModels === 'rerank' ? '获取中…' : '获取模型' }}
                    </button>
                  </div>
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
                    {{ testing === 'rerank' ? '测试中…' : '测试连接' }}
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
              <span v-if="rerank.state.modelsError" class="settings-row__error">{{ rerank.state.modelsError }}</span>
            </div>
          </section>

          <!-- ── 大语言模型（LLM） ── -->
          <section id="sec-llm" class="settings-section">
            <h3 class="settings-section__title">大语言模型</h3>
            <div class="settings-card card">
              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">选择服务商</div>
                  <div class="settings-row__desc">用于对话问答、自动标签、提交说明；选好后一般只需填写 API Key</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <select
                    :value="chat.state.providerId"
                    class="select"
                    @change="(e) => chat.applyPreset((e.target as HTMLSelectElement).value)"
                  >
                    <option v-for="p in PROVIDER_PRESETS" :key="p.id" :value="p.id">{{ p.name }}</option>
                    <option :value="CUSTOM_PROVIDER_ID">自定义（手动填写）</option>
                  </select>
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">接口地址</div>
                  <div class="settings-row__desc">OpenAI 兼容的 <code>/chat/completions</code> 端点，一般已由服务商自动填好</div>
                </div>
                <div class="settings-row__control settings-row__control--wide">
                  <input
                    v-model="chat.state.baseUrl"
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
                    v-model="chat.state.apiKey"
                    class="input"
                    type="password"
                    placeholder="留空不修改"
                  />
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">模型</div>
                  <div class="settings-row__desc">点「获取模型」拉取该服务的可用模型，从下拉选择</div>
                </div>
                <div class="settings-row__control settings-row__control--wide python-control">
                  <div class="python-detected-chip model-chip">
                    <span
                      class="python-detected-chip__dot"
                      :class="chat.state.model ? 'python-detected-chip__dot--ok' : 'python-detected-chip__dot--warn'"
                    ></span>
                    <span
                      class="python-detected-chip__version"
                      :class="{ 'python-detected-chip__version--warn': !chat.state.model }"
                      :title="chat.state.model || '未选择模型'"
                    >
                      {{ chat.state.model || '未选择模型' }}
                    </span>
                  </div>
                  <div class="python-control__foot">
                    <select
                      v-if="llmUseSelect"
                      v-model="chat.state.model"
                      class="select"
                    >
                      <option v-for="m in llmModelOptions" :key="m" :value="m">{{ m }}</option>
                    </select>
                    <input
                      v-else
                      v-model="chat.state.model"
                      class="input"
                      placeholder="gpt-4o-mini"
                    />
                    <button
                      class="btn btn-ghost btn-sm"
                      :disabled="fetchingModels === 'chat'"
                      @click="handleFetchModels('chat')"
                    >
                      {{ fetchingModels === 'chat' ? '获取中…' : '获取模型' }}
                    </button>
                  </div>
                </div>
              </div>

              <div class="settings-row">
                <div class="settings-row__text">
                  <div class="settings-row__title">Temperature</div>
                  <div class="settings-row__desc">值越低越稳定，越高越发散（0–2）</div>
                </div>
                <div class="settings-row__control settings-row__control--narrow">
                  <input
                    v-model.number="chat.state.temperature"
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
                    {{ testing === 'chat' ? '测试中…' : '测试连接' }}
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
              <span v-if="chat.state.modelsError" class="settings-row__error">{{ chat.state.modelsError }}</span>
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

    <!-- 首次使用向导 -->
    <OnboardingWizard :visible="showWizard" @done="onWizardDone" />
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

/* 首次使用引导卡 */
.onboard-card {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 16px 18px;
  margin-bottom: 22px;
  border-color: var(--c-brand-border);
  background: var(--c-brand-soft);
}
.onboard-card__icon {
  width: 40px;
  height: 40px;
  border-radius: var(--r-xl);
  background: var(--c-bg-elevated);
  color: var(--c-brand);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.onboard-card__text {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 3px;
  font-size: 13px;
  color: var(--c-text-secondary);
  line-height: 1.5;
}
.onboard-card__text strong {
  color: var(--c-text-primary);
  font-size: 14px;
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

/* 可选区块小徽标 */
.section-tag {
  font-size: 11px;
  font-weight: 500;
  color: var(--c-text-tertiary);
  background: var(--c-bg-hover);
  border: 1px solid var(--c-border);
  border-radius: var(--r-full);
  padding: 1px 8px;
  margin-left: 4px;
  vertical-align: 2px;
}

/* 高级选项折叠区 */
.settings-advanced {
  margin-top: 10px;
  border: 1px dashed var(--c-border);
  border-radius: var(--r-md);
  padding: 0 12px;
}
.settings-advanced summary {
  cursor: pointer;
  user-select: none;
  font-size: 12px;
  color: var(--c-text-tertiary);
  padding: 10px 2px;
}
.settings-advanced summary:hover {
  color: var(--c-text-secondary);
}
.settings-advanced__body .settings-row:last-child {
  border-bottom: none;
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
  flex-wrap: wrap;
}
.settings-row:last-child {
  border-bottom: none;
}
.settings-row--action {
  border-bottom: none;
  padding-top: 8px;
  flex-wrap: nowrap;
}
.settings-row--action .settings-row__text {
  min-width: 0;
}

.settings-row__text {
  flex: 1;
  min-width: 200px; /* 防止窗口较窄时被右侧固定宽控制列挤成竖排（一行一个字） */
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
/* 模型行：下拉/输入框占满剩余宽度，测试按钮固定右侧同一行 */
.settings-row__control--wide .select {
  flex: 1;
  min-width: 0;
}
.settings-row__control--wide .input {
  flex: 1;
  min-width: 0;
}
.settings-row__control--narrow {
  flex: 0 0 120px;
}
.settings-row__control--action {
  flex: 0 0 auto;
}

/* 自动保存开关 */
.switch {
  position: relative;
  display: inline-block;
  width: 40px;
  height: 22px;
  cursor: pointer;
}
.switch input {
  position: absolute;
  opacity: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  cursor: pointer;
}
.switch__slider {
  position: absolute;
  inset: 0;
  border-radius: var(--r-full);
  background: var(--c-bg-hover);
  border: 1px solid var(--c-border-strong);
  transition: background 0.15s, border-color 0.15s;
}
.switch__slider::before {
  content: '';
  position: absolute;
  left: 2px;
  top: 2px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--c-text-secondary);
  transition: transform 0.15s, background 0.15s;
}
.switch input:checked + .switch__slider {
  background: var(--c-brand);
  border-color: var(--c-brand);
}
.switch input:checked + .switch__slider::before {
  transform: translateX(18px);
  background: #fff;
}

.settings-row__error {
  display: block;
  padding: 4px 0 10px;
  font-size: 12.5px;
  color: var(--c-danger);
}

/* Python 路径控制区：检测结果 / 按钮 横排，避免挤出 */
.python-control {
  flex-direction: column;
  align-items: stretch;
  gap: 6px;
}
.python-control__main {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.python-control__main .python-detected-chip {
  flex: 1;
  margin-bottom: 0;
  width: auto;
}
.python-control__pick {
  flex-shrink: 0;
}
.python-control__foot {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.python-control__foot .select {
  flex: 1;
  width: auto;
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
  align-items: stretch;
  gap: 2px;
  min-width: 0;
  flex: 1;
}
.python-detected-chip__version {
  display: flex;
  align-items: baseline;
  gap: 6px;
  font-weight: 700;
  color: var(--c-success);
  font-size: 13px;
  white-space: nowrap;
  flex-wrap: nowrap;
  max-width: 100%;
  width: 66.666%;
  min-width: 0;
  overflow: hidden;
}
.python-detected-chip__version-name {
  flex-shrink: 0;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.python-detected-chip__version-num {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.python-detected-chip__version--warn {
  color: var(--c-warning);
}
.python-detected-chip__dot--warn {
  background: var(--c-warning);
  box-shadow: 0 0 0 3px var(--c-warning-soft);
}
.python-detected-chip__path {
  max-width: 66.666%;
  min-width: 0;
  color: var(--c-text-tertiary);
  font-family: var(--font-mono, monospace);
  font-size: 11.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 模型 chip：与版本号一致显示，但模型名较长，用更宽的固定长度保证三块一致 */
.model-chip .python-detected-chip__version {
  display: block;
  width: 24em;
  text-overflow: ellipsis;
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