<script setup lang="ts">
import { ref, watch, nextTick, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import type { QAMessage } from '@/types'
import Icon from '@/components/Icon.vue'

// ────────────────────────────────────────────────────────────────
// Props 契约（后续同事依赖此接口，勿改签名）
// ────────────────────────────────────────────────────────────────
const props = withDefaults(
  defineProps<{
    messages: QAMessage[]
    sending?: boolean
    placeholder?: string
  }>(),
  { placeholder: '输入问题，Enter 发送，Shift+Enter 换行' },
)

// ────────────────────────────────────────────────────────────────
// Emits 契约
// ────────────────────────────────────────────────────────────────
const emit = defineEmits<{
  (e: 'send', text: string): void
}>()

const router = useRouter()

// ────────────────────────────────────────────────────────────────
// Markdown 渲染 + DOMPurify 严格净化（替换自制 DOMParser sanitizer）
// ────────────────────────────────────────────────────────────────
const ALLOWED_TAGS = [
  'p', 'br', 'b', 'i', 'strong', 'em', 'a', 'ul', 'ol', 'li', 'code', 'pre', 'blockquote',
  'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'hr', 'table', 'thead', 'tbody', 'tr', 'th', 'td',
  'span', 'div', 'del', 'ins', 'sub', 'sup', 'img', 'figure', 'figcaption', 'mark',
]
const ALLOWED_ATTR = ['href', 'target', 'rel', 'class', 'data-path', 'src', 'alt', 'title']
// 允许的 URI 协议：http(s)、mailto、# 锚点、内联图片（data:image/*）。其余（javascript:、外链 data: 等）一律拒绝。
const ALLOWED_URI_REGEXP = /^(?:(?:https?|mailto):|#|data:image\/(?:png|jpe?g|gif|webp);)/i
// 额外禁止的标签（即便 ALLOWED_TAGS 已排除，仍显式禁止以作纵深防御）
const FORBID_TAGS = ['style', 'form', 'input', 'iframe', 'object', 'embed', 'base', 'link', 'meta', 'script']

// [文件=路径, 段落=N] 引用标记转成可点击链接（后端已把回答里的文件路径规整为标记）
const FILE_REF_RE = /\[文件=([^\]]+?)(?:,\s*段落=(\d+))?\]/g
const FILE_REF_PLACEHOLDER = '\u0000REF\u0000'

function renderMarkdown(text: string): string {
  if (!text) return ''
  const refs: { path: string; seq: string }[] = []
  // 先把引用标记换成占位符（避免 marked 破坏链接 HTML），再在净化前还原为链接
  const withPlaceholders = text.replace(FILE_REF_RE, (_full, path, seq) => {
    refs.push({ path: (path || '').trim(), seq: seq || '' })
    return FILE_REF_PLACEHOLDER
  })
  // 启用 GFM 换行（单个 \n 渲染为 <br>），保证标题/列表/粗体等 Markdown 按预期呈现
  let html = marked.parse(withPlaceholders, {
    async: false,
    gfm: true,
    breaks: true,
  }) as string
  if (!html) return ''
  // 还原链接：href 固定为 #，真实路径经 URL 编码放 data-path，点击由事件委托处理（防注入）
  html = html.replace(new RegExp(FILE_REF_PLACEHOLDER, 'g'), () => {
    const ref = refs.shift()
    if (!ref) return ''
    const safePath = ref.path
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;')
    return `<a class="msg-file-link" href="#qa-file" data-path="${encodeURIComponent(ref.path)}">📄 ${safePath}${ref.seq ? ` (§${ref.seq})` : ''}</a>`
  })
  // DOMPurify 严格净化：仅保留白名单标签/属性/协议，剥离原始 HTML、内联样式、表单元素、远程媒体
  html = DOMPurify.sanitize(html, {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    ALLOWED_URI_REGEXP,
    FORBID_TAGS,
  })
  return html
}

// ────────────────────────────────────────────────────────────────
// 思考块检测：content 以 %%%THINK%%% 开头，或 think(ing) 字段为真
// ────────────────────────────────────────────────────────────────
const THINK_CONTENT_PREFIX = '%%%THINK%%%'
function getThinking(msg: QAMessage): string | null {
  if (msg.thinking && msg.thinking.trim()) return msg.thinking
  if (msg.content && msg.content.startsWith(THINK_CONTENT_PREFIX)) {
    return msg.content.slice(THINK_CONTENT_PREFIX.length)
  }
  return null
}
// 是否还有可作为正文渲染的内容（思考块单独展示，不解析其内部 Markdown）
function hasRenderableContent(msg: QAMessage): boolean {
  if (!msg.content) return false
  if (msg.content.startsWith(THINK_CONTENT_PREFIX)) return false
  return true
}

// ────────────────────────────────────────────────────────────────
// 时间戳格式化（复用现有 formatChatTime 逻辑）
// ────────────────────────────────────────────────────────────────
function formatChatTime(ms?: number): string {
  if (!ms) return ''
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// ────────────────────────────────────────────────────────────────
// 自动滚动到底部
// ────────────────────────────────────────────────────────────────
const listRef = ref<HTMLElement | null>(null)
function scrollToBottom() {
  nextTick(() => {
    if (listRef.value) {
      listRef.value.scrollTop = listRef.value.scrollHeight
    }
  })
}
watch(
  () => props.messages.length,
  () => scrollToBottom(),
)
watch(
  () => {
    const last = props.messages[props.messages.length - 1]
    return last ? last.content.length : 0
  },
  () => {
    if (props.sending) scrollToBottom()
  },
)

// ────────────────────────────────────────────────────────────────
// 文件引用链接点击委托（读取 data-path 跳转，避免把用户数据拼进内联 JS）
// ────────────────────────────────────────────────────────────────
const rootRef = ref<HTMLElement | null>(null)
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

onMounted(() => {
  rootRef.value?.addEventListener('click', handleFileLinkClick)
  scrollToBottom()
})
onUnmounted(() => {
  rootRef.value?.removeEventListener('click', handleFileLinkClick)
})

// ────────────────────────────────────────────────────────────────
// 输入区
// ────────────────────────────────────────────────────────────────
const inputText = ref('')
function handleSend() {
  const text = inputText.value.trim()
  if (!text || props.sending) return
  emit('send', text)
  inputText.value = ''
}
</script>

<template>
  <div ref="rootRef" class="chat-surface">
    <!-- 消息列表 -->
    <div ref="listRef" class="message-list">
      <div
        v-for="msg in messages"
        :key="msg.id"
        class="message"
        :class="`message--${msg.role === 'user' ? 'user' : 'assistant'}`"
      >
        <div class="message-avatar">
          <Icon :name="msg.role === 'user' ? 'chat' : 'memory'" :size="15" />
        </div>
        <div class="message-body">
          <div class="message-top">
            <span class="message-role">{{ msg.role === 'user' ? '你' : 'Memora' }}</span>
            <span class="message-time">{{ formatChatTime(msg.createdAt) }}</span>
          </div>
          <!-- 思考过程：折叠/淡化样式单独展示，不解析内部 Markdown -->
          <details v-if="getThinking(msg)" class="thinking-box" open>
            <summary>思考过程</summary>
            <div class="thinking-content">{{ getThinking(msg) }}</div>
          </details>
          <!-- 用户消息：纯文本，右对齐 -->
          <div v-if="msg.role === 'user'" class="message-content">{{ msg.content }}</div>
          <!-- AI 回答：Markdown 渲染，左对齐 -->
          <div
            v-else-if="hasRenderableContent(msg)"
            class="message-content"
            v-html="renderMarkdown(msg.content)"
          ></div>
          <!-- 流式输出等待期的打字光标 -->
          <span v-if="msg.role === 'assistant' && !msg.content && sending" class="typing-cursor">▍</span>
        </div>
      </div>
    </div>

    <!-- 输入区 -->
    <div class="qa-input-bar">
      <textarea
        v-model="inputText"
        class="input qa-input"
        :placeholder="sending ? '思考中…' : placeholder"
        :disabled="sending"
        rows="1"
        @keydown.enter.exact.prevent="handleSend"
      />
      <button class="btn btn-primary" :disabled="sending || !inputText.trim()" @click="handleSend">
        {{ sending ? '思考中…' : '发送' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.chat-surface {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  gap: 12px;
}

/* ─────── 消息列表 ─────── */
.message-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 8px 4px;
  display: flex;
  flex-direction: column;
  gap: 20px;
  scroll-behavior: smooth;
}

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

/* ─────── Markdown 元素样式（AI 回答） ─────── */
.message-content h1,
.message-content h2,
.message-content h3,
.message-content h4,
.message-content h5,
.message-content h6,
.message-content p,
.message-content ul,
.message-content ol,
.message-content pre,
.message-content blockquote {
  margin: 0.45em 0;
}

.message-content > h1:first-child,
.message-content > h2:first-child,
.message-content > h3:first-child,
.message-content > h4:first-child,
.message-content > h5:first-child,
.message-content > h6:first-child,
.message-content > p:first-child {
  margin-top: 0;
}

.message-content > :last-child {
  margin-bottom: 0;
}

.message-content h1 {
  font-size: 16px;
  font-weight: 700;
  line-height: 1.4;
  margin-top: 0.7em;
  color: var(--c-text-primary);
}

.message-content h2 {
  font-size: 15px;
  font-weight: 700;
  line-height: 1.45;
  color: var(--c-text-primary);
}

.message-content h3 {
  font-size: 14.5px;
  font-weight: 700;
  line-height: 1.5;
  color: var(--c-brand);
}

.message-content h4,
.message-content h5,
.message-content h6 {
  font-size: 14px;
  font-weight: 600;
  line-height: 1.5;
  color: var(--c-text-primary);
}

.message-content strong {
  font-weight: 700;
  color: var(--c-text-primary);
}

.message-content ul,
.message-content ol {
  padding-left: 20px;
}

.message-content li {
  margin: 0.15em 0;
  line-height: 1.7;
}

.message-content ul ul,
.message-content ol ol,
.message-content ul ol,
.message-content ol ul {
  margin: 0.2em 0;
}

.message-content code {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 0.9em;
  padding: 0.1em 0.4em;
  margin: 0 0.05em;
  background: var(--c-bg-hover, #f1f3f5);
  border-radius: var(--r-xs);
  color: var(--c-text-primary);
}

.message-content pre {
  overflow-x: auto;
  padding: 8px 10px;
  border-radius: var(--r-md);
  background: var(--c-bg-hover, #f1f3f5);
  border: 1px solid var(--c-border);
  font-size: 13px;
  line-height: 1.55;
}

.message-content pre code {
  padding: 0;
  margin: 0;
  background: transparent;
}

.message-content blockquote {
  margin-left: 0;
  padding: 2px 10px;
  border-left: 3px solid var(--c-border);
  color: var(--c-text-secondary);
}

.message-content a:not(.msg-file-link) {
  color: var(--c-brand);
  text-decoration: underline;
  text-decoration-color: transparent;
  transition: text-decoration-color 0.12s;
}

.message-content a:not(.msg-file-link):hover {
  text-decoration-color: var(--c-brand);
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
  cursor: pointer;
  transition: border-color 0.12s;
}

.msg-file-link:hover {
  border-color: var(--c-info);
}

/* 思考过程折叠区 */
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

/* ─────── 输入区 ─────── */
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