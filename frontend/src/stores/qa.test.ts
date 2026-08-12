// qa store 竞态测试（P1-07）：
//   - generation token 防覆盖：旧请求的流式块/结果不覆盖新请求
//   - newSession / selectSession 使旧请求失效
//   - sending 状态正确；错误路径不污染消息
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises } from '@vue/test-utils'
import { useQAStore } from '@/stores/qa'
import type { QAMessage } from '@/types'

// 仅 mock 网络函数，保留 translateApiError 真实实现（校验错误文案映射）
const mocks = vi.hoisted(() => ({
  askQuestionStream: vi.fn(),
  listQASessions: vi.fn(),
  getQAMessages: vi.fn(),
  deleteQASession: vi.fn(),
}))

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return { ...actual, ...mocks }
})

interface StreamCall {
  params: { question: string; mode: string; fileId?: number; sessionId?: number }
  onChunk: (chunk: string) => void
  onDone: (result: { sessionId: number; sources: any[] }) => void
  onError: (err: string) => void
  signal: AbortSignal
}

let streamCalls: StreamCall[] = []

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  streamCalls = []
  mocks.listQASessions.mockResolvedValue([])
  mocks.getQAMessages.mockResolvedValue([])
  mocks.deleteQASession.mockResolvedValue(undefined)
  mocks.askQuestionStream.mockImplementation(
    (params: StreamCall['params'], onChunk: StreamCall['onChunk'], onDone: StreamCall['onDone'], onError: StreamCall['onError'], signal: AbortSignal) => {
      streamCalls.push({ params, onChunk, onDone, onError, signal })
    },
  )
})

describe('qa store 发送竞态', () => {
  it('发送中再次发送：旧请求的流式块/结果不覆盖新请求', async () => {
    const store = useQAStore()
    store.send({ question: 'A', mode: 'chat' })
    const pB = store.send({ question: 'B', mode: 'chat' })
    expect(streamCalls.length).toBe(2)
    expect(store.messages.map((m) => m.content)).toEqual(['A', '', 'B', ''])

    // A 的流式块晚到：generation 已更新 → 丢弃，不写入任何消息
    streamCalls[0].onChunk('A 的旧内容')
    expect(store.messages[3].content).toBe('')

    // B 的流式块正常累积并写入（两段触发立即 flush）
    streamCalls[1].onChunk('B 的第一段')
    streamCalls[1].onChunk('01234567')
    expect(store.messages[3].content).toBe('B 的第一段01234567')

    // A 的 done 晚到：被忽略，不把会话切回 1
    streamCalls[0].onDone({ sessionId: 1, sources: [] })
    expect(store.currentSessionId).not.toBe(1)

    // B 的 done：正常收尾，会话落到 2
    streamCalls[1].onDone({ sessionId: 2, sources: [] })
    await Promise.resolve()
    await flushPromises()
    await pB
    expect(store.currentSessionId).toBe(2)
    expect(store.sending).toBe(false)
  })

  it('newSession 使旧请求失效：旧流式响应不写入新会话', async () => {
    const store = useQAStore()
    store.send({ question: 'A', mode: 'chat' })
    expect(store.messages.length).toBe(2)

    store.newSession()
    expect(store.messages.length).toBe(0)
    expect(store.currentSessionId).toBe(null)

    // 旧请求的流式块/结果晚到：一律丢弃
    streamCalls[0].onChunk('01234567')
    expect(store.messages.length).toBe(0)
    streamCalls[0].onDone({ sessionId: 1, sources: [] })
    expect(store.currentSessionId).toBe(null)
    expect(store.messages.length).toBe(0)
    // pA 永不 resolve（回调被代次守卫拦截），测试不应等待它
  })

  it('selectSession 切换会话后，旧请求的流式响应不写入', async () => {
    const loaded: QAMessage[] = [
      { id: 1, sessionId: 2, role: 'assistant', content: '已加载的消息', createdAt: 1 },
    ]
    mocks.getQAMessages.mockResolvedValue(loaded)
    const store = useQAStore()
    store.send({ question: 'A', mode: 'chat' }) // 发送中切换
    await store.selectSession(2)

    expect(store.currentSessionId).toBe(2)
    expect(store.messages).toEqual(loaded)

    // 旧流式块晚到：被丢弃，不污染新会话消息
    streamCalls[0].onChunk('01234567')
    expect(store.messages).toEqual(loaded)

    // 旧 done 晚到：不覆盖 currentSessionId
    streamCalls[0].onDone({ sessionId: 1, sources: [] })
    expect(store.currentSessionId).toBe(2)
  })
})

