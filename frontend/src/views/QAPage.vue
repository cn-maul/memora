<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQAStore } from '@/stores/qa'
import { useFilesStore } from '@/stores/files'
import { useWorkspaceStore } from '@/stores/workspace'
import type { QASession } from '@/types'
import Icon from '@/components/Icon.vue'
import { marked } from 'marked'
import { getFile } from '@/api/client'

const route = useRoute()
const router = useRouter()
const qa = useQAStore()
const files = useFilesStore()
const ws = useWorkspaceStore()

const question = ref('')
const mode = ref('global')
const showSessions = ref(true)
const selectedFileId = ref<number | null>(null)
const qaError = ref('')
const listRef = ref<HTMLDivElement | null>(null)

function scrollToBottom() {
  nextTick(() => {
    if (listRef.value) {
      listRef.value.scrollTop = listRef.value.scrollHeight
    }
  })
}

// 流式输出期间自动跟随滚动：监听最后一条消息的内容长度变化；
// 仅当用户本就在底部附近（距离底部 < 80px）时跟随，避免向上阅读被强拉回
watch(
  () => {
    const last = qa.messages[qa.messages.length - 1]
    return last ? last.content.length : 0
  },
  () => {
    if (!qa.sending) return
    const el = listRef.value
    if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 80) {
      scrollToBottom()
    }
  },
)

onMounted(async () => {
  // 读取工作区初始化/AI 配置状态，用于顶部引导横幅
  ws.fetchInfo()
  // 自动恢复上一次 AI 对话（需求：再次打开加载上次会话，而不是新建）
  const q = route.query
  if (q.mode === 'file' && q.fileId) {
    // 从文档索引/资源管理器跳转到文件问答：优先使用跳转目标。
    // 显式清空会话状态并跳过自动恢复：App.vue 挂载时可能随后恢复上次会话，
    // 若不清空/不跳过，文件问答会被覆盖或写进上次会话（review 发现：冷启动模式混合污染）
    qa.skipRestore()
    qa.currentSessionId = null
    qa.messages = []
    await qa.fetchSessions()
    mode.value = 'file'
    selectedFileId.value = Number(q.fileId)
    await loadIndexedFiles()
  } else {
    // 普通进入问答页：恢复上次活跃会话（无记录时取最新会话）。
    // doRestore 内部已 try/catch 不会 reject，此处 catch 仅为防御（review nit）
    await qa.restoreLastSession().catch(() => {})
    if (qa.currentSessionId) {
      const s = qa.sessions.find((x) => x.id === qa.currentSessionId)
      if (s) {
        mode.value = s.mode
        selectedFileId.value = s.fileId ?? null
        if (mode.value === 'file') await loadIndexedFiles()
      }
    }
  }
  scrollToBottom()
  // 消息内文件引用链接的事件委托
  document.addEventListener('click', handleFileLinkClick)
})

onUnmounted(() => {
  document.removeEventListener('click', handleFileLinkClick)
})

async function loadIndexedFiles() {
  // 避免污染全局 files store 的 pageSize（修复 L-5）：用局部参数请求，不写 store 的 pageSize
  await files.fetch({ status: 'indexed', pageSize: 200 })
  // 从文档索引页跳转的目标文件可能不在前 200 条里，单独拉取补进下拉选项（修复 M-3）
  if (selectedFileId.value && !files.items.some((f) => f.id === selectedFileId.value)) {
    try {
      const target = await getFile(selectedFileId.value)
      if (target && !files.items.some((f) => f.id === target.id)) {
        files.items = [target, ...files.items]
      }
    } catch {
      // 目标文件不存在则忽略
    }
  }
}

function onModeChange() {
  selectedFileId.value = null
  qaError.value = ''
  if (mode.value === 'file') {
    loadIndexedFiles()
  }
}

function onFileSelect(e: Event) {
  const v = (e.target as HTMLSelectElement).value
  selectedFileId.value = v ? Number(v) : null
  qaError.value = ''
}

async function handleRemoveSession(id: number) {
  try {
    await qa.removeSession(id)
  } catch (e: any) {
    qaError.value = e?.message || '删除会话失败'
  }
}

