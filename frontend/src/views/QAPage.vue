<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQAStore } from '@/stores/qa'
import { useFilesStore } from '@/stores/files'
import { useWorkspaceStore } from '@/stores/workspace'
import type { QASession } from '@/types'
import Icon from '@/components/Icon.vue'
import ChatSurface from '@/components/ChatSurface.vue'
import { getFile } from '@/api/client'

const route = useRoute()
const router = useRouter()
const qa = useQAStore()
const files = useFilesStore()
const ws = useWorkspaceStore()

const mode = ref('global')
const showSessions = ref(true)
const selectedFileId = ref<number | null>(null)
const qaError = ref('')

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

async function handleSendMessage(text: string) {
  if (!text.trim() || qa.sending) return
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
    question: text,
    mode: mode.value,
    fileId: mode.value === 'file' ? selectedFileId.value ?? undefined : undefined,
    sessionId,
  })
}

// 示例问题：新手点一下就能问（S4：降低提问门槛）
const exampleQuestions = [
  '帮我把工作文件夹里的文档做个总结',
  '我在哪些文档里写过关于预算的内容？',
  '最近一个月有哪些文件改动比较大？',
]
function askExample(q: string) {
  if (mode.value === 'file') return
  handleSendMessage(q)
}

function selectSession(s: QASession) {
  qa.selectSession(s.id)
  mode.value = s.mode
  selectedFileId.value = s.fileId ?? null
  if (mode.value === 'file') {
    loadIndexedFiles()
  }
}

function newSession() {
  qa.newSession() // 内部递增 generation/sendSeq，使旧请求的流式响应失效，并清空消息、重置 currentSessionId
  qaError.value = ''
  mode.value = 'global'
  selectedFileId.value = null
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
        <div v-if="qa.sessionsError" class="empty-state qa-sessions__empty qa-sessions__empty--error">{{ qa.sessionsError }}</div>
        <div v-else-if="qa.sessions.length === 0" class="empty-state qa-sessions__empty">暂无会话</div>
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
        <span v-if="files.error" class="file-picker__hint file-picker__hint--error">{{ files.error }}</span>
        <span v-else-if="files.loading" class="file-picker__hint">加载中…</span>
        <span v-else-if="files.items.length === 0" class="file-picker__hint">暂无可问答文件（请先索引文档）</span>
      </div>

      <div v-if="qaError" class="alert alert--error qa-error">{{ qaError }}</div>
      <div v-if="qa.error" class="alert alert--error qa-error">{{ qa.error }}</div>

      <!-- 空态欢迎 -->
      <div v-if="qa.messages.length === 0 && !qa.error" class="qa-welcome">
        <div class="qa-welcome__logo">
          <Icon name="memory" :size="30" />
        </div>
        <h3>{{ qa.currentSessionId ? '开始提问吧' : '选择会话或创建新会话开始提问' }}</h3>
        <p>基于文档向内容语义问答，引用可溯源。</p>
        <div v-if="mode !== 'file'" class="qa-examples">
          <button
            v-for="q in exampleQuestions"
            :key="q"
            class="qa-example"
            @click="askExample(q)"
          >
            {{ q }}
          </button>
        </div>
      </div>

      <!-- ChatSurface 消息渲染与输入 -->
      <ChatSurface
        v-if="qa.messages.length > 0"
        :messages="qa.messages"
        :sending="qa.sending"
        :thinking="qa.thinking"
        placeholder="输入问题，Enter 发送，Shift+Enter 换行"
        @send="handleSendMessage"
        @cancel="qa.cancel"
      />
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

.qa-sessions__empty--error {
  color: var(--c-danger);
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

.file-picker__hint--error {
  color: var(--c-danger);
  white-space: normal;
}

.qa-error {
  margin-bottom: 0;
  flex-shrink: 0;
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

/* 示例问题（S4） */
.qa-examples {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 24px;
  width: 100%;
  max-width: 340px;
}
.qa-example {
  padding: 10px 14px;
  border: 1px solid var(--c-border);
  border-radius: var(--r-md);
  background: var(--c-bg-panel);
  color: var(--c-text-secondary);
  font-size: 13px;
  text-align: left;
  cursor: pointer;
  transition: border-color 0.1s, color 0.1s, background 0.1s;
}
.qa-example:hover {
  border-color: var(--c-accent);
  color: var(--c-text-primary);
  background: var(--c-bg-hover);
}
</style>