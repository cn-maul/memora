import axios from 'axios'
import {
  AppError,
  ERROR_CODES,
  isAppError,
  unwrapData,
  type ErrorCode,
} from '@/utils/errors'
import type {
  ApiResponse,
  WorkspaceInfo,
  InitRequest,
  PaginatedData,
  FileItem,
  TagInfo,
  TagSuggestion,
  SearchResult,
  QASession,
  QAMessage,
  StatsMetrics,
  ProbeResult,
  BrowseDirResponse,
  BrowseSearchResponse,
  BrowsePickDirResponse,
  CommitItem,
  VersionFile,
  CommitFile,
} from '@/types'

const http = axios.create({
  baseURL: '/api',
  timeout: 30000,
})

// 后端错误 → 通俗提示（小白友好）。顺序敏感：先匹配"未配置"，再匹配鉴权/限流等。
export function translateApiError(msg: string): string {
  if (!msg) return '操作失败，请重试'
  const s = String(msg)
  const low = s.toLowerCase()

  if (/network error|timeout|etimedout|econnreset|econnrefused|failed to fetch|无法连接/.test(low)) {
    return '无法连接服务，请检查网络或稍后重试'
  }
  if (s.includes('聊天端点未配置')) return 'AI 助手还未连接，请到「设置 → AI 助手」里连接'
  if (s.includes('嵌入端点未配置')) return '「按内容搜索」还未连接，请到「设置 → 内容整理模型」里连接'
  if (s.includes('工作区未初始化') || s.includes('仓库未初始化')) {
    return '还没有选择要管理的文件夹，请到「设置」里选择'
  }
  if (/401|unauthorized|invalid api|api ?key/.test(low)) return 'API Key 不正确或已失效，请检查后重试'
  if (/403|forbidden/.test(low)) return '没有权限访问该服务，请检查账号权限或 API Key'
  if (/429|rate ?limit|too many requests/.test(low)) return '请求太频繁被限流了，稍等片刻再试'
  if (/model.{0,20}not found|invalid model/.test(low)) return '模型名称不正确，请检查模型设置'
  return s
}

// ──────────────────────── 统一错误（P1-13） ────────────────────────
// 所有失败都以 AppError 形式抛出（带稳定 code + 用户可读 message + requestId）。
// 原始 body/Blob 内容绝不作为 message 直接回吐（避免 SQL/Go 文本泄漏）。

// 把未知响应体规整为 record（兼容 responseType:'text' 时 axios 返回的 JSON 字符串）。
function toRecord(raw: unknown): Record<string, unknown> | null {
  if (typeof raw === 'string') {
    const t = raw.trim()
    if (t.startsWith('{') || t.startsWith('[')) {
      try {
        const parsed = JSON.parse(t)
        return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
          ? (parsed as Record<string, unknown>)
          : null
      } catch {
        return null
      }
    }
    return null
  }
  if (typeof raw === 'object' && raw !== null && !Array.isArray(raw)) {
    return raw as Record<string, unknown>
  }
  return null
}

// 后端原始 code → 前端稳定 ErrorCode；未知 code 一律 internal（不向用户暴露）。
function normalizeCode(code: string): ErrorCode {
  const values = Object.values(ERROR_CODES) as string[]
  return values.includes(code) ? (code as ErrorCode) : ERROR_CODES.internal
}

// 从响应头 + 响应体两处读取 requestId（体优先，头兜底）。
function readRequestId(res?: { headers?: unknown; data?: unknown }): string | undefined {
  const headers = res?.headers as Record<string, unknown> | undefined
  if (headers) {
    const v = headers['x-request-id'] ?? headers['X-Request-ID']
    if (typeof v === 'string' && v) return v
  }
  const body = toRecord(res?.data)
  if (body && typeof body.requestId === 'string' && body.requestId) return body.requestId
  return undefined
}

// 信封形错误体（HTTP 非 2xx）→ AppError。非信封（网络/超时）走 fallbackMsg。
function appErrorFromBody(raw: unknown, opts: { status?: number; headerReqId?: string; fallbackMsg: string }): AppError {
  const body = toRecord(raw)
  let requestId = opts.headerReqId
  if (body && typeof body.requestId === 'string' && body.requestId) requestId = body.requestId
  if (body && typeof body.code === 'string') {
    const msg = typeof body.message === 'string' && body.message ? body.message : opts.fallbackMsg
    return new AppError(msg, normalizeCode(body.code), {
      status: opts.status,
      requestId,
      detail: body.data,
    })
  }
  return new AppError(opts.fallbackMsg, ERROR_CODES.internal, { status: opts.status, requestId })
}

