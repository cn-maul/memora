<script setup lang="ts">
import { onMounted, onUnmounted, ref, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useTagsStore } from '@/stores/tags'
import { useQAStore } from '@/stores/qa'
import { useFilesStore } from '@/stores/files'
import {
  createSSEConnection,
  getQueueStatus,
  autoCommit,
  getCommitStatus,
  suggestCommitMessage,
  manualCommit,
  type QueueStatus,
  type CommitFileStatus,
} from '@/api/client'
import Icon, { type IconName } from '@/components/Icon.vue'

const router = useRouter()
const route = useRoute()
const ws = useWorkspaceStore()
const tags = useTagsStore()
const qa = useQAStore()
const filesStore = useFilesStore()

// 设置页自带完整侧边栏，隐藏全局壳的两侧面板以腾出空间
const hideSidePanels = computed(() => route.path === '/settings')

const queueStatus = ref<QueueStatus | null>(null)

async function refreshQueue() {
  try {
    queueStatus.value = await getQueueStatus()
  } catch {
    // 队列接口不可用时静默忽略（如后端未就绪）
  }
}

let queueTimer: ReturnType<typeof setInterval> | null = null

const navItems: { path: string; label: string; icon: IconName }[] = [
  { path: '/files', label: '全部文件', icon: 'folder' },
  { path: '/workspace', label: '资源管理器', icon: 'folder-open' },
  { path: '/index', label: '文档索引', icon: 'search' },
  { path: '/timeline', label: '提交记录', icon: 'clock' },
  { path: '/qa', label: '问答', icon: 'chat' },
  { path: '/stats', label: '统计', icon: 'chart' },
  { path: '/settings', label: '设置', icon: 'settings' },
]

const gitBranchName = computed(() => {
  // 优先展示后端返回的真实分支名；未初始化/未就绪时回退为占位（修复分支名硬编码问题）
  return ws.info?.head?.branch || '主分支'
})
const autoCommitting = ref(false)
const autoCommitResult = ref('')

function headHashShort(): string {
  const h = ws.info?.head?.hash
  return h ? h.slice(0, 8) : ''
}

async function handleAutoCommit() {
  autoCommitting.value = true
  autoCommitResult.value = ''
  try {
    const res = await autoCommit()
    if (res.hash) {
      autoCommitResult.value = res.ai ? `AI 提交 ${res.hash.slice(0, 8)}` : `已提交 ${res.hash.slice(0, 8)}`
    } else {
      autoCommitResult.value = '无变更'
    }
  } catch (e: any) {
    autoCommitResult.value = e.message || '提交失败'
  } finally {
    autoCommitting.value = false
  }
}

function gitDirtySum(): number {
  if (!ws.info?.dirtyCounts) return 0
  return (
    (ws.info.dirtyCounts.modified || 0) +
    (ws.info.dirtyCounts.untracked || 0) +
    (ws.info.dirtyCounts.deleted || 0)
  )
}

function hasUncommitted(): boolean {
  return gitDirtySum() > 0
}
function codeLabel(code: string): string {
  const map: Record<string, string> = { M: '修改', A: '新增', D: '删除', '??': '未跟踪' }
  return map[code] || code
}
function codeClass(code: string): string {
  const map: Record<string, string> = { M: 'cm-m', A: 'cm-a', D: 'cm-d', '??': 'cm-u' }
  return map[code] || 'cm-m'
}
function codeIconName(code: string): 'plus' | 'x' | 'check' | 'refresh' {
  if (code === 'A' || code === '??') return 'plus'
  if (code === 'D') return 'x'
  return 'refresh'
}

// ──────── 手动提交对话框 ────────
const commitDialogOpen = ref(false)
const commitMessage = ref('')
const commitFiles = ref<CommitFileStatus[]>([])
const commitLoading = ref(false)
const commitSubmitting = ref(false)
const commitSuggesting = ref(false)
const commitError = ref('')
const commitSuccess = ref('')

async function openCommitDialog() {
  commitDialogOpen.value = true
  commitMessage.value = ''
  commitError.value = ''
  commitSuccess.value = ''
  await loadCommitStatus()
}

function closeCommitDialog() {
  commitDialogOpen.value = false
}

async function loadCommitStatus() {
  commitLoading.value = true
  commitError.value = ''
  try {
    const res = await getCommitStatus()
    commitFiles.value = res.files || []
  } catch (e: any) {
    commitFiles.value = []
    commitError.value = e.message || '获取变更状态失败'
  } finally {
    commitLoading.value = false
  }
}

async function handleSuggestMessage() {
  commitSuggesting.value = true
  commitError.value = ''
  commitSuccess.value = ''
  try {
    const suggestion = await suggestCommitMessage()
    commitMessage.value = suggestion
    commitSuccess.value = '已生成 AI 建议，可自行修改后提交'
  } catch (e: any) {
    commitError.value = e.message || 'AI 生成备注失败'
  } finally {
    commitSuggesting.value = false
  }
}

