import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { QASession, QAMessage } from '@/types'
import {
  listQASessions,
  getQAMessages,
  askQuestionStream,
  deleteQASession,
  translateApiError,
} from '@/api/client'

export const useQAStore = defineStore('qa', () => {
  const sessions = ref<QASession[]>([])
  const currentSessionId = ref<number | null>(null)
  const messages = ref<QAMessage[]>([])
  const sending = ref(false)
  const abortCtrl = ref<AbortController | null>(null)
  const sessionsError = ref('') // 会话列表加载失败（修复：不静默伪装成"暂无会话"）
  const error = ref('') // 会话消息加载失败（修复：不静默伪装成空对话）

  // 上次活跃会话 ID 持久化（localStorage），重启后自动恢复（需求：再次打开加载上一次对话）
  const LAST_SESSION_KEY = 'memora.lastSessionId'

  async function fetchSessions() {
    try {
      sessions.value = (await listQASessions()) ?? []
      sessionsError.value = ''
    } catch (e: any) {
      sessionsError.value = e?.message || '加载会话列表失败'
    }
  }

  async function selectSession(id: number) {
    generation.value++ // 切换会话：使旧请求的流式响应无效
    sendSeq.value++
    currentSessionId.value = id
    try {
      localStorage.setItem(LAST_SESSION_KEY, String(id))
    } catch {
      // localStorage 不可用时忽略（隐私模式等）
    }
    try {
      messages.value = (await getQAMessages(id)) ?? []
      error.value = ''
    } catch (e: any) {
      messages.value = []
      error.value = e?.message || '加载对话消息失败'
    }
  }

  // 恢复去重与共享：App.vue（根组件挂载）与 QAPage（普通进入）都会调用 restoreLastSession，
  // 用模块级共享 Promise 保证整次页面生命周期只恢复一次，后续调用 await 同一结果，
  // 避免并发重复拉取或读到来恢复的状态（review 发现：双调竞态）。
  // 文件问答跳转需跳过自动恢复（见 skipRestore），否则 App.vue 冷启动恢复会覆盖跳转目标。
  let restorePromise: Promise<void> | null = null

  async function doRestore() {
    await fetchSessions()
    if (sessions.value.length === 0) return
    let target: number | null = null
    try {
      const saved = localStorage.getItem(LAST_SESSION_KEY)
      if (saved) {
        const id = Number(saved)
        if (sessions.value.some((s) => s.id === id)) target = id
      }
    } catch {
      // 忽略读取失败
    }
    if (target === null) target = sessions.value[0].id // 无记录时取最近会话
    await selectSession(target)
  }

  // 重启后自动恢复上次会话：存在持久化 ID 且仍在会话列表则恢复，否则取最新会话。
  // 所有调用方共享同一个恢复 Promise。
  function restoreLastSession(): Promise<void> {
    if (!restorePromise) restorePromise = doRestore()
    return restorePromise
  }

  // 跳过自动恢复：文件问答跳转（含冷启动直接命中 /qa?mode=file）时调用，
  // 阻止 App.vue 随后执行的恢复把上次会话覆盖到文件问答目标（review 发现）。
  // 设计取舍：此场景下侧栏聊天同样不恢复上次会话（保持空态），属预期行为。
  function skipRestore() {
    if (!restorePromise) restorePromise = Promise.resolve()
  }

  const sendSeq = ref(0)
  const generation = ref(0) // 递增的请求令牌，切换/新建会话时递增，使过期流式响应无效

  // 流式节流状态（store 级，供 cancel() 清理，防跨会话污染）
  // 思考过程块前缀与后端 contract.ThinkChunkPrefix 一致（\x00MTHINK\x00），
  // 推理模型（SenseNova reasoning 等）思维链以该前缀标识，单独渲染、不计入回答
  const THINK_PREFIX = '\u0000MTHINK\u0000'
  // A1/B4：阶段事件前缀，后端 __STAGE__:<stage> 透出的检索/生成阶段标记，不写入消息内容
  const STAGE_PREFIX = '__STAGE__:'
  let pending = ''
  let thinkPending = ''
  let flushTimer: ReturnType<typeof setTimeout> | null = null
  let activeAsstMsg: QAMessage | null = null

  // A1/B4：流式阶段的 UI 状态（retrieving→"正在检索文档…"，generating→"正在生成回答…"）
  const thinking = ref<string>('')

  const flush = (seq: number) => {
    if (flushTimer) {
      clearTimeout(flushTimer)
      flushTimer = null
    }
    if (!pending && !thinkPending) return
    if (seq !== sendSeq.value) {
      pending = '' // 过期轮次的增量直接丢弃，防止写入新消息
      thinkPending = ''
      return
    }
    // 用消息对象引用而非数组下标：流式期间切换会话/清空 messages 后，
    // 增量只写回原消息，不会污染新会话的同类消息
    const target = activeAsstMsg
    if (target && target.role === 'assistant') {
      if (pending) target.content += pending
      if (thinkPending) target.thinking = (target.thinking || '') + thinkPending
    }
    pending = ''
    thinkPending = ''
  }

  async function send(params: { question: string; mode: string; fileId?: number; sessionId?: number }) {
    sending.value = true
    error.value = '' // 新一次提问清空历史加载错误，避免旧错误残留
    // 立即插入用户消息，显示本人气泡
    const userMsg: QAMessage = {
      id: -Date.now(),
      sessionId: currentSessionId.value || 0,
      role: 'user',
      content: params.question,
      createdAt: Date.now(),
    }
    messages.value.push(userMsg)

    // 插入空的助手占位消息
    const assistantMsg: QAMessage = {
      id: -Date.now() - 1,
      sessionId: currentSessionId.value || 0,
      role: 'assistant',
      content: '',
      createdAt: Date.now(),
    }
    messages.value.push(assistantMsg)
    activeAsstMsg = assistantMsg

    // 中止旧请求并创建新控制器
    abortCtrl.value?.abort()
    const ctrl = new AbortController()
    abortCtrl.value = ctrl
    const mySeq = ++sendSeq.value
    const currentGen = ++generation.value

    try {
      await new Promise<void>((resolve) => {
        askQuestionStream(
          params,
          (chunk) => {
            if (generation.value !== currentGen || mySeq !== sendSeq.value) return
            // A1/B4：阶段事件（__STAGE__:<stage>）不写入消息内容，仅更新 thinking 状态
            if (chunk.startsWith(STAGE_PREFIX)) {
              const stage = chunk.slice(STAGE_PREFIX.length)
              if (stage === 'retrieving') {
                thinking.value = '正在检索文档…'
              } else if (stage === 'generating') {
                thinking.value = '正在生成回答…'
              }
              return
            }
            if (chunk.startsWith(THINK_PREFIX)) {
              thinkPending += chunk.slice(THINK_PREFIX.length)
            } else {
              pending += chunk
            }
            // 流式节流：高频 chunk 先累积再刷新，避免每 chunk 触发整条消息
            // 的 Markdown 全量重渲染。但"累积"会让用户看到"字一次性出来"的错觉，
            // 故采用双策略：①小增量走 16ms 批量刷新（接近刷新率，肉眼仍为渐进）；
            // ②一旦累积达 8 字符立即刷新，保证首字与节奏及时可见。
            if (!flushTimer) {
              flushTimer = setTimeout(() => flush(mySeq), 16)
            } else if (pending.length >= 8 || thinkPending.length >= 8) {
              flush(mySeq)
            }
          },
          (result) => {
            if (generation.value !== currentGen || mySeq !== sendSeq.value) return
            flush(mySeq) // 立即刷新未写入的增量
            thinking.value = '' // 流完成：清空阶段状态
            // 流式为空、后端非流式重试兜底时：answer 携带完整回答，
            // 此时无任何 chunk 输出，直接把占位消息补全（正常流式路径 answer 为空，无需处理）
            if (result.answer && activeAsstMsg && !activeAsstMsg.content) {
              activeAsstMsg.content = result.answer
            }
            if (result.sessionId && !currentSessionId.value) {
              currentSessionId.value = result.sessionId
            }
            // 从后端拉取完整消息（含 sources）
            selectSession(result.sessionId || currentSessionId.value || 0)
            fetchSessions()
            resolve()
          },
          (err) => {
            if (generation.value !== currentGen || mySeq !== sendSeq.value) return
            flush(mySeq)
            const cur = activeAsstMsg
            if (!cur) {
              resolve()
              return
            }
            if (err === '已取消') {
              cur.content = (cur.content || '') + '\n（已中止）'
            } else if (cur.content) {
              // 已有部分回答：追加中断提示，保留已生成内容
              cur.content += `\n\n（生成中断：${translateApiError(err)}）`
            } else {
              cur.content = `（${translateApiError(err)}）`
            }
            resolve()
          },
          ctrl.signal,
        )
      })
    } finally {
      flush(mySeq)
      if (abortCtrl.value === ctrl) abortCtrl.value = null
      sending.value = false
    }
  }

  function cancel() {
    if (flushTimer) {
      clearTimeout(flushTimer)
      flushTimer = null
      pending = ''
      thinkPending = ''
    }
    if (abortCtrl.value) {
      abortCtrl.value.abort()
      abortCtrl.value = null
    }
    sending.value = false
  }

  // 新建空会话：递增请求令牌使旧请求的流式响应、增量刷新全部失效，
  // 避免"发送中新建会话，旧回答完成后覆盖新会话"（P1-07）
  function newSession() {
    generation.value++
    sendSeq.value++
    if (flushTimer) {
      clearTimeout(flushTimer)
      flushTimer = null
    }
    pending = ''
    thinkPending = ''
    activeAsstMsg = null
    currentSessionId.value = null
    messages.value = []
  }

  async function removeSession(id: number) {
    try {
      await deleteQASession(id)
    } catch {
      // 删除失败：保留本地状态，让 UI 展示错误（修复 L-6）
      throw new Error('删除会话失败，请重试')
    }
    if (currentSessionId.value === id) {
      currentSessionId.value = null
      messages.value = []
    }
    await fetchSessions()
  }

  return {
    sessions,
    currentSessionId,
    messages,
    sending,
    thinking,
    sessionsError,
    error,
    fetchSessions,
    selectSession,
    restoreLastSession,
    skipRestore,
    send,
    cancel,
    newSession,
    removeSession,
  }
})