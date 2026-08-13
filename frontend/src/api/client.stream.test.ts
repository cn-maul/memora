// askQuestionStream 流式测试（Wails v3 事件模型）。
// 当前实现不依赖 fetch/SSE：前端生成事件 id（qa:chunk:<id> / qa:done:<id> / qa:error:<id>），
// 经 Events.On 订阅，后端在流结束时经 QAService.AskStream 调用与全局事件回推。
// 本测试 mock @wailsio/runtime 的 Events 与绑定调用，模拟后端事件推送，覆盖：
//   - chunk/done/error 回调与事件 id 握手
//   - 后端流结束但未发 done/error → 回调"连接中断，请重试"（不悬挂）
//   - 中止 → 「已取消」且事件订阅被清理
//   - AskStream 传输层异常 → onError
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { askQuestionStream } from '@/api/client'

// 捕获 Events.On 注册的事件处理器（name → handler），便于测试端模拟后端推送。
// 所有 mock 状态经 vi.hoisted 声明，避免 vi.mock 提升导致的 TDZ 报错。
const h = vi.hoisted(() => {
  const eventHandlers = new Map<string, (payload: any) => void>()
  return {
    eventHandlers,
    eventsOn: vi.fn((name: string, cb: (payload: any) => void) => { eventHandlers.set(name, cb) }),
    eventsOff: vi.fn((name: string) => { eventHandlers.delete(name) }),
    mockAskStream: vi.fn(),
  }
})

vi.mock('@wailsio/runtime', () => ({
  Events: { On: h.eventsOn, Off: h.eventsOff },
}))

vi.mock('../../bindings/memora/internal/assembler/qaservice.js', () => ({
  AskStream: (...args: unknown[]) => h.mockAskStream(...args),
}))