async function handleManualCommit() {
  const msg = commitMessage.value.trim()
  if (!msg) {
    commitError.value = '请填写提交备注，或点「AI 生成」自动总结'
    return
  }
  commitSubmitting.value = true
  commitError.value = ''
  commitSuccess.value = ''
  try {
    const hash = await manualCommit(msg)
    commitSuccess.value = hash ? `提交成功 ${hash.slice(0, 8)}` : '没有变更需要提交'
    commitDialogOpen.value = false
    await ws.fetchInfo()
    autoCommitResult.value = hash ? `已提交 ${hash.slice(0, 8)}` : ''
  } catch (e: any) {
    commitError.value = e.message || '提交失败'
  } finally {
    commitSubmitting.value = false
  }
}

// ──────── 侧栏折叠 ────────
const sidebarCollapsed = ref(false)
const sidebarEffectiveWidth = computed(() => (sidebarCollapsed.value ? 48 : sidebarWidth.value))
function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
}
const sidebarRef = ref<HTMLElement | null>(null)
const chatRef = ref<HTMLElement | null>(null)
const sidebarWidth = ref(260)
const chatWidth = ref(320)
const dragging = ref<'sidebar' | 'chat' | null>(null)

function startDrag(target: 'sidebar' | 'chat', e: MouseEvent) {
  const el = (target === 'sidebar' ? sidebarRef.value : chatRef.value) as HTMLElement | null
  if (!el) return
  const sideEl = el
  dragging.value = target
  document.body.style.cursor = 'col-resize'
  e.preventDefault()

  function onMove(ev: MouseEvent) {
    if (target === 'sidebar') {
      const w =
        ev.clientX - sideEl.getBoundingClientRect().left + sidebarWidth.value - sideEl.offsetWidth
      if (w >= 180 && w <= 420) sidebarWidth.value = w
    } else {
      const w = window.innerWidth - ev.clientX
      if (w >= 280 && w <= 560) chatWidth.value = w
    }
  }

  function onUp() {
    dragging.value = null
    document.body.style.cursor = ''
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
  }

  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
}

// ── 对话输入 ────────
const chatInput = ref('')
const chatInputRef = ref<HTMLTextAreaElement | null>(null)
const chatError = ref('')
function autosize() {
  const ta = chatInputRef.value
  if (!ta) return
  ta.style.height = 'auto'
  ta.style.height = Math.min(ta.scrollHeight, 180) + 'px'
}
async function send() {
  const text = chatInput.value.trim()
  if (!text || qa.sending) return
  chatInput.value = ''
  autosize()
  chatError.value = ''
  // 侧栏始终按全局问答发送：若当前恢复的会话是 file 模式，先新建会话，
  // 避免把全局消息写进文件问答会话（review 发现：复用 sessionId 会混合模式）
  let sessionId: number | undefined = qa.currentSessionId ?? undefined
  if (sessionId !== undefined) {
    const cur = qa.sessions.find((s) => s.id === sessionId)
    if (cur && cur.mode === 'file') sessionId = undefined
  }
  await qa.send({ question: text, mode: 'global', sessionId })
  // 注：错误提示由 qa store 写入助手消息气泡，此处不再需要空消息兜底分支
}
function cancelSend() {
  qa.cancel()
}

// 将答案中的文件引用转换为可点击链接
function renderAnswer(text: string): string {
  if (!text) return ''
  // 先提取 [文件=路径, 段落=N] 引用占位，再转义其余文本，最后插入链接
  const refs: { path: string; seq: string }[] = []
  const placeholder = '\u0000REF\u0000'
  // 路径贪婪匹配到 ]（允许含逗号），段落号为可选项。修复 L-3：路径含逗号被截断
  const withPlaceholders = text.replace(/\[文件=([^\]]+?)(?:,\s*段落=(\d+))?\]/g, (_full, path, seq) => {
    // 若路径本身以 ", 段落=N" 结尾（LLM 同时输出），剥离出来作为 seq
    let p = (path || '').trim()
    let s = seq || ''
    const segMatch = p.match(/,\s*段落=(\d+)$/)
    if (segMatch && !s) {
      s = segMatch[1]
      p = p.slice(0, segMatch.index).trim()
    }
    refs.push({ path: p, seq: s })
    return placeholder
  })
  // 转义非引用部分
  let html = withPlaceholders
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
  // 把占位替换回链接；ref.path 来自 LLM 输出（prompt injection 可控）。
  // 链接文本 HTML 转义，路径经 URL 编码后放 data-* 属性，点击由根元素事件委托处理——
  // 不把用户数据拼进任何内联 JS 字符串（消除 onclick 注入面）。
  html = html.replace(new RegExp(placeholder, 'g'), () => {
    const ref = refs.shift()
    if (!ref) return ''
    const encoded = encodeURIComponent(ref.path)
    const safePath = ref.path
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;')
    return `<a class="chat-file-link" href="/files?highlight=${encoded}" data-path="${encoded}">📄 ${safePath}${ref.seq ? ` (§${ref.seq})` : ''}</a>`
  })
  // 处理换行
  html = html.replace(/\n/g, '<br>')
  return html
}
function formatChatTime(ms?: number) {
  if (!ms) return ''
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}
// 聊天文件引用链接的点击委托处理（读取 data-path，避免内联 onclick 注入）
function handleChatFileLinkClick(e: MouseEvent) {
  const target = (e.target as HTMLElement | null)?.closest?.('a.chat-file-link') as HTMLAnchorElement | null
  if (!target) return
  e.preventDefault()
  const raw = target.getAttribute('data-path')
  if (!raw) return
  let relPath = ''
  try {
    relPath = decodeURIComponent(raw)
  } catch {
    return
  }
  ;(window as any).__navigateToFile?.(relPath)
}
function newChat() {
  qa.currentSessionId = null
  qa.messages = []
  chatError.value = ''
}