function toAppError(err: unknown): AppError {
  const ae = err as { response?: { status?: number; headers?: unknown; data?: unknown }; message?: string; code?: string }
  const status = ae.response?.status
  const requestId = readRequestId(ae.response)

  // 客户端主动取消（AbortController）
  if (ae?.code === 'ERR_CANCELED') {
    return new AppError('请求已取消', ERROR_CODES.canceled, { status, requestId })
  }

  // Blob 响应（下载端点）：不读取 blob 文本，抛出通用文案
  if (ae.response?.data instanceof Blob) {
    return new AppError('下载失败', ERROR_CODES.internal, { status, requestId })
  }

  // 有响应体：走信封解析（含 responseType:'text' 时的 JSON 字符串）
  if (ae.response) {
    return appErrorFromBody(ae.response.data, { status, headerReqId: requestId, fallbackMsg: translateApiError(ae?.message || '网络错误') })
  }

  // 无响应体（网络/超时）：保留小白友好翻译
  return new AppError(translateApiError(ae?.message || '网络错误'), ERROR_CODES.internal, { requestId })
}

http.interceptors.response.use(
  (res) => res,
  (err) => {
    if (isAppError(err)) return Promise.reject(err)
    return Promise.reject(toAppError(err))
  },
)

// ──────── 工作区 ────────

export async function getWorkspaceInfo(): Promise<WorkspaceInfo> {
  const { data } = await http.get<ApiResponse<WorkspaceInfo>>('/workspace/info')
  return unwrapData<WorkspaceInfo>(data)
}

export async function initWorkspace(req: InitRequest): Promise<void> {
  await http.post('/workspace/init', req)
}

// ──────── 文件 ────────

export async function listFiles(params: {
  status?: string
  tag?: string
  page?: number
  pageSize?: number
  sort?: string
}): Promise<PaginatedData<FileItem>> {
  const { data } = await http.get<ApiResponse<PaginatedData<FileItem>>>('/files', { params })
  return unwrapData<PaginatedData<FileItem>>(data)
}

export async function getRecentFiles(params?: {
  window?: number
  limit?: number
}): Promise<{ items: FileItem[]; window: number }> {
  const { data } = await http.get<ApiResponse<{ items: FileItem[]; window: number }>>('/files/recent', { params })
  return unwrapData<{ items: FileItem[]; window: number }>(data)
}

export async function getFile(id: number): Promise<FileItem> {
  const { data } = await http.get<ApiResponse<FileItem>>(`/files/${id}`)
  return unwrapData<FileItem>(data)
}

export async function getFileHistory(id: number): Promise<{ commits: any[] }> {
  const { data } = await http.get<ApiResponse<{ commits: any[] }>>(`/files/${id}/history`)
  return unwrapData<{ commits: any[] }>(data)
}

export async function downloadHistoryVersion(relPath: string, hash: string): Promise<Blob> {
  const res = await http.get('/files/download-history', {
    params: { relPath, hash },
    responseType: 'blob',
  })
  return res.data as Blob
}

export async function resolveFileId(relPath: string): Promise<number> {
  const { data } = await http.get<ApiResponse<{ fileId: number }>>('/files/resolve', { params: { relPath } })
  return unwrapData<{ fileId: number }>(data).fileId
}

// 恢复文件到指定历史版本（后端会先自动保存当前状态，小白可一键找回）
export async function restoreFile(id: number, commitHash: string): Promise<void> {
  await http.post(`/files/${id}/restore`, { commitHash })
}

// 查看某版本中文件的文本内容（版本预览）
export async function getCommitFileContent(commitHash: string, path: string): Promise<string> {
  const res = await http.get(`/commits/${commitHash}/content`, {
    params: { path },
    responseType: 'text',
  })
  return res.data as string
}

export async function updateFileTags(
  id: number,
  add: string[],
  remove: string[],
): Promise<void> {
  await http.post(`/files/${id}/tags`, { add, remove })
}

// ──────── 搜索 ────────

export async function searchFiles(params: {
  q: string
  tag?: string
  page?: number
}): Promise<{ items: SearchResult[]; total: number; page: number }> {
  const { data } = await http.get<ApiResponse<{ items: SearchResult[]; total: number; page: number }>>('/search', { params })
  return unwrapData<{ items: SearchResult[]; total: number; page: number }>(data)
}