beforeEach(() => {
  h.eventHandlers.clear()
  h.eventsOn.mockClear()
  h.eventsOff.mockClear()
  h.mockAskStream.mockReset()
  h.mockAskStream.mockResolvedValue(undefined)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

type DoneCb = (result: { sessionId: number; sources: any[]; answer?: string }) => void

interface StreamResult {
  chunks: string[]
  done: ReturnType<typeof vi.fn<DoneCb>>
  errors: string[]
}

// 按前缀找到注册的处理器（事件 id 由实现内部生成，测试通过前缀定位）
function handlerFor(prefix: string): { name: string; handler: (payload: any) => void } | null {
  for (const [name, handler] of h.eventHandlers) {
    if (name.startsWith(prefix)) return { name, handler }
  }
  return null
}

function emit(prefix: 'qa:chunk:' | 'qa:done:' | 'qa:error:', payload: any) {
  const f = handlerFor(prefix)
  if (!f) throw new Error(`未找到 ${prefix} 事件订阅`)
  f.handler(payload)
}

// 发起一次流式请求，返回回调收集句柄与"模拟后端事件推送"的闭包
function runStream(): { result: StreamResult; emit: (prefix: 'qa:chunk:' | 'qa:done:' | 'qa:error:', payload: any) => void; signal: AbortController } {
  const result: StreamResult = { chunks: [], done: vi.fn(), errors: [] }
  const ctrl = new AbortController()
  askQuestionStream(
    { question: 'q', mode: 'chat' },
    (c) => result.chunks.push(c),
    result.done,
    (e) => result.errors.push(e),
    ctrl.signal,
  )
  return { result, emit, signal: ctrl }
}

describe('askQuestionStream 事件订阅', () => {
  it('订阅 qa:chunk:/qa:done:/qa:error: 三组事件，并把 requestId 随 AskStream 回传', async () => {
    const { result, emit } = runStream()
    // 事件 id 由实现内部生成，三个事件必须共享同一 id（同源握手）
    const chunk = handlerFor('qa:chunk:')
    const done = handlerFor('qa:done:')
    const error = handlerFor('qa:error:')
    expect(chunk).not.toBeNull()
    expect(done).not.toBeNull()
    expect(error).not.toBeNull()
    const id = chunk!.name.replace('qa:chunk:', '')
    expect(done!.name).toBe('qa:done:' + id)
    expect(error!.name).toBe('qa:error:' + id)
    // AskStream 已携带 requestId 调用
    expect(h.mockAskStream).toHaveBeenCalledTimes(1)
    const [req] = h.mockAskStream.mock.calls[0]
    expect(req.requestId).toBe(id)

    emit('qa:done:', { sessionId: 7, sources: [{ fileId: 1 }] })
    expect(result.done).toHaveBeenCalledWith({ sessionId: 7, sources: [{ fileId: 1 }], answer: '' })
    expect(result.errors).toEqual([])
  })

  it('chunk 事件 → onChunk 收到原始文本', async () => {
    const { result, emit } = runStream()
    emit('qa:chunk:', '你好，')
    emit('qa:chunk:', '世界')
    emit('qa:done:', { sessionId: 3, sources: [] })
    expect(result.chunks).toEqual(['你好，', '世界'])
    expect(result.done).toHaveBeenCalledWith({ sessionId: 3, sources: [], answer: '' })
  })

  it('payload 走 {data: ...} 包装时也能解出内容', async () => {
    const { result, emit } = runStream()
    emit('qa:chunk:', { data: '包装块' })
    emit('qa:done:', { data: { sessionId: 5, sources: [], answer: '重试答案' } })
    expect(result.chunks).toEqual(['包装块'])
    expect(result.done).toHaveBeenCalledWith({ sessionId: 5, sources: [], answer: '重试答案' })
  })

  it('error 事件 → onError 收到错误文案', async () => {
    const { result, emit } = runStream()
    emit('qa:error:', '模型暂时不可用，请稍后重试')
    expect(result.done).not.toHaveBeenCalled()
    expect(result.errors).toEqual(['模型暂时不可用，请稍后重试'])
  })

  it('error 事件 payload 无内容 → 按协议错误提示', async () => {
    const { result, emit } = runStream()
    emit('qa:error:', {})
    expect(result.errors).toEqual(['连接中断，请重试'])
  })

  it('done 事件后订阅被清理（Events.Off 三组）', async () => {
    const { emit } = runStream()
    emit('qa:done:', { sessionId: 1, sources: [] })
    expect(h.eventsOff).toHaveBeenCalledTimes(3)
    expect(h.eventHandlers.size).toBe(0)
  })

  it('收到 done 后不再触发额外错误', async () => {
    const { result, emit } = runStream()
    emit('qa:done:', { sessionId: 1, sources: [] })
    await Promise.resolve()
    expect(result.errors).toEqual([])
  })
})

describe('askQuestionStream 异常路径', () => {
  it('AskStream 返回但未发 done/error（后端异常）→ 回调"连接中断，请重试"而非悬挂', async () => {
    const { result } = runStream()
    await new Promise((r) => setTimeout(r, 0))
    expect(result.done).not.toHaveBeenCalled()
    expect(result.errors).toEqual(['连接中断，请重试'])
    expect(h.eventHandlers.size).toBe(0)
  })

  it('请求中止 → onError 收到「已取消」且订阅清理', async () => {
    const { result, signal } = runStream()
    signal.abort()
    await new Promise((r) => setTimeout(r, 0))
    expect(result.errors).toEqual(['已取消'])
    expect(h.eventHandlers.size).toBe(0)
  })

  it('AskStream 传输层异常 → onError 收到友好提示', async () => {
    h.mockAskStream.mockRejectedValueOnce(new Error('聊天端点未配置'))
    const { result } = runStream()
    await new Promise((r) => setTimeout(r, 0))
    expect(result.errors).toEqual(['AI 助手还未连接，请到「设置 → AI 助手」里连接'])
  })

  it('AskStream 网络异常 → onError 收到网络提示', async () => {
    h.mockAskStream.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    const { result } = runStream()
    await new Promise((r) => setTimeout(r, 0))
    expect(result.errors).toEqual(['无法连接服务，请检查网络或稍后重试'])
  })
})