// ──────── 明暗模式 ────────
const theme = ref<'dark' | 'light'>(localStorage.getItem('memora-theme') as 'dark' | 'light' || 'dark')

function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  localStorage.setItem('memora-theme', theme.value)
  applyTheme(theme.value)
}

function applyTheme(t: 'dark' | 'light') {
  document.documentElement.setAttribute('data-theme', t)
}

let cleanupSSE: (() => void) | null = null
// 重建完成提示的自动清除 timer（新重建时清理，避免竞态误清新进度）
let reindexDoneTimer: ReturnType<typeof setTimeout> | null = null
// 单文件索引进度刷新的节流 timer
let incrementalRefreshTimer: ReturnType<typeof setTimeout> | null = null

onMounted(async () => {
  applyTheme(theme.value)
  await ws.fetchInfo()
  // 自动恢复上一次 AI 对话到侧栏聊天（需求：再次打开软件显示上次会话，而不是空的新对话）
  qa.restoreLastSession().catch(() => {})
  // 挂载文件导航函数到 window，供 renderAnswer 生成的链接使用
  ;(window as any).__navigateToFile = (relPath: string) => {
    ;(window as any).__highlightFile = relPath
    // 带 query 跳转：已在 /files 页时 query 变化会触发 AllFilesPage 的 watch，
    // 不在 /files 时挂载后 onMounted 读 query.highlight——两种场景都能高亮。
    // 修复：之前 push('/files') 无 query，已在文件页时组件不重挂载导致高亮失效。
    router.push({ path: '/files', query: { highlight: relPath } })
  }
  // 文件引用链接事件委托：点击 .chat-file-link 时从 data-path 读取路径导航。
  // 用事件委托而非内联 onclick，避免用户数据进入 JS 字符串（防注入）。
  document.addEventListener('click', handleChatFileLinkClick)
  cleanupSSE = createSSEConnection((topic: string, data: any) => {
    if (topic === 'index_progress' && data) {
      // 仅处理 FullReindex 的事件（带 phase 字段：reset/processing/done）。
      // 单文件索引进度事件（{done:true, fileId, relPath}）无 phase，忽略以免产生幽灵进度卡片。
      if (data.phase === 'reset' || data.phase === 'processing' || data.phase === 'done') {
        filesStore.setReindexProgress({
          phase: data.phase,
          done: data.done,
          total: data.total,
          current: data.current,
        })
        // 新重建开始或完成时清理旧的自动清除 timer，避免竞态误清新进度
        if (data.phase === 'reset' || data.phase === 'done') {
          if (reindexDoneTimer) clearTimeout(reindexDoneTimer)
          reindexDoneTimer = null
        }
        if (data.phase === 'reset' || data.phase === 'done') {
          filesStore.fetch()
        }
        // done 后 2 秒自动清除进度卡片
        if (data.phase === 'done') {
          reindexDoneTimer = setTimeout(() => filesStore.setReindexProgress(null), 2000)
        }
      } else if (data.done && data.fileId) {
        // 单文件索引进度（增量/重试完成）：节流刷新列表，让 IndexPage 状态列实时更新（修复 M-4）
        if (incrementalRefreshTimer) clearTimeout(incrementalRefreshTimer)
        incrementalRefreshTimer = setTimeout(() => filesStore.fetch(), 500)
      }
    }
    if (topic === 'index_progress' || topic === 'files_changed' || topic === 'tag_done') {
      tags.fetchTags()
    }
    if (topic === 'files_changed' || topic === 'commit_done' || topic === 'task_queue') {
      ws.fetchInfo()
      refreshQueue()
    }
    if (topic === 'task_queue') {
      refreshQueue()
    }
  })

  tags.fetchTags()
  refreshQueue()
  queueTimer = setInterval(refreshQueue, 5000)
})

onUnmounted(() => {
  cleanupSSE?.()
  if (queueTimer) clearInterval(queueTimer)
  if (reindexDoneTimer) clearTimeout(reindexDoneTimer)
  if (incrementalRefreshTimer) clearTimeout(incrementalRefreshTimer)
  document.removeEventListener('click', handleChatFileLinkClick)
})
</script>