export async function reindexAll(): Promise<void> {
  await http.post('/index/reindex')
}

export async function retryFile(id: number): Promise<void> {
  await http.post(`/files/${id}/retry`)
}

// ──────── 文件浏览（资源管理器） ────────

export async function browseDir(path?: string): Promise<BrowseDirResponse> {
  const { data } = await http.get<ApiResponse<BrowseDirResponse>>('/browse', { params: path ? { path } : {} })
  return unwrapData<BrowseDirResponse>(data)
}

export async function browseSearch(q: string, limit?: number): Promise<BrowseSearchResponse> {
  const { data } = await http.get<ApiResponse<BrowseSearchResponse>>('/browse/search', { params: { q, limit } })
  return unwrapData<BrowseSearchResponse>(data)
}

export async function browseOpen(relPath: string): Promise<void> {
  await http.post('/browse/open', { relPath })
}

export async function browsePickDir(initial?: string): Promise<BrowsePickDirResponse> {
  // 原生目录选择对话框：请求会一直挂到用户在对话框里选完/取消才返回（含用户思考时间）。
  // 后端已常驻 PowerShell 预加载，首次点击也秒开；此处只需给足用户选择时间即可。
  const { data } = await http.post<ApiResponse<BrowsePickDirResponse>>('/browse/pickdir', { initial }, { timeout: 180000 })
  return unwrapData<BrowsePickDirResponse>(data)
}

// ──────── 标签 ────────

export async function listTags(): Promise<TagInfo[]> {
  const { data } = await http.get<ApiResponse<{ tags: TagInfo[] }>>('/tags')
  return unwrapData<{ tags: TagInfo[] }>(data).tags
}

export async function listTagSuggestions(): Promise<TagSuggestion[]> {
  const { data } = await http.get<ApiResponse<{ suggestions: TagSuggestion[] }>>('/tag-suggestions')
  return unwrapData<{ suggestions: TagSuggestion[] }>(data).suggestions
}

export async function acceptSuggestion(id: number): Promise<void> {
  await http.post(`/tag-suggestions/${id}/accept`)
}

export async function rejectSuggestion(id: number): Promise<void> {
  await http.post(`/tag-suggestions/${id}/reject`)
}

// ──────── 问答 ────────

export async function listQASessions(): Promise<QASession[]> {
  const { data } = await http.get<ApiResponse<{ sessions: QASession[] }>>('/qa/sessions')
  return unwrapData<{ sessions: QASession[] }>(data).sessions
}

export async function getQAMessages(sessionId: number): Promise<QAMessage[]> {
  const { data } = await http.get<ApiResponse<{ messages: QAMessage[] }>>(`/qa/sessions/${sessionId}/messages`)
  return unwrapData<{ messages: QAMessage[] }>(data).messages
}

export async function askQuestionStream(
  params: { question: string; mode: string; fileId?: number; sessionId?: number },
  onChunk: (chunk: string) => void,
  onDone: (result: { sessionId: number; sources: any[] }) => void,
  onError: (err: string) => void,
  signal: AbortSignal,
): Promise<void> {
  try {
    const res = await fetch('/api/qa/stream', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(params),
      signal,
    })
    if (!res.ok) {
      // 非 2xx：正文为统一信封（含稳定 code / message / requestId），解析为 AppError 再取值
      let raw: unknown = null
      try {
        raw = await res.json()
      } catch {
        // 非 JSON 正文（旧后端/网关），忽略，走通用文案
      }
      const appErr = appErrorFromBody(raw, {
        status: res.status,
        headerReqId: res.headers.get('x-request-id') || undefined,
        fallbackMsg: translateApiError(`请求失败（${res.status}）`),
      })
      onError(appErr.requestId ? `${appErr.message}（错误编号 ${appErr.requestId}）` : appErr.message)
      return
    }
    const reader = res.body?.getReader()
    if (!reader) {
      onError('响应体不可读')
      return
    }
    const decoder = new TextDecoder()
    let buffer = ''
    let eventType = '' // 当前事件类型

    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })

      // 按双换行分割事件
      const parts = buffer.split('\n\n')
      buffer = parts.pop() || ''

      for (const part of parts) {
        const lines = part.split('\n')
        let dataLines: string[] = []

        for (const line of lines) {
          const trimmed = line.trim()
          if (trimmed.startsWith('event: ')) {
            eventType = trimmed.slice(7).trim()
          } else if (trimmed.startsWith('data: ')) {
            dataLines.push(trimmed.slice(6))
          }
        }

        const payload = dataLines.join('\n')

        if (eventType === 'done') {
          try {
            const parsed = JSON.parse(payload)
            onDone({ sessionId: parsed.sessionId || 0, sources: parsed.sources || [] })
          } catch {
            onDone({ sessionId: 0, sources: [] })
          }
          eventType = ''
        } else if (eventType === 'error') {
          // error 数据可能是 JSON 编码的（后端 2025 版起），也可能是明文（旧版）
          let errMsg = payload
          try {
            const parsed = JSON.parse(payload)
            if (typeof parsed === 'string') errMsg = parsed
          } catch {
            // 明文，原样使用
          }
          onError(errMsg)
          eventType = ''
        } else if (payload) {
          // 流式增量：后端以 JSON 字符串编码（含换行的 chunk 安全传输），解码还原；兼容旧版明文
          let text = payload
          try {
            const parsed = JSON.parse(payload)
            if (typeof parsed === 'string') text = parsed
          } catch {
            // 明文，原样使用
          }
          onChunk(text)
        }
      }
    }
  } catch (e: any) {
    if (e.name === 'AbortError' || e?.code === 'ERR_CANCELED') {
      onError('已取消')
    } else if (isAppError(e)) {
      onError(e.requestId ? `${e.message}（错误编号 ${e.requestId}）` : e.message)
    } else {
      onError(translateApiError(e?.message || '网络错误'))
    }
  }
}

