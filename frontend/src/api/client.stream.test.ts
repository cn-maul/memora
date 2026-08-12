// askQuestionStream SSE 分帧 / EOF 测试（P1-06）。
// 通过 mock 全局 fetch（vi.stubGlobal）注入构造的 SSE 流，覆盖：
//   - 分帧解析（含跨 chunk 拼接半帧）
//   - event: done / event: error 回调
//   - 异常 EOF（无 done/error 直接结束 → 回调"连接中断，请重试"，不悬挂）
//   - data: JSON 字符串增量块 → onChunk 解码
//   - 非 2xx 信封错误、网络异常、中止
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { askQuestionStream } from '@/api/client'

const mockFetch = vi.fn()

function encode(chunk: string | Uint8Array): Uint8Array {
  if (typeof chunk === 'string') return new TextEncoder().encode(chunk)
  return chunk
}

// 构造一个 SSE 响应流：start 时一次性推入所有 chunk 并关闭
function makeStream(chunks: (string | Uint8Array)[]): ReadableStream<Uint8Array> {
  return new ReadableStream<Uint8Array>({
    start(controller) {
      for (const c of chunks) controller.enqueue(encode(c))
      controller.close()
    },
  })
}

function respondWithStream(chunks: (string | Uint8Array)[]) {
  mockFetch.mockResolvedValueOnce(new Response(makeStream(chunks), { status: 200 }))
}