async function handleSend() {
  if (!question.value.trim()) return
  // 文件问答必须选择文件（修复 B-05）
  if (mode.value === 'file' && !selectedFileId.value) {
    qaError.value = '请先选择要问答的文件'
    return
  }
  qaError.value = ''
  // 会话模式一致性：若当前会话与发送模式不符（如恢复的 global 会话切到文件问答），
  // 必须新建会话，避免把消息写进模式不符的旧会话（review 发现：模式混合污染）
  let sessionId: number | undefined = qa.currentSessionId ?? undefined
  if (sessionId !== undefined) {
    const cur = qa.sessions.find((s) => s.id === sessionId)
    if (!cur || cur.mode !== mode.value) sessionId = undefined
  }
  await qa.send({
    question: question.value,
    mode: mode.value,
    fileId: mode.value === 'file' ? selectedFileId.value ?? undefined : undefined,
    sessionId,
  })
  question.value = ''
  scrollToBottom()
}

function selectSession(s: QASession) {
  qa.selectSession(s.id)
  mode.value = s.mode
  selectedFileId.value = s.fileId ?? null
  if (mode.value === 'file') {
    loadIndexedFiles()
  }
  scrollToBottom()
}

function newSession() {
  qa.currentSessionId = null
  qa.messages = []
  qaError.value = ''
  mode.value = 'global'
  selectedFileId.value = null
}

function formatTime(ms?: number) {
  if (!ms) return ''
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function parseSources(raw?: string) {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed)
      ? parsed
      : Array.isArray(parsed.sources)
        ? parsed.sources
        : []
  } catch {
    return []
  }
}

// 将 Markdown 转换为安全的 HTML
// 移除预转义——它会破坏 # / - / ** / 代码块 等 Markdown 语法的解析；
// marked 默认只解析文本级 Markdown，不会执行 <script> 等 raw HTML（safe 默认关闭但无副作用），
// 再通过 DOMParser 剥离潜在危险标签实现 XSS 防护
// [文件=路径, 段落=N] 引用标记转成可点击链接（后端已把回答里的文件路径规整为标记）
const FILE_REF_RE = /\[文件=([^\]]+?)(?:,\s*段落=(\d+))?\]/g
const FILE_REF_PLACEHOLDER = '\u0000REF\u0000'

function renderMarkdown(text: string): string {
  if (!text) return ''
  const refs: { path: string; seq: string }[] = []
  // 先把引用标记换成占位符（避免 marked 破坏链接 HTML），再在 XSS 剥离前还原为链接
  const withPlaceholders = text.replace(FILE_REF_RE, (_full, path, seq) => {
    refs.push({ path: (path || '').trim(), seq: seq || '' })
    return FILE_REF_PLACEHOLDER
  })
  let html = marked.parse(withPlaceholders, { async: false }) as string
  if (!html) return ''
  // 先还原链接再做 XSS 剥离：链接本身安全（href 仅 #，真实路径在 data-path），
  // 且占位符若先过 DOMParser 会被替换为 U+FFFD 导致匹配失败
  html = html.replace(new RegExp(FILE_REF_PLACEHOLDER, 'g'), () => {
    const ref = refs.shift()
    if (!ref) return ''
    const safePath = ref.path
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;')
    // 链接文本 HTML 转义，路径经 URL 编码放 data-path，点击由事件委托处理（防注入）
    return `<a class="msg-file-link" href="#qa-file" data-path="${encodeURIComponent(ref.path)}">📄 ${safePath}${ref.seq ? ` (§${ref.seq})` : ''}</a>`
  })
  html = stripDangerousHtml(html)
  return html
}

// 消息内文件链接的点击委托（读取 data-path 跳转，避免把用户数据拼进内联 JS）
function handleFileLinkClick(e: MouseEvent) {
  const target = (e.target as HTMLElement | null)?.closest?.('a.msg-file-link') as HTMLAnchorElement | null
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
  router.push({ path: '/files', query: { highlight: relPath } })
}

function stripDangerousHtml(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html')
  for (const tag of ['script', 'iframe', 'object', 'embed', 'base', 'link', 'meta', 'style']) {
    for (const el of doc.body.querySelectorAll(tag)) {
      el.remove()
    }
  }
  // 移除危险属性：on* 事件、javascript:/data: URL（含 tab/换行混淆）、xlink:href、src 外链
  for (const el of doc.body.querySelectorAll('*')) {
    const attrs = Array.from(el.attributes)
    for (const a of attrs) {
      const name = a.name.toLowerCase()
      // 事件属性全部移除
      if (/^on\w+/.test(name)) {
        el.removeAttribute(a.name)
        continue
      }
      // URL 属性（href / xlink:href / src / srcset）：剥离控制字符后校验协议
      if (/^(href|xlink:href|src|srcset)$/.test(name) || name.endsWith(':href')) {
        const cleaned = a.value.replace(/[\u0000-\u001f\u007f]/g, '').trim()
        if (/(javascript|data|vbscript):/i.test(cleaned)) {
          el.removeAttribute(a.name)
        }
      }
      // 通用 URL 协议校验（action/formaction 等）
      if (/(action|formaction)$/.test(name) && /(javascript|data):/i.test(a.value.replace(/[\u0000-\u001f\u007f]/g, ''))) {
        el.removeAttribute(a.name)
      }
    }
  }
  return doc.body.innerHTML
}
</script>

