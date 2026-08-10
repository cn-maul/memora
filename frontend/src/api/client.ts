import axios from 'axios'
import type {
  ApiResponse,
  WorkspaceInfo,
  InitRequest,
  PaginatedData,
  FileItem,
  TagInfo,
  TagSuggestion,
  SearchResult,
  TimelineNode,
  QASession,
  QAMessage,
  StatsMetrics,
  ProbeResult,
  BrowseDirResponse,
  BrowseSearchResponse,
  BrowsePickDirResponse,
  CommitItem,
  VersionFile,
} from '@/types'

const http = axios.create({
  baseURL: '/api',
  timeout: 30000,
})

http.interceptors.response.use(
  (res) => res,
  (err) => {
    const msg = err.response?.data?.message || err.message || '网络错误'
    return Promise.reject(new Error(msg))
  },
)

// ──────── 工作区 ────────

export async function getWorkspaceInfo(): Promise<WorkspaceInfo> {
  const { data } = await http.get<ApiResponse<WorkspaceInfo>>('/workspace/info')
  return data.data!
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
  return data.data!
}

export async function getRecentFiles(params?: {
  window?: number
  limit?: number
}): Promise<{ items: FileItem[]; window: number }> {
  const { data } = await http.get<ApiResponse<{ items: FileItem[]; window: number }>>('/files/recent', { params })
  return data.data!
}

export async function getFile(id: number): Promise<FileItem> {
  const { data } = await http.get<ApiResponse<FileItem>>(`/files/${id}`)
  return data.data!
}

export async function getFileHistory(id: number): Promise<{ commits: any[] }> {
  const { data } = await http.get<ApiResponse<{ commits: any[] }>>(`/files/${id}/history`)
  return data.data!
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
  return data.data!.fileId
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
  return data.data!
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
  return data.data!
}

export async function browseSearch(q: string, limit?: number): Promise<BrowseSearchResponse> {
  const { data } = await http.get<ApiResponse<BrowseSearchResponse>>('/browse/search', { params: { q, limit } })
  return data.data!
}

export async function browseOpen(relPath: string): Promise<void> {
  await http.post('/browse/open', { relPath })
}

export async function browsePickDir(initial?: string): Promise<BrowsePickDirResponse> {
  const { data } = await http.post<ApiResponse<BrowsePickDirResponse>>('/browse/pickdir', { initial })
  return data.data!
}

// ──────── 标签 ────────

export async function listTags(): Promise<TagInfo[]> {
  const { data } = await http.get<ApiResponse<{ tags: TagInfo[] }>>('/tags')
  return data.data!.tags
}

export async function listTagSuggestions(): Promise<TagSuggestion[]> {
  const { data } = await http.get<ApiResponse<{ suggestions: TagSuggestion[] }>>('/tag-suggestions')
  return data.data!.suggestions
}

export async function acceptSuggestion(id: number): Promise<void> {
  await http.post(`/tag-suggestions/${id}/accept`)
}

export async function rejectSuggestion(id: number): Promise<void> {
  await http.post(`/tag-suggestions/${id}/reject`)
}

// ──────── 时间线 ────────

export async function getTimeline(params: {
  granularity?: string
  tag?: string
  from?: number
  to?: number
}): Promise<TimelineNode[]> {
  const { data } = await http.get<ApiResponse<{ nodes: TimelineNode[] }>>('/timeline', { params })
  return data.data!.nodes
}

// ──────── 问答 ────────

export async function listQASessions(): Promise<QASession[]> {
  const { data } = await http.get<ApiResponse<{ sessions: QASession[] }>>('/qa/sessions')
  return data.data!.sessions
}

export async function getQAMessages(sessionId: number): Promise<QAMessage[]> {
  const { data } = await http.get<ApiResponse<{ messages: QAMessage[] }>>(`/qa/sessions/${sessionId}/messages`)
  return data.data!.messages
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
      const errBody = await res.text()
      onError(`请求失败: ${res.status} ${errBody}`)
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
    if (e.name === 'AbortError') {
      onError('已取消')
    } else {
      onError(e.message || '网络错误')
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
  return data.data!
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
  if (data.data?.skipped) return { hash: null }
  return { hash: data.data!.hash ?? null, message: data.data?.message, ai: data.data?.ai }
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
  return data.data!
}

export async function suggestCommitMessage(): Promise<string> {
  const { data } = await http.post<ApiResponse<{ suggestion: string }>>('/commits/suggest')
  return data.data!.suggestion
}

export async function manualCommit(message: string): Promise<string> {
  const { data } = await http.post<ApiResponse<{ hash: string; skipped?: boolean }>>('/commits/manual', { message })
  return data.data!.hash
}

export async function getCommitList(): Promise<CommitItem[]> {
  const { data } = await http.get<ApiResponse<{ commits: CommitItem[] }>>('/commits/list')
  return data.data!.commits || []
}

export async function getCommitFiles(commitHash: string): Promise<VersionFile[]> {
  const { data } = await http.get<ApiResponse<{ files: VersionFile[] }>>(`/commits/${commitHash}/files`)
  return data.data?.files || []
}

// ──────── 设置 ────────

export async function getSettings(): Promise<Record<string, any>> {
  const { data } = await http.get<ApiResponse<Record<string, any>>>('/settings')
  return data.data!
}

export async function updateSettings(settings: Record<string, any>): Promise<{ restartRequired: string[] }> {
  const { data } = await http.put<ApiResponse<{ restartRequired: string[] }>>('/settings', settings)
  return data.data ?? { restartRequired: [] }
}

export async function updateSecrets(llmApiKey?: string, embedApiKey?: string, rerankApiKey?: string): Promise<void> {
  await http.put('/settings/secrets', { llmApiKey, embedApiKey, rerankApiKey })
}

// ──────── 测试 ────────

export async function testMarkitdown(pythonPath: string, command: string): Promise<ProbeResult> {
  const { data } = await http.post<ApiResponse<ProbeResult>>('/test/markitdown', { pythonPath, command })
  return data.data!
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
  return data.data!
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
  const results = data.data?.results || []
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
  return data.data!
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