beforeEach(() => {
  mockFetch.mockReset()
  vi.stubGlobal('fetch', mockFetch)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

interface StreamResult {
  chunks: string[]
  done: (result: { sessionId: number; sources: any[] }) => void
  errors: string[]
}

// 调用 askQuestionStream，收集回调并返回便于断言的句柄
function runStream(chunks: (string | Uint8Array)[]): Promise<StreamResult> {
  const result: StreamResult = { chunks: [], done: vi.fn<(result: { sessionId: number; sources: any[] }) => void>(), errors: [] }
  respondWithStream(chunks)
  const p = askQuestionStream(
    { question: 'q', mode: 'chat' },
    (c) => result.chunks.push(c),
    result.done,
    (e) => result.errors.push(e),
    new AbortController().signal,
  )
  return p.then(() => result)
}

describe('askQuestionStream SSE 分帧解析', () => {
  it('完整帧解析：event: done → onDone 收到 sessionId/sources', async () => {
    const result = await runStream(['event: done\ndata: {"sessionId":7,"sources":[{"fileId":1}]}\n\n'])
    expect(result.chunks).toEqual([])
    expect(result.done).toHaveBeenCalledTimes(1)
    expect(result.done).toHaveBeenCalledWith({ sessionId: 7, sources: [{ fileId: 1 }] })
    expect(result.errors).toEqual([])
  })

  it('跨 chunk 拼接：半帧拆在两个 chunk 中也能正确解析', async () => {
    const result = await runStream([
      'event: done\ndata: {"se', // 前半帧
      'ssionId":7,"sources":[{"fileId":1}]}\n\n', // 后半帧补全
    ])
    expect(result.done).toHaveBeenCalledWith({ sessionId: 7, sources: [{ fileId: 1 }] })
    expect(result.errors).toEqual([])
  })

  it('跨 chunk 拼接：一个 chunk 含多帧 + 尾部半帧延后补全', async () => {
    const result = await runStream([
      'event: message\ndata: "你好"\n\nevent: mess', // 完整帧 + 下一帧开头
      'age\ndata: "世界"\n\nevent: done\ndata: {"sessionId":3,"sources":[]}\n\n', // 补全
    ])
    expect(result.chunks).toEqual(['你好', '世界'])
    expect(result.done).toHaveBeenCalledWith({ sessionId: 3, sources: [] })
    expect(result.errors).toEqual([])
  })

  it('data: JSON 字符串增量块 → onChunk 拿到解码后的文本', async () => {
    const result = await runStream([
      'event: message\ndata: "你好，"\n\n',
      'event: message\ndata: "世界"\n\n',
      'event: done\ndata: {"sessionId":9,"sources":[]}\n\n',
    ])
    expect(result.chunks).toEqual(['你好，', '世界'])
    expect(result.done).toHaveBeenCalledWith({ sessionId: 9, sources: [] })
    expect(result.errors).toEqual([])
  })

  it('event: error → onError 收到解码后的错误文案', async () => {
    const result = await runStream(['event: error\ndata: "模型暂时不可用，请稍后重试"\n\n'])
    expect(result.done).not.toHaveBeenCalled()
    expect(result.errors).toEqual(['模型暂时不可用，请稍后重试'])
  })

  it('event: error 的 data 不是合法 JSON → 按协议错误提示', async () => {
    const result = await runStream(['event: error\ndata: 服务异常直接明文\n\n'])
    expect(result.errors).toEqual(['连接中断，请重试'])
  })

  it('done 的 data 不是合法 JSON → 协议错误，不静默当空结果', async () => {
    const result = await runStream(['event: done\ndata: not-json\n\n'])
    expect(result.done).not.toHaveBeenCalled()
    expect(result.errors).toEqual(['连接中断，请重试'])
  })
})

describe('askQuestionStream 异常 EOF（P1-06）', () => {
  it('无 done/error 事件直接结束 → 回调"连接中断，请重试"而非悬挂', async () => {
    const result = await runStream(['event: message\ndata: "只发了一半"\n\n'])
    expect(result.chunks).toEqual(['只发了一半'])
    expect(result.done).not.toHaveBeenCalled()
    expect(result.errors).toEqual(['连接中断，请重试'])
  })

  it('EOF 时缓冲区残留半帧 → 同样判定连接中断', async () => {
    const result = await runStream(['event: message\ndata: "未'])
    expect(result.chunks).toEqual([])
    expect(result.done).not.toHaveBeenCalled()
    expect(result.errors).toEqual(['连接中断，请重试'])
  })

  it('空流直接结束 → 连接中断', async () => {
    const result = await runStream([])
    expect(result.errors).toEqual(['连接中断，请重试'])
  })

  it('收到 done 后正常关闭 → 不触发额外错误', async () => {
    const result = await runStream(['event: done\ndata: {"sessionId":1,"sources":[]}\n\n'])
    expect(result.done).toHaveBeenCalledTimes(1)
    expect(result.errors).toEqual([])
  })
})

describe('askQuestionStream 传输层失败', () => {
  it('非 2xx：解析统一信封并把 requestId 附在消息后', async () => {
    const result: StreamResult = { chunks: [], done: vi.fn<(result: { sessionId: number; sources: any[] }) => void>(), errors: [] }
    mockFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ code: 'llm_error', message: 'LLM 服务异常', requestId: 'req-123' }), {
        status: 502,
      }),
    )
    await askQuestionStream(
      { question: 'q', mode: 'chat' },
      (c) => result.chunks.push(c),
      result.done,
      (e) => result.errors.push(e),
      new AbortController().signal,
    )
    expect(result.errors).toEqual(['LLM 服务异常（错误编号 req-123）'])
    expect(result.done).not.toHaveBeenCalled()
  })

  it('非 2xx 且非 JSON 正文 → 通用文案', async () => {
    const result: StreamResult = { chunks: [], done: vi.fn<(result: { sessionId: number; sources: any[] }) => void>(), errors: [] }
    mockFetch.mockResolvedValueOnce(new Response('<html>gateway error</html>', { status: 502 }))
    await askQuestionStream(
      { question: 'q', mode: 'chat' },
      (c) => result.chunks.push(c),
      result.done,
      (e) => result.errors.push(e),
      new AbortController().signal,
    )
    expect(result.errors).toEqual(['请求失败（502）'])
  })

  it('fetch 网络异常 → onError 收到友好提示', async () => {
    const result: StreamResult = { chunks: [], done: vi.fn<(result: { sessionId: number; sources: any[] }) => void>(), errors: [] }
    mockFetch.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    await askQuestionStream(
      { question: 'q', mode: 'chat' },
      (c) => result.chunks.push(c),
      result.done,
      (e) => result.errors.push(e),
      new AbortController().signal,
    )
    expect(result.errors).toEqual(['无法连接服务，请检查网络或稍后重试'])
  })

  it('请求中止 → onError 收到「已取消」', async () => {
    const result: StreamResult = { chunks: [], done: vi.fn<(result: { sessionId: number; sources: any[] }) => void>(), errors: [] }
    mockFetch.mockRejectedValueOnce(new DOMException('The operation was aborted.', 'AbortError'))
    await askQuestionStream(
      { question: 'q', mode: 'chat' },
      (c) => result.chunks.push(c),
      result.done,
      (e) => result.errors.push(e),
      new AbortController().signal,
    )
    expect(result.errors).toEqual(['已取消'])
  })

  it('响应体不可读 → onError 收到明确提示', async () => {
    const result: StreamResult = { chunks: [], done: vi.fn<(result: { sessionId: number; sources: any[] }) => void>(), errors: [] }
    mockFetch.mockResolvedValueOnce({ ok: true, status: 200, headers: { get: () => null }, body: null })
    await askQuestionStream(
      { question: 'q', mode: 'chat' },
      (c) => result.chunks.push(c),
      result.done,
      (e) => result.errors.push(e),
      new AbortController().signal,
    )
    expect(result.errors).toEqual(['响应体不可读'])
  })
})