<template>
  <div class="qa-page">
    <!-- 左侧：会话列表 -->
    <aside class="qa-sessions" :class="{ 'qa-sessions--hidden': !showSessions }">
      <div class="qa-sessions__head">
        <button class="btn btn-primary btn-sm" style="flex:1" @click="newSession">
          <Icon name="plus" :size="14" />
          新会话
        </button>
      </div>
      <div class="qa-sessions__list">
        <div v-if="qa.sessions.length === 0" class="empty-state qa-sessions__empty">暂无会话</div>
        <div
          v-for="s in qa.sessions"
          :key="s.id"
          class="session-item"
          :class="{ 'session-item--active': s.id === qa.currentSessionId }"
          @click="selectSession(s)"
        >
          <div class="session-item__main">
            <span class="session-item__title">
              {{ s.mode === 'global' ? '全局问答' : '文件问答' }}
              <span v-if="s.mode === 'global' && showSessions" class="session-item__badge">全局</span>
            </span>
            <span class="session-item__meta">{{ s.messageCount }} 条消息</span>
          </div>
          <button
            class="btn session-item__delete"
            @click.stop="handleRemoveSession(s.id)"
            title="删除会话"
          >
            <Icon name="trash" :size="14" />
          </button>
        </div>
      </div>
    </aside>

    <!-- 右侧：对话面板 -->
    <section class="qa-chat">
      <!-- 未选文件夹 / AI 未配置引导 -->
      <div v-if="!ws.initialized" class="qa-guide banner-warn">
        <Icon name="folder" :size="14" />
        <span>还没有选择要管理的文件夹</span>
        <button class="btn btn-primary btn-sm" @click="router.push('/settings')">去设置</button>
      </div>
      <div v-else-if="!ws.info?.llmConfigured" class="qa-guide banner-warn">
        <Icon name="chat" :size="14" />
        <span>问答需要先连接 AI 助手（对话模型），连接后即可向文档提问</span>
        <button class="btn btn-primary btn-sm" @click="router.push('/settings')">去设置</button>
      </div>

      <header class="qa-chat__header">
        <button
          class="btn btn-ghost btn-sm qa-chat__toggle"
          @click="showSessions = !showSessions"
          :title="showSessions ? '隐藏会话列表' : '显示会话列表'"
        >
          <Icon name="chat" :size="15" />
          <span>{{ qa.sessions.length }}</span>
        </button>
        <div class="mode-toggle segmented">
          <button
            v-if="!qa.currentSessionId"
            class="btn"
            :class="{ 'btn--active': mode === 'global' }"
            @click="mode = 'global'; onModeChange()"
          >全局问答</button>
          <button
            v-if="!qa.currentSessionId"
            class="btn"
            :class="{ 'btn--active': mode === 'file' }"
            @click="mode = 'file'; onModeChange()"
          >文件问答</button>
          <span v-else class="qa-current-label">
            {{ qa.messages.length }} 条消息
          </span>
        </div>
      </header>

      <!-- 文件选择（文件问答模式） -->
      <div v-if="mode === 'file' && !qa.currentSessionId" class="file-picker">
        <Icon name="file" :size="14" class="file-picker__icon" />
        <select class="input" :value="selectedFileId ?? ''" @change="onFileSelect">
          <option value="">-- 请选择文件 --</option>
          <option v-for="f in files.items" :key="f.id" :value="f.id">{{ f.relPath }}</option>
        </select>
        <span v-if="files.loading" class="file-picker__hint">加载中…</span>
        <span v-else-if="files.items.length === 0" class="file-picker__hint">暂无可问答文件（请先索引文档）</span>
      </div>

      <div v-if="qaError" class="alert alert--error qa-error">{{ qaError }}</div>

      <!-- 消息列表 -->
      <div ref="listRef" class="message-list">
        <div v-if="qa.messages.length === 0" class="qa-welcome">
          <div class="qa-welcome__logo">
            <Icon name="memory" :size="30" />
          </div>
          <h3>{{ qa.currentSessionId ? '开始提问吧' : '选择会话或创建新会话开始提问' }}</h3>
          <p>基于文档向内容语义问答，引用可溯源。</p>
        </div>
        <div v-else>
          <div
            v-for="msg in qa.messages"
            :key="msg.id"
            class="message"
            :class="`message--${msg.role}`"
          >
            <div class="message-avatar">
              <Icon :name="msg.role === 'user' ? 'chat' : 'memory'" :size="15" />
            </div>
            <div class="message-body">
              <div class="message-top">
                <span class="message-role">{{ msg.role === 'user' ? '你' : 'Memora' }}</span>
                <span class="message-time">{{ formatTime(msg.createdAt) }}</span>
              </div>
              <!-- 思考过程：推理模型思维链（流式期间实时展示，不落库，刷新后消失） -->
              <details v-if="msg.thinking" class="thinking-box" open>
                <summary>思考过程</summary>
                <div class="thinking-content">{{ msg.thinking }}</div>
              </details>
              <div class="message-content" v-if="msg.role === 'assistant'" v-html="renderMarkdown(msg.content)"></div>
              <div class="message-content" v-else>{{ msg.content }}</div>
              <span
                v-if="msg.role === 'assistant' && !msg.content && qa.sending"
                class="typing-cursor"
              >▍</span>
              <div v-if="parseSources(msg.sources).length" class="message-sources">
                <span
                  v-for="(src, i) in parseSources(msg.sources)"
                  :key="i"
                  class="msg-source"
                  :title="typeof src === 'string' ? src : (src.relPath || JSON.stringify(src))"
                >
                  <Icon name="file" :size="12" />
                  {{ typeof src === 'string' ? src : (src.relPath || src) }}
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 输入 -->
      <div class="qa-input-bar">
        <textarea
          v-model="question"
          class="input qa-input"
          :placeholder="qa.sending ? '等待回答…' : '输入问题，Enter 发送，Shift+Enter 换行…'"
          :disabled="qa.sending"
          rows="1"
          @keydown.enter.exact.prevent="handleSend"
        />
        <button class="btn btn-primary" :disabled="qa.sending || !question.trim()" @click="handleSend">
          {{ qa.sending ? '发送中…' : '发送' }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
/* 顶部引导横幅（未选文件夹 / AI 未配置） */
.qa-guide {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border-radius: var(--r-lg);
  border: 1px solid var(--c-warning);
  background: var(--c-warning-soft);
  font-size: 13px;
  color: var(--c-text-secondary);
  flex-shrink: 0;
}
.qa-guide span {
  flex: 1;
}

.qa-page {
  display: flex;
  height: 100%;
  padding: 16px;
  gap: 14px;
  overflow: hidden;
}

/* ─────────── 会话列表 ─────────── */

.qa-sessions {
  width: 240px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  background: var(--c-bg-secondary);
  border: 1px solid var(--c-border);
  border-radius: var(--r-lg);
  overflow: hidden;
  transition: width 0.2s ease;
}

.qa-sessions--hidden {
  width: 0;
  border: none;
  flex-shrink: 0;
}

.qa-sessions__head {
  padding: 10px;
  border-bottom: 1px solid var(--c-border);
}

.qa-sessions__list {
  flex: 1;
  overflow-y: auto;
  padding: 8px;
}

.qa-sessions__empty {
  padding: 24px 10px;
}

.session-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  padding: 9px 10px;
  border-radius: var(--r-md);
  cursor: pointer;
  font-size: 13px;
  transition: background 0.14s ease;
  margin-bottom: 2px;
}