<template>
  <div class="app">
    <!-- 左侧栏 -->
    <aside
      v-show="!hideSidePanels"
      ref="sidebarRef"
      class="sidebar"
      :style="{ width: sidebarEffectiveWidth + 'px' }"
    >
      <div class="sidebar-top">
        <button title="菜单" @click="toggleSidebar">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M3 12h18M3 6h18M3 18h18"/></svg>
        </button>
      </div>

      <nav class="nav" v-show="!sidebarCollapsed">
        <router-link
          v-for="item in navItems"
          :key="item.path"
          :to="item.path"
          class="nav-item"
          active-class="nav-item--active"
        >
          <Icon :name="item.icon" :size="16" />
          <span class="nav-label">{{ item.label }}</span>
        </router-link>
      </nav>

      <div class="git-section" v-show="!sidebarCollapsed">
        <div class="git-label">版本控制</div>

        <div class="git-branch-row">
          <Icon name="git-branch" :size="14" :color="'var(--c-icon-secondary)'" />
          <span class="git-branch-name">{{ gitBranchName }}</span>
        </div>

        <div class="git-head-row" v-if="ws.initialized && ws.info?.head">
          <span class="git-head">
            <span class="git-head__label">HEAD</span>
            <span class="git-head__hash">{{ headHashShort() || '—' }}</span>
            <span class="git-head__sep">·</span>
            <span class="git-head__count">{{ ws.info.head?.countFiles ?? 0 }} 文件</span>
            <span class="git-head__sep">·</span>
            <span class="git-head__count">{{ ws.info.head?.changedFiles ?? 0 }} 改动</span>
          </span>
        </div>

        <div class="git-status-row" v-if="ws.initialized">
          <span class="git-status-item" title="相对上次提交被修改的文件数">
            <span class="git-status-dot" :class="{ 'dot--modified': (ws.info?.dirtyCounts?.modified || 0) > 0 }"></span>
            <span class="git-status-num">{{ ws.info?.dirtyCounts?.modified || 0 }}</span>
            <span class="git-status-text">修改</span>
          </span>
          <span class="git-status-item" title="尚未被 Git 跟踪的新文件数">
            <span class="git-status-dot" :class="{ 'dot--untracked': (ws.info?.dirtyCounts?.untracked || 0) > 0 }"></span>
            <span class="git-status-num">{{ ws.info?.dirtyCounts?.untracked || 0 }}</span>
            <span class="git-status-text">未跟踪</span>
          </span>
          <span class="git-status-item" title="相对上次提交被删除的文件数">
            <span class="git-status-dot" :class="{ 'dot--deleted': (ws.info?.dirtyCounts?.deleted || 0) > 0 }"></span>
            <span class="git-status-num">{{ ws.info?.dirtyCounts?.deleted || 0 }}</span>
            <span class="git-status-text">删除</span>
          </span>
        </div>

        <div class="git-btn-row">
          <button
            class="git-commit-btn git-commit-btn--auto"
            :disabled="autoCommitting || !hasUncommitted()"
            @click="handleAutoCommit"
            title="自动提交"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v4M12 18v4M2 12h4M18 12h4"/></svg>
            {{ autoCommitting ? '…' : 'auto' }}
          </button>
          <button
            class="git-commit-btn git-commit-btn--manual"
            :disabled="!hasUncommitted()"
            @click="openCommitDialog"
            title="手动提交（可写备注 + AI 生成）"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
            <span class="git-commit-btn__label">{{ autoCommitting ? '…' : '写备注' }}</span>
          </button>
        </div>

        <div v-if="autoCommitResult" class="git-result" :class="autoCommitResult.startsWith('已提交') ? 'git-result--success' : ''">
          {{ autoCommitResult }}
        </div>
      </div>

      <div class="sidebar-footer">
        <button @click="router.push('/settings')" title="设置">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3"/><path d="M1 14h6M9 8h6M17 16h6"/></svg>
        </button>
        <button @click="toggleTheme" :title="theme === 'dark' ? '切换浅色模式' : '切换深色模式'" class="theme-toggle">
          <svg v-if="theme === 'dark'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="5"/><path d="M12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4"/></svg>
          <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
        </button>
      </div>
    </aside>

    <div
      v-show="!hideSidePanels"
      class="drag-handle"
      :class="{ dragging: dragging === 'sidebar' }"
      @mousedown="startDrag('sidebar', $event)"
    ></div>

    <!-- 路由出口 -->
    <main class="app-main">
      <!-- 队列异常提示条（修复 L-4：轮询的队列状态现在有 UI 消费） -->
      <div v-if="queueStatus?.paused || queueStatus?.error" class="queue-banner">
        <Icon name="git-branch" :size="13" />
        <span class="queue-banner__text">
          {{ queueStatus.paused ? '任务队列已暂停' : '任务队列异常' }}
          <template v-if="queueStatus.error">：{{ queueStatus.error }}</template>
        </span>
      </div>
      <router-view />
    </main>

    <div
      v-show="!hideSidePanels"
      class="drag-handle chat-handle"
      :class="{ dragging: dragging === 'chat' }"
      @mousedown="startDrag('chat', $event)"
    ></div>

    <!-- 右侧：对话区 -->
    <aside
      v-show="!hideSidePanels"
      ref="chatRef"
      class="chat-panel"
      :style="{ width: chatWidth + 'px' }"
    >
      <div v-if="qa.messages.length === 0" class="chat-greeting">
        <div class="greeting-avatar">
          <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="8.5" cy="10.5" r="1.5" fill="#fff"/><circle cx="15.5" cy="10.5" r="1.5" fill="#fff"/><path d="M12 17.5c2.33 0 4.32-1.45 5.12-3.5H6.88c.8 2.05 2.79 3.5 5.12 3.5z" fill="#fff"/></svg>
        </div>
        <span class="greeting-text">向文档提问，获取基于内容的回答</span>
      </div>

      <div v-else class="chat-messages">
        <div v-for="msg in qa.messages" :key="msg.id" class="chat-msg" :class="`chat-msg--${msg.role}`">
          <div class="chat-msg__role">{{ msg.role === 'user' ? '你' : 'Memora' }}</div>
          <div class="chat-msg__content">
            <template v-if="msg.role === 'assistant' && !msg.content && qa.sending">
              <span class="chat-msg__thinking">正在思考</span>
              <span class="chat-msg__dots"><span></span><span></span><span></span></span>
            </template>
            <template v-else>
              <span v-html="renderAnswer(msg.content)"></span>
            </template>
          </div>
          <div class="chat-msg__time">{{ formatChatTime(msg.createdAt) }}</div>
        </div>
      </div>

      <div v-if="chatError" class="alert alert--error chat-error">{{ chatError }}</div>

      <div class="input-area">
        <div class="input-box">
          <textarea
            ref="chatInputRef"
            v-model="chatInput"
            class="input-textarea"
            :placeholder="qa.sending ? '等待回答…' : '输入问题…'"
            :disabled="qa.sending"
            rows="1"
            @input="autosize"
            @keydown.enter.exact.prevent="send"
          ></textarea>
          <div class="input-toolbar">
            <div class="input-toolbar-right">
              <button
                v-if="qa.sending"
                class="send-btn send-btn--cancel"
                title="中止"
                @click="cancelSend"
              >
                <svg viewBox="0 0 24 24" fill="currentColor" stroke="none"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>
              </button>
              <button
                v-else
                class="send-btn"
                :class="{ 'has-content': chatInput.trim().length > 0 }"
                title="发送"
                :disabled="!chatInput.trim()"
                @click="send"
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M13 6l6 6-6 6"/></svg>
              </button>
            </div>
          </div>
        </div>

        <div class="quick-actions">
          <button class="quick-chip" @click="newChat">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>
            新对话
          </button>
          <button class="quick-chip" @click="router.push('/qa')">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a8 8 0 0 1-8 8H5l-3 2 1-3.5A8 8 0 1 1 21 12z"/></svg>
            详细问答
          </button>
        </div>
      </div>
    </aside>

    <!-- 手动提交对话框 -->
    <div v-if="commitDialogOpen" class="commit-mask" @click.self="closeCommitDialog">
      <div class="commit-dialog card">
        <div class="commit-dialog__head">
          <div class="commit-dialog__title">
            <Icon name="git-branch" :size="16" />
            <span>手动提交</span>
          </div>
          <button class="commit-dialog__close" title="关闭" @click="closeCommitDialog">
            <Icon name="x" :size="16" />
          </button>
        </div>

        <div v-if="commitLoading" class="loading">加载变更状态…</div>
        <template v-else>
          <div v-if="commitFiles.length === 0 && !commitError" class="commit-dialog__empty">
            当前没有变更需要提交
          </div>

          <div v-if="commitFiles.length" class="commit-dialog__files">
            <div v-for="f in commitFiles" :key="f.relPath" class="commit-file">
              <span class="commit-file__badge" :class="codeClass(f.code)">
                <Icon :name="codeIconName(f.code)" :size="11" />
                {{ codeLabel(f.code) }}
              </span>
              <span class="commit-file__path" :title="f.relPath">{{ f.relPath }}</span>
            </div>
          </div>

          <div v-if="commitError" class="alert alert--error commit-dialog__error">{{ commitError }}</div>
          <div v-if="commitSuccess" class="alert alert--success commit-dialog__success">{{ commitSuccess }}</div>

          <label class="commit-dialog__label">提交备注</label>
          <textarea
            v-model="commitMessage"
            class="input textarea commit-dialog__message"
            placeholder="请填写本次提交的说明…"
            rows="3"
          ></textarea>

          <div class="commit-dialog__actions">
            <button
              class="btn btn-ghost btn-sm"
              :disabled="commitSuggesting"
              @click="handleSuggestMessage"
            >
              <Icon name="memory" :size="13" />
              {{ commitSuggesting ? '生成中…' : 'AI 生成' }}
            </button>
            <div class="commit-dialog__actions-right">
              <button class="btn btn-ghost btn-sm" @click="closeCommitDialog">取消</button>
              <button
                class="btn btn-primary btn-sm"
                :disabled="commitSubmitting || !commitMessage.trim()"
                @click="handleManualCommit"
              >
                {{ commitSubmitting ? '提交中…' : '提交' }}
              </button>
            </div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.app {
  width: 100vw;
  height: 100vh;
  display: flex;
  overflow: hidden;
}