describe('qa store sending / 错误路径', () => {
  it('发送中 sending=true，done 完成后复位 false', async () => {
    const store = useQAStore()
    const p = store.send({ question: 'hi', mode: 'chat' })
    expect(store.sending).toBe(true)
    expect(streamCalls[0].signal.aborted).toBe(false)

    streamCalls[0].onDone({ sessionId: 1, sources: [] })
    await p
    await flushPromises()
    expect(store.sending).toBe(false)
  })

  it('流式错误：助手消息标注友好错误、用户消息不被污染、sending 复位', async () => {
    const store = useQAStore()
    const p = store.send({ question: 'hi', mode: 'chat' })

    streamCalls[0].onError('timeout')
    await p
    await flushPromises()

    expect(store.sending).toBe(false)
    expect(store.messages.length).toBe(2)
    expect(store.messages[0]).toMatchObject({ role: 'user', content: 'hi' })
    expect(store.messages[1]).toMatchObject({ role: 'assistant' })
    expect(store.messages[1].content).toBe('（无法连接服务，请检查网络或稍后重试）')
  })

  it('已取消：助手消息追加「已中止」', async () => {
    const store = useQAStore()
    const p = store.send({ question: 'hi', mode: 'chat' })
    streamCalls[0].onError('已取消')
    await p
    expect(store.messages[1].content).toBe('\n（已中止）')
    expect(store.messages[1].content).toContain('已中止')
  })

  it('部分回答后出错：保留已生成内容并追加中断提示', async () => {
    const store = useQAStore()
    const p = store.send({ question: 'hi', mode: 'chat' })
    streamCalls[0].onChunk('第一段')
    streamCalls[0].onChunk('01234567') // 触发 flush，内容已写入
    streamCalls[0].onError('模型暂时不可用')
    await p
    expect(store.messages[1].content).toContain('第一段01234567')
    expect(store.messages[1].content).toContain('生成中断')
  })
})

describe('qa store 会话加载错误', () => {
  it('会话列表加载失败 → sessionsError 置位，不伪装成空会话', async () => {
    mocks.listQASessions.mockRejectedValueOnce(new Error('列表挂了'))
    const store = useQAStore()
    await store.fetchSessions()
    expect(store.sessionsError).toBe('列表挂了')
    expect(store.sessions).toEqual([])
  })

  it('切换会话消息加载失败 → error 置位，messages 清空不残留旧会话', async () => {
    mocks.getQAMessages.mockRejectedValueOnce(new Error('加载消息失败'))
    const store = useQAStore()
    await store.selectSession(2)
    expect(store.error).toBe('加载消息失败')
    expect(store.messages).toEqual([])
  })
})

describe('qa store 会话管理', () => {
  it('删除当前会话 → 清空本地消息并刷新列表', async () => {
    mocks.getQAMessages.mockResolvedValue([
      { id: 1, sessionId: 2, role: 'assistant', content: 'x', createdAt: 1 },
    ])
    const store = useQAStore()
    await store.selectSession(2)
    expect(store.messages.length).toBe(1)

    await store.removeSession(2)
    expect(mocks.deleteQASession).toHaveBeenCalledWith(2)
    expect(store.currentSessionId).toBe(null)
    expect(store.messages).toEqual([])
    expect(mocks.listQASessions).toHaveBeenCalled()
  })

  it('删除失败 → 抛错，不静默假装成功', async () => {
    mocks.deleteQASession.mockRejectedValueOnce(new Error('boom'))
    const store = useQAStore()
    await expect(store.removeSession(1)).rejects.toThrow('删除会话失败，请重试')
  })
})