export async function deleteQASession(id: number): Promise<void> {
  await http.delete(`/qa/sessions/${id}`)
}

// ──────── 统计 ────────

export async function getStats(params?: {
  range?: string
  from?: number
  to?: number
}): Promise<{ enabled: boolean; metrics?: StatsMetrics }> {
  const { data } = await http.get<ApiResponse<{ enabled: boolean; metrics?: StatsMetrics }>>('/stats', { params })
  return unwrapData<{ enabled: boolean; metrics?: StatsMetrics }>(data)
}

export async function exportStats(format: string, params?: {
  range?: string
}): Promise<Blob> {
  const res = await http.get('/stats/export', {
    params: { format, ...params },
    responseType: 'blob',
  })
  return res.data
}

// ──────── 提交 ────────

export async function autoCommit(): Promise<{ hash: string | null; message?: string; ai?: string }> {
  const { data } = await http.post<ApiResponse<{ skipped?: boolean; hash?: string; message?: string; ai?: string }>>('/commits/auto')
  const d = unwrapData<{ skipped?: boolean; hash?: string; message?: string; ai?: string }>(data)
  if (d?.skipped) return { hash: null }
  return { hash: d?.hash ?? null, message: d?.message, ai: d?.ai }
}

export interface CommitFileStatus {
  relPath: string
  code: string // M/D/A/??
}

export async function getCommitStatus(): Promise<{
  files: CommitFileStatus[]
  count: number
}> {
  const { data } = await http.get<ApiResponse<{ files: CommitFileStatus[]; count: number }>>('/commits/status')
  return unwrapData<{ files: CommitFileStatus[]; count: number }>(data)
}

export async function suggestCommitMessage(): Promise<string> {
  const { data } = await http.post<ApiResponse<{ suggestion: string }>>('/commits/suggest')
  return unwrapData<{ suggestion: string }>(data).suggestion
}

export async function manualCommit(message: string): Promise<string> {
  const { data } = await http.post<ApiResponse<{ hash: string; skipped?: boolean }>>('/commits/manual', { message })
  return unwrapData<{ hash: string; skipped?: boolean }>(data).hash
}

export async function getCommitList(withFiles?: boolean): Promise<CommitItem[]> {
  const { data } = await http.get<ApiResponse<{ commits: CommitItem[] }>>('/commits/list', { params: withFiles ? { withFiles: 'true' } : undefined })
  return unwrapData<{ commits: CommitItem[] }>(data)?.commits || []
}

export async function getCommitFiles(commitHash: string): Promise<VersionFile[]> {
  const { data } = await http.get<ApiResponse<{ files: VersionFile[] }>>(`/commits/${commitHash}/files`)
  return unwrapData<{ files: VersionFile[] }>(data)?.files || []
}

// getCommitDiff 获取单个提交的改动文件列表（新增/修改/删除），供版本记录页展开时按需获取。
export async function getCommitDiff(commitHash: string): Promise<CommitFile[]> {
  const { data } = await http.get<ApiResponse<{ files: CommitFile[] }>>(`/commits/${commitHash}/diff`)
  return unwrapData<{ files: CommitFile[] }>(data)?.files || []
}