/* 拖动分隔条 */
.drag-handle {
  width: 4px;
  flex-shrink: 0;
  cursor: col-resize;
  background: var(--c-bg-elevated);
  position: relative;
  z-index: 5;
  transition: background 0.15s;
}
.drag-handle:hover,
.drag-handle.dragging {
  background: var(--c-brand-border);
}
.drag-handle::after {
  content: "";
  position: absolute;
  inset: 0 -3px;
  cursor: col-resize;
}
.drag-handle.dragging::after {
  position: fixed;
  z-index: 999;
}

/* ── 路由主区 ── */
.queue-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 14px;
  font-size: 12px;
  color: var(--c-warning);
  background: var(--c-warning-soft);
  border-bottom: 1px solid var(--c-border);
  flex-shrink: 0;
}

.queue-banner__text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-main {
  flex: 1;
  min-width: 0;
  height: 100%;
  overflow: hidden;
  background: var(--c-bg-page);
  display: flex;
  flex-direction: column;
}
.app-main > :deep(.settings-layout),
.app-main > :deep(.workspace-page),
.app-main > :deep(.all-files-page),
.app-main > :deep(.index-page),
.app-main > :deep(.timeline-page),
.app-main > :deep(.qa-page),
.app-main > :deep(.stats-page) {
  flex: 1;
  min-height: 0;
}

