import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { QASession, QAMessage } from '@/types'
import {
  listQASessions,
  getQAMessages,
  askQuestionStream,
  deleteQASession,
} from '@/api/client'

export const useQAStore = defineStore('qa', () => {
  const sessions = ref<QASession[]>([])
  const currentSessionId = ref<number | null>(null)
  const messages = ref<QAMessage[]>([])
  const sending = ref(false)
  const abortCtrl = ref<AbortController | null>(null)

  async function fetchSessions() {
    try {
      sessions.value = await listQASessions()
    } catch {
      sessions.value = []
    }
  }

  async function selectSession(id: number) {
    currentSessionId.value = id
    try {
      messages.value = await getQAMessages(id)
    } catch {
      messages.value = []
    }
  }

  const sendSeq = ref(0)

  // 流式节流状态（store 级，供 cancel() 清理，防跨会话污染）
  let pending = ''
  let flushTimer: ReturnType<typeof setTimeout> | null = null
  let activeAsstMsg: QAMessage | null = null

  const flush = (seq: number) => {
    if (flushTimer) {
      clearTimeout(flushTimer)
      flushTimer = null
    }
    if (!pending) return
    if (seq !== sendSeq.value) {
      pending = '' // 过期轮次的增量直接丢弃，防止写入新消息
      return
    }
    // 用消息对象引用而非数组下标：流式期间切换会话/清空 messages 后，
    // 增量只写回原消息，不会污染新会话的同类消息
    const target = activeAsstMsg
    if (target && target.role === 'assistant') {
      target.content += pending
    }
    pending = ''
  }

  async function send(params: { question: string; mode: string; fileId?: number; sessionId?: number }) {
    sending.value = true
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

    // 中止控制器
    const ctrl = new AbortController()
    abortCtrl.value = ctrl
    const mySeq = ++sendSeq.value

    try {
      await new Promise<void>((resolve) => {
        askQuestionStream(
          params,
          (chunk) => {
            if (mySeq !== sendSeq.value) return
            // 流式节流：chunk 高频到达时先累积，定时（60ms）批量刷新 content，
            // 避免每个 chunk 都触发整条消息的 Markdown 全量重渲染
            pending += chunk
            if (!flushTimer) flushTimer = setTimeout(() => flush(mySeq), 60)
          },
          (result) => {
            if (mySeq !== sendSeq.value) return
            flush(mySeq) // 立即刷新未写入的增量
            if (result.sessionId && !currentSessionId.value) {
              currentSessionId.value = result.sessionId
            }
            // 从后端拉取完整消息（含 sources）
            selectSession(result.sessionId || currentSessionId.value || 0)
            fetchSessions()
            resolve()
          },
          (err) => {
            if (mySeq !== sendSeq.value) return
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
              cur.content += `\n\n（生成中断：${err}）`
            } else {
              cur.content = `（错误：${err}）`
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
    }
    if (abortCtrl.value) {
      abortCtrl.value.abort()
      abortCtrl.value = null
    }
    sending.value = false
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
    fetchSessions,
    selectSession,
    send,
    cancel,
    removeSession,
  }
})