.session-item:hover {
  background: var(--c-bg-hover);
}

.session-item--active {
  background: var(--c-brand-soft);
}

.session-item__main {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.session-item__title {
  font-weight: 600;
  color: var(--c-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-item--active .session-item__title {
  color: var(--c-brand);
}

.session-item__meta {
  font-size: 11px;
  color: var(--c-text-tertiary);
}

.session-item__badge {
  font-size: 10px;
  padding: 0 6px;
  border-radius: var(--r-full);
  background: var(--c-info-soft);
  color: var(--c-info);
  font-weight: 600;
  margin-left: 4px;
}

.session-item__delete {
  padding: 4px;
  color: var(--c-text-tertiary);
  opacity: 0;
  transition: opacity 0.14s ease;
}

.session-item:hover .session-item__delete {
  opacity: 1;
}

.session-item__delete:hover {
  color: var(--c-danger);
  background: var(--c-danger-soft);
}

/* ─────────── 对话面板 ─────────── */

.qa-chat {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.qa-chat__header {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}

.qa-chat__toggle {
  color: var(--c-text-secondary);
}

.qa-current-label {
  font-size: 13px;
  color: var(--c-text-secondary);
}

.file-picker {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  background: var(--c-bg-secondary);
  border: 1px solid var(--c-border);
  border-radius: var(--r-lg);
  flex-shrink: 0;
}

.file-picker__icon {
  color: var(--c-icon-secondary);
  flex-shrink: 0;
}

.file-picker .input {
  flex: 1;
}

.file-picker__hint {
  font-size: 12px;
  color: var(--c-text-tertiary);
  flex-shrink: 0;
  white-space: nowrap;
}

.qa-error {
  margin-bottom: 0;
  flex-shrink: 0;
}

.message-list {
  flex: 1;
  overflow-y: auto;
  padding: 8px 4px;
  display: flex;
  flex-direction: column;
  gap: 20px;
  scroll-behavior: smooth;
}

/* 欢迎空态 */
.qa-welcome {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  color: var(--c-text-tertiary);
  padding: 40px 20px;
}

.qa-welcome__logo {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 64px;
  height: 64px;
  border-radius: var(--r-2xl);
  background: var(--c-brand-soft);
  color: var(--c-brand);
  margin-bottom: 16px;
}

.qa-welcome h3 {
  font-size: 16px;
  font-weight: 600;
  color: var(--c-text-primary);
  margin: 0 0 6px;
}

.qa-welcome p {
  font-size: 13px;
  margin: 0;
}

/* 消息气泡 */
.message {
  display: flex;
  gap: 10px;
  max-width: 88%;
  animation: msg-in 0.18s ease-out;
}

@keyframes msg-in {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

.message--user {
  align-self: flex-end;
  flex-direction: row-reverse;
}

.message--assistant {
  align-self: flex-start;
}

.message-avatar {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: var(--r-full);
  flex-shrink: 0;
}

.message--user .message-avatar {
  background: var(--c-info-soft);
  color: var(--c-info);
}

.message--assistant .message-avatar {
  background: var(--c-brand-soft);
  color: var(--c-brand);
}

.message-body {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.message-top {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.message-role {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--c-text-secondary);
}

.message-time {
  font-size: 11px;
  color: var(--c-text-tertiary);
}

.message-content {
  padding: 10px 14px;
  border-radius: var(--r-lg);
  font-size: 14px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-wrap: break-word;
}

.message--user .message-content {
  background: var(--c-brand);
  color: var(--c-on-brand);
  border-top-right-radius: var(--r-xs);
}

.message--assistant .message-content {
  background: var(--c-bg-secondary);
  border: 1px solid var(--c-border);
  border-top-left-radius: var(--r-xs);
  color: var(--c-text-primary);
}

/* 消息内的文件引用链接 */
.msg-file-link {
  display: inline-block;
  margin: 2px 0;
  padding: 1px 8px;
  border-radius: var(--r-full);
  background: var(--c-info-soft);
  color: var(--c-info);
  font-size: 12.5px;
  text-decoration: none;
  border: 1px solid transparent;
  transition: border-color 0.12s;
}
.msg-file-link:hover {
  border-color: var(--c-info);
}

/* 思考过程折叠区（推理模型思维链，流式期间实时展示） */
.thinking-box {
  background: var(--c-bg-secondary);
  border: 1px dashed var(--c-border);
  border-radius: var(--r-md);
  padding: 6px 10px;
  font-size: 12.5px;
  color: var(--c-text-tertiary);
  line-height: 1.6;
}

.thinking-box summary {
  cursor: pointer;
  user-select: none;
  font-weight: 600;
  color: var(--c-text-secondary);
}

.thinking-content {
  margin-top: 4px;
  white-space: pre-wrap;
  word-break: break-word;
}

/* 流式输出等待期的打字光标 */
.typing-cursor {
  display: inline-block;
  margin: 6px 0 0 4px;
  color: var(--c-brand);
  font-size: 14px;
  animation: typing-blink 0.9s steps(1) infinite;
}

@keyframes typing-blink {
  50% {
    opacity: 0;
  }
}

.message-sources {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.msg-source {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11.5px;
  padding: 2px 9px;
  border-radius: var(--r-full);
  background: var(--c-info-soft);
  color: var(--c-info);
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 输入区 */
.qa-input-bar {
  display: flex;
  gap: 8px;
  align-items: flex-end;
  flex-shrink: 0;
  padding-top: 4px;
}

.qa-input {
  flex: 1;
  min-height: 42px;
  max-height: 160px;
  resize: none;
  line-height: 1.5;
}
</style>