/* ── 左侧栏 ── */
.sidebar {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  background: var(--c-bg-page);
  border-right: 1px solid var(--c-border);
}
.sidebar-top {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 10px 12px;
  height: 42px;
}
.sidebar-top button {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: var(--r-sm);
  color: var(--c-icon-secondary);
  border: none;
  background: transparent;
  cursor: pointer;
  padding: 0;
  margin: 0;
  line-height: 1;
  transition: background 0.1s;
}
.sidebar-top button:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-secondary);
}
.sidebar-top button svg {
  width: 16px;
  height: 16px;
  display: block;
}

/* 导航 */
.nav {
  padding: 4px 8px;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 7px 10px;
  border-radius: var(--r-md);
  color: var(--c-text-secondary);
  font-size: 13px;
  cursor: pointer;
  text-decoration: none;
  transition: background 0.1s, color 0.1s;
}
.nav-item:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}
.nav-item svg {
  color: var(--c-icon-secondary);
  flex-shrink: 0;
}
.nav-item--active {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}
.nav-item--active svg {
  color: var(--c-text-secondary);
}
.nav-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ── 版本控制 ── */
.git-section {
  margin-top: 8px;
  padding: 0 6px;
}
.git-label {
  font-size: 11px;
  color: var(--c-text-tertiary);
  padding: 4px 10px 6px;
  letter-spacing: 0.5px;
}
.git-branch-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  border-radius: var(--r-sm);
  color: var(--c-text-secondary);
  font-size: 12.5px;
}
.git-branch-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.git-head-row {
  padding: 3px 10px 1px;
}

.git-head {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 7px;
  border-radius: var(--r-xs);
  background: var(--c-bg-elevated);
  font-size: 10.5px;
  color: var(--c-text-tertiary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.git-head__label {
  color: var(--c-text-tertiary);
  flex-shrink: 0;
}

.git-head__hash {
  color: var(--c-brand);
  font-family: Consolas, monospace;
  font-weight: 600;
  letter-spacing: -0.2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.git-head__count {
  color: var(--c-text-secondary);
  font-weight: 500;
  white-space: nowrap;
}

.git-head__sep {
  color: var(--c-text-tertiary);
  flex-shrink: 0;
}

.git-status-row {
  display: flex;
  flex-wrap: nowrap;
  gap: 4px;
  padding: 3px 10px 1px;
}
.git-status-item {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 1px 4px;
  border-radius: var(--r-xs);
  background: var(--c-bg-elevated);
  font-size: 10.5px;
  color: var(--c-text-tertiary);
  flex-shrink: 0;
  white-space: nowrap;
}
.git-status-num {
  font-weight: 600;
  color: var(--c-text-secondary);
  font-variant-numeric: tabular-nums;
}
.git-status-text {
  color: var(--c-icon-secondary);
  font-size: 10px;
  white-space: nowrap;
}
.git-status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: transparent;
  flex-shrink: 0;
}
.git-status-dot.dot--modified {
  background: var(--c-warning);
  box-shadow: 0 0 0 2px var(--c-warning-soft);
}
.git-status-dot.dot--untracked {
  background: var(--c-warning);
  box-shadow: 0 0 0 2px var(--c-warning-soft);
}
.git-status-dot.dot--deleted {
  background: var(--c-danger);
  box-shadow: 0 0 0 2px var(--c-danger-soft);
}
.git-btn-row {
  display: flex;
  gap: 4px;
  padding: 4px 10px 2px;
}

.git-commit-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 3px 6px;
  border-radius: var(--r-xs);
  font-size: 10.5px;
  font-weight: 600;
  cursor: pointer;
  transition: background 0.1s, color 0.1s;
  white-space: nowrap;
  line-height: 1.2;
}

.git-commit-btn--auto {
  background: var(--c-brand-soft);
  color: var(--c-brand);
}
.git-commit-btn--auto:hover:not(:disabled) {
  background: var(--c-brand-border);
}

.git-commit-btn--manual {
  background: var(--c-bg-hover);
  color: var(--c-text-secondary);
}
.git-commit-btn--manual:hover:not(:disabled) {
  background: var(--c-bg-active);
  color: var(--c-text-secondary);
}

.git-commit-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.git-commit-btn svg {
  width: 12px;
  height: 12px;
  flex-shrink: 0;
}

.git-result {
  padding: 3px 10px 2px;
  font-size: 10.5px;
  color: var(--c-icon-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.git-result--success {
  color: var(--c-brand);
}

/* ── 手动提交对话框 ── */
.commit-mask {
  position: fixed;
  inset: 0;
  z-index: 100;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.5);
  backdrop-filter: blur(2px);
}

.commit-dialog {
  width: 440px;
  max-width: calc(100vw - 40px);
  max-height: 70vh;
  overflow-y: auto;
  padding: 18px 20px;
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.5);
}

.commit-dialog__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}