// ──────── 设置 ────────

export async function getSettings(): Promise<Record<string, any>> {
  const { data } = await http.get<ApiResponse<Record<string, any>>>('/settings')
  return unwrapData<Record<string, any>>(data)
}

export async function updateSettings(settings: Record<string, any>): Promise<{ restartRequired: string[]; reindexRequired: boolean }> {
  const { data } = await http.put<ApiResponse<{ restartRequired: string[]; reindexRequired: boolean }>>('/settings', settings)
  return unwrapData<{ restartRequired: string[]; reindexRequired: boolean }>(data) ?? { restartRequired: [], reindexRequired: false }
}

export async function updateSecrets(llmApiKey?: string, embedApiKey?: string, rerankApiKey?: string): Promise<void> {
  await http.put('/settings/secrets', { llmApiKey, embedApiKey, rerankApiKey })
}

// ──────── 测试 ────────

export async function testMarkitdown(pythonPath: string, command: string): Promise<ProbeResult> {
  const { data } = await http.post<ApiResponse<ProbeResult>>('/test/markitdown', { pythonPath, command })
  return unwrapData<ProbeResult>(data)
}

export interface LLMTestParams {
  type: 'chat' | 'embed' | 'rerank'
  baseUrl?: string
  model?: string
  apiKey?: string
  temperature?: number
}

export async function testLLM(params: LLMTestParams): Promise<ProbeResult> {
  const { data } = await http.post<ApiResponse<ProbeResult>>('/test/llm', params)
  return unwrapData<ProbeResult>(data)
}

// 获取端点支持的模型列表（「获取模型」按钮）；kind 区分 chat/embed/rerank，后端回退对应已保存密钥
export async function fetchModels(params: { kind: 'chat' | 'embed' | 'rerank'; baseUrl: string; apiKey?: string }): Promise<string[]> {
  const { data } = await http.post<ApiResponse<{ ok: boolean; models?: string[]; message?: string }>>('/test/llm', {
    type: 'models',
    kind: params.kind,
    baseUrl: params.baseUrl,
    apiKey: params.apiKey,
  })
  const d = unwrapData<{ ok: boolean; models?: string[]; message?: string }>(data)
  if (!d?.ok) {
    // 业务失败（200 响应）：以 AppError 抛出，携带 requestId 供界面定位
    throw new AppError(d?.message || '获取模型失败', ERROR_CODES.llmError, { requestId: data.requestId })
  }
  return d?.models || []
}

export interface PythonDetectResult {
  path: string
  ok: boolean
  version?: string
  markitdownCmd?: string
  error?: string
}

export async function detectPython(): Promise<PythonDetectResult> {
  const { data } = await http.get<ApiResponse<{ results: PythonDetectResult[] }>>('/python/detect')
  const results = unwrapData<{ results: PythonDetectResult[] }>(data)?.results || []
  const found = results.find((r) => r.ok) || results[0] || { path: '', ok: false, error: '未检测到 Python' }
  return found
}

// ──────── 任务队列 ────────

export interface QueueStatus {
  running: number
  pending: number
  paused: boolean
  error?: string
}

export async function getQueueStatus(): Promise<QueueStatus> {
  const { data } = await http.get<ApiResponse<QueueStatus>>('/queue/status')
  return unwrapData<QueueStatus>(data)
}

export async function pauseQueue(): Promise<void> {
  await http.post('/queue/pause')
}

export async function resumeQueue(): Promise<void> {
  await http.post('/queue/resume')
}

// ──────── SSE ────────

export function createSSEConnection(onEvent: (topic: string, data: any) => void): () => void {
  let eventSource: EventSource | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let closed = false

  function connect() {
    if (closed) return

    eventSource = new EventSource('/api/events')

    eventSource.onmessage = (e) => {
      try {
        const parsed = JSON.parse(e.data)
        if (parsed.topic && parsed.data !== undefined) {
          onEvent(parsed.topic, parsed.data)
        }
      } catch {
        // ignore parse errors
      }
    }

    eventSource.onerror = () => {
      eventSource?.close()
      if (!closed) {
        reconnectTimer = setTimeout(connect, 3000)
      }
    }
  }

  connect()

  return () => {
    closed = true
    if (reconnectTimer) clearTimeout(reconnectTimer)
    eventSource?.close()
  }
}