.commit-dialog__title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 15px;
  font-weight: 700;
  color: var(--c-text-primary);
}

.commit-dialog__title :deep(svg) {
  color: var(--c-brand);
}

.commit-dialog__close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: var(--r-md);
  color: var(--c-text-tertiary);
  transition: background 0.1s, color 0.1s;
}
.commit-dialog__close:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}

.commit-dialog__empty {
  padding: 14px;
  border: 1px dashed var(--c-border-strong);
  border-radius: var(--r-md);
  color: var(--c-text-secondary);
  font-size: 13px;
  text-align: center;
  margin-bottom: 12px;
}

.commit-dialog__files {
  max-height: 180px;
  overflow-y: auto;
  border: 1px solid var(--c-border);
  border-radius: var(--r-md);
  background: var(--c-bg-secondary);
  padding: 8px;
  margin-bottom: 12px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.commit-file {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
}

.commit-file__badge {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: var(--r-full);
  font-size: 11px;
  font-weight: 600;
}
.commit-file__badge.cm-m { background: var(--c-warning-soft); color: var(--c-warning); }
.commit-file__badge.cm-a { background: var(--c-success-soft); color: var(--c-success); }
.commit-file__badge.cm-d { background: var(--c-danger-soft); color: var(--c-danger); }
.commit-file__badge.cm-u { background: var(--c-info-soft); color: var(--c-info); }

.commit-file__path {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-secondary);
}

.commit-dialog__error,
.commit-dialog__success {
  margin-bottom: 12px;
}

.commit-dialog__label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: var(--c-text-secondary);
  margin-bottom: 6px;
}

.commit-dialog__message {
  width: 100%;
  resize: vertical;
  min-height: 64px;
  margin-bottom: 14px;
  font-family: inherit;
}

.commit-dialog__actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.commit-dialog__actions-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.sidebar-footer {
  margin-top: auto;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 14px;
  border-top: 1px solid var(--c-border);
}
.sidebar-footer button {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--c-icon-secondary);
  border: none;
  background: transparent;
  cursor: pointer;
  padding: 5px 8px;
  border-radius: var(--r-sm);
  font-size: 12.5px;
  line-height: 1.2;
  margin: 0;
  transition: background 0.1s;
}
.sidebar-footer button:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-secondary);
}
.sidebar-footer button svg {
  width: 15px;
  height: 15px;
  display: block;
}

/* ── 右侧对话区 ── */
.chat-panel {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  background: var(--c-bg-page);
  position: relative;
  overflow: hidden;
}

.chat-greeting {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 28px 20px;
  flex-direction: column;
  text-align: center;
  flex: 1;
  justify-content: center;
}
.greeting-avatar {
  width: 36px;
  height: 36px;
  border-radius: var(--r-md);
  background: var(--c-brand);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.greeting-avatar svg {
  width: 22px;
  height: 22px;
  color: var(--c-on-brand);
}
.greeting-text {
  font-size: 14px;
  font-weight: 500;
  color: var(--c-text-tertiary);
  letter-spacing: 0.3px;
  text-align: center;
  line-height: 1.5;
}

/* 聊天消息列表 */
.chat-messages {
  flex: 1;
  overflow-y: auto;
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  scroll-behavior: smooth;
}

.chat-msg {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-width: 100%;
  min-width: 0;
}

.chat-msg--user {
  align-items: flex-end;
}

.chat-msg--assistant {
  align-items: flex-start;
}

.chat-msg__role {
  font-size: 11px;
  font-weight: 600;
  color: var(--c-text-tertiary);
  padding: 0 2px;
}

.chat-msg__content {
  padding: 8px 12px;
  border-radius: var(--r-md);
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-wrap: break-word;
  overflow-wrap: break-word;
  word-break: break-word;
  max-width: 100%;
  color: var(--c-text-secondary);
}

.chat-msg--user .chat-msg__content {
  background: var(--c-brand);
  color: var(--c-on-brand);
  border-bottom-right-radius: 3px;
}

.chat-msg--assistant .chat-msg__content {
  background: var(--c-bg-panel);
  border: 1px solid var(--c-border);
  border-bottom-left-radius: 3px;
}

.chat-msg__time {
  font-size: 10px;
  color: var(--c-text-tertiary);
  padding: 0 4px;
}

.chat-error {
  margin: 0 14px 8px;
  flex-shrink: 0;
}

/* 输入框 */
.input-area {
  width: 100%;
  max-width: 100%;
  padding: 0 12px 12px;
  flex-shrink: 0;
}
.input-box {
  background: var(--c-bg-panel);
  border: 1px solid var(--c-border);
  border-radius: var(--r-lg);
  padding: 0;
  display: flex;
  flex-direction: column;
  transition: border-color 0.15s, box-shadow 0.15s;
}
.input-box:focus-within {
  border-color: var(--c-brand-border);
  box-shadow: 0 0 0 2px var(--c-brand-soft);
}
.input-textarea {
  width: 100%;
  min-height: 52px;
  max-height: 180px;
  resize: none;
  border: none;
  outline: none;
  background: transparent;
  color: var(--c-text-primary);
  font-size: 14px;
  line-height: 1.6;
  padding: 14px 16px 4px;
  font-family: inherit;
}
.input-textarea::placeholder {
  color: var(--c-text-tertiary);
}
.input-toolbar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px 10px;
}
.input-toolbar-left {
  display: flex;
  align-items: center;
  gap: 4px;
}
.input-toolbar-right {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
}
.toolbar-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: var(--r-md);
  color: var(--c-icon-secondary);
  transition: background 0.1s;
}
.toolbar-btn:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-secondary);
}
.toolbar-btn svg {
  width: 16px;
  height: 16px;
}
.toolbar-label {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--c-brand);
  cursor: pointer;
  padding: 4px 8px;
  border-radius: var(--r-sm);
  transition: background 0.1s;
}
.toolbar-label:hover {
  background: var(--c-brand-soft);
}
.toolbar-label svg {
  width: 13px;
  height: 13px;
}
.toolbar-model {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 11.5px;
  color: var(--c-icon-secondary);
  padding: 4px 8px;
  border-radius: var(--r-sm);
  cursor: pointer;
  transition: background 0.1s;
  max-width: 140px;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.toolbar-model:hover {
  background: var(--c-bg-hover);
}
.toolbar-model svg {
  width: 13px;
  height: 13px;
  flex-shrink: 0;
}
.toolbar-model__text {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.send-btn {
  width: 30px;
  height: 30px;
  border-radius: 50%;
  background: var(--c-brand);
  color: var(--c-on-brand);
  display: flex;
  align-items: center;
  justify-content: center;
  transition: background 0.1s, transform 0.08s;
  opacity: 0.55;
}
.send-btn:hover {
  background: var(--c-brand-hover);
}
.send-btn:active {
  transform: scale(0.92);
}
.send-btn.has-content {
  opacity: 1;
}
.send-btn svg {
  width: 14px;
  height: 14px;
}
.send-btn--cancel {
  background: var(--c-danger);
  opacity: 1;
}
.send-btn--cancel:hover {
  background: var(--c-danger);
}

.chat-msg__thinking {
  color: var(--c-icon-secondary);
  font-size: 13px;
}
.chat-msg__dots {
  display: inline-flex;
  gap: 3px;
  margin-left: 4px;
  vertical-align: middle;
}
.chat-msg__dots span {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--c-text-tertiary);
  animation: chat-dot 1.2s infinite ease-in-out;
}
.chat-msg__dots span:nth-child(2) {
  animation-delay: 0.2s;
}
.chat-msg__dots span:nth-child(3) {
  animation-delay: 0.4s;
}
@keyframes chat-dot {
  0%, 80%, 100% { opacity: 0.3; transform: translateY(0); }
  40% { opacity: 1; transform: translateY(-3px); }
}

.chat-file-link {
  color: var(--c-info);
  text-decoration: none;
  font-size: 12.5px;
  padding: 1px 4px;
  border-radius: var(--r-xs);
  background: var(--c-info-soft);
  white-space: nowrap;
}
.chat-file-link:hover {
  color: var(--c-info);
  background: var(--c-info-soft);
}

/* 快捷操作 */
.quick-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 16px;
  justify-content: center;
  flex-wrap: wrap;
}
.quick-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 30px;
  padding: 0 12px;
  border: 1px solid var(--c-border);
  border-radius: var(--r-xl);
  background: var(--c-bg-hover);
  color: var(--c-text-tertiary);
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s;
  white-space: nowrap;
}
.quick-chip:hover {
  border-color: var(--c-brand-border);
  color: var(--c-text-secondary);
  background: var(--c-brand-soft);
}
.quick-chip svg {
  width: 13px;
  height: 13px;
  opacity: 0.7;
}
</style>