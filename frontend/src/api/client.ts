// client.ts — Wails v3 模式 API 客户端
// 所有后端通信通过 Wails 绑定完成，无 HTTP 服务器。

import { Events } from '@wailsio/runtime'

import * as WorkspaceService from '../../bindings/memora/internal/assembler/workspaceservice.js'
import * as FilesService from '../../bindings/memora/internal/assembler/filesservice.js'
import * as SearchService from '../../bindings/memora/internal/assembler/searchservice.js'
import * as IndexService from '../../bindings/memora/internal/assembler/indexservice.js'
import * as BrowseService from '../../bindings/memora/internal/assembler/browseservice.js'
import * as TagsService from '../../bindings/memora/internal/assembler/tagsservice.js'
import * as QAService from '../../bindings/memora/internal/assembler/qaservice.js'
import * as StatsService from '../../bindings/memora/internal/assembler/statsservice.js'
import * as CommitsService from '../../bindings/memora/internal/assembler/commitsservice.js'
import * as SettingsService from '../../bindings/memora/internal/assembler/settingsservice.js'
import * as TestService from '../../bindings/memora/internal/assembler/testservice.js'
import * as QueueService from '../../bindings/memora/internal/assembler/queueservice.js'

import { AppError, ERROR_CODES } from '@/utils/errors'
import type { InitRequest } from '@/types'

export function translateApiError(msg: string): string {
  if (!msg || !msg.trim()) return '操作失败，请重试'
  const m = msg.trim()
  // 取消
  if (m.includes('canceled') || m.includes('cancel') || m === '已取消') return '操作已取消'
  // 网络 / 超时 / 连接失败
  if (
    m.includes('Network Error') ||
    m.includes('timeout') ||
    m.includes('无法连接') ||
    m.includes('Failed to fetch') ||
    m.includes('fetch failed') ||
    m.includes('网络异常')
  ) {
    return '无法连接服务，请检查网络或稍后重试'
  }
  // 端点未配置 → 引导去设置对应区块
  if (m.includes('聊天端点未配置') || m.includes('未配置聊天')) {
    return 'AI 助手还未连接，请到「设置 → AI 助手」里连接'
  }
  if (m.includes('嵌入端点未配置') || m.includes('未配置嵌入')) {
    return '「按内容搜索」还未连接，请到「设置 → 内容整理模型」里连接'
  }
  if (m.includes('重排端点未配置') || m.includes('未配置重排')) {
    return '「排序优化」还未连接，请到「设置 → 排序优化」里连接'
  }
  // 工作区未初始化 → 引导去设置选择文件夹
  if (m.includes('工作区未初始化') || m.includes('仓库未初始化') || m.includes('未选择工作区')) {
    return '还没有选择要管理的文件夹，请到「设置」里选择'
  }
  // 鉴权失败 / API Key 问题
  if (
    m.includes('401') ||
    m.includes('unauthorized') ||
    m.includes('invalid api key') ||
    m.includes('api key 不正确') ||
    m.includes('API Key')
  ) {
    return 'API Key 不正确或已失效，请检查后重试'
  }
  // 403 无权限
  if (m.includes('403') || m.includes('forbidden')) {
    return '没有权限访问该服务，请检查账号权限或 API Key'
  }
  // 429 限流
  if (m.includes('429') || m.includes('rate limit') || m.includes('too many requests')) {
    return '请求太频繁被限流了，稍等片刻再试'
  }
  // 模型不存在
  if (m.includes('model not found') || m.includes('模型不存在') || /model\s+"[^"]+"\s+not found/.test(m)) {
    return '模型名称不正确，请检查模型设置'
  }
  // 未匹配：原样返回（不吞掉可读信息）
  return m
}

function fromBindingError(err: unknown, fallback: string = '请求失败'): AppError {
  if (err instanceof AppError) return err
  const msg = typeof err === 'string' ? err : err instanceof Error ? err.message : fallback
  return new AppError(msg, ERROR_CODES.internal)
}

// ──────── 工作区 ────────

export async function getWorkspaceInfo(): Promise<any> {
  try { return await WorkspaceService.Info() } catch (e) { throw fromBindingError(e) }
}
export async function initWorkspace(req: InitRequest): Promise<void> {
  try {
    // 完整传递工作区路径 + markitdown/llm/embed/rerank 配置区块，
    // 后端在初始化时一并落盘（修复：此前只传 workspace，AI 配置被静默丢弃）
    await WorkspaceService.Init({
      workspace: req.workspacePath,
      markitdown: {
        pythonPath: req.markitdown?.pythonPath || '',
        command: req.markitdown?.command || '',
      },
      // 绑定签名：llm 可空（null）；embed/rerank 为必填对象，未配置时传空对象
      llm: req.llm
        ? {
            baseUrl: req.llm.baseUrl || '',
            apiKey: req.llm.apiKey || '',
            model: req.llm.model || '',
            temperature: req.llm.temperature || 0,
          }
        : null,
      embed: {
        baseUrl: req.embed?.baseUrl || '',
        apiKey: req.embed?.apiKey || '',
        model: req.embed?.model || '',
        dimensions: req.embed?.dimensions || 0,
      },
      rerank: {
        baseUrl: req.rerank?.baseUrl || '',
        apiKey: req.rerank?.apiKey || '',
        model: req.rerank?.model || '',
      },
    })
  } catch (e) { throw fromBindingError(e) }
}

// ──────── 文件 ────────

export async function listFiles(
  params: { status?: string; tag?: string; page?: number; pageSize?: number; sort?: string },
  _opts?: { signal?: AbortSignal },
): Promise<any> {
  try {
    // Wails 绑定直接返回 { items, total, page }，不再包装 HTTP 信封
    return await FilesService.List({ status: params.status || '', tag: params.tag || '', page: params.page || 0, pageSize: params.pageSize || 50, sort: params.sort || '', windowHours: 0 })
  } catch (e) { throw fromBindingError(e) }
}
export async function getRecentFiles(params?: { window?: number; limit?: number }): Promise<any> {
  try {
    // windowHours 透传后端换算时间窗（修复：此前 window 参数被丢弃、时间窗筛选失效）
    return await FilesService.Recent({ status: '', tag: '', page: 0, pageSize: params?.limit || 50, sort: '', windowHours: params?.window || 0 })
  } catch (e) { throw fromBindingError(e) }
}
export async function getFile(id: number): Promise<any> {
  try { return await FilesService.Get(id) } catch (e) { throw fromBindingError(e) }
}
export async function getFileHistory(id: number): Promise<any> {
  try {
    const res = await FilesService.History(id)
    return { commits: res?.commits || [] }
  } catch (e) { throw fromBindingError(e) }
}
export async function downloadHistoryVersion(relPath: string, hash: string): Promise<Blob> {
  try {
    const content = await FilesService.DownloadHistory(relPath, hash)
    return new Blob([content as string], { type: 'text/plain' })
  } catch (e) { throw fromBindingError(e) }
}
export async function resolveFileId(relPath: string): Promise<number> {
  try { return await FilesService.Resolve(relPath) } catch (e) { throw fromBindingError(e) }
}
export async function restoreFile(id: number, commitHash: string): Promise<void> {
  try { await FilesService.Restore({ id, commitHash }) } catch (e) { throw fromBindingError(e) }
}
export async function getCommitFileContent(commitHash: string, path: string): Promise<string> {
  try { return await FilesService.CommitFileContent(commitHash, path) } catch (e) { throw fromBindingError(e) }
}
export async function updateFileTags(id: number, add: string[], remove: string[]): Promise<void> {
  try { await FilesService.UpdateTags({ id, add, remove }) } catch (e) { throw fromBindingError(e) }
}

// ──────── 搜索 ────────

export async function searchFiles(params: { q: string; tag?: string; page?: number }): Promise<any> {
  try {
    return await SearchService.Search({ q: params.q, tag: params.tag || '', page: params.page || 0, tagFilter: [] })
  } catch (e) { throw fromBindingError(e) }
}
export async function reindexAll(): Promise<void> {
  try { await IndexService.Reindex() } catch (e) { throw fromBindingError(e) }
}
export async function retryFile(id: number): Promise<void> {
  try { await FilesService.Retry(id) } catch (e) { throw fromBindingError(e) }
}

// ──────── 文件浏览 ────────

export async function browseDir(path?: string): Promise<any> {
  try {
    return await BrowseService.Dir({ path: path || '' })
  } catch (e) { throw fromBindingError(e) }
}
export async function browseSearch(q: string, limit?: number): Promise<any> {
  try {
    const res = await BrowseService.SearchByName({ q, limit: limit || 100 })
    // 后端返回 { results, total }，前端统一按 { items, total } 消费（与语义搜索形状一致）
    return { items: res?.results || [], total: res?.total || 0 }
  } catch (e) { throw fromBindingError(e) }
}
export async function browseOpen(relPath: string): Promise<void> {
  try { await BrowseService.OpenFile(relPath) } catch (e) { throw fromBindingError(e) }
}
export async function browsePickDir(initial?: string, kind?: string): Promise<any> {
  try {
    const res: any = await BrowseService.PickDir({ initial: initial || "", kind: kind || "dir" })
    return { path: res?.path || "", cancelled: !res?.path }
  } catch (e) { throw fromBindingError(e) }
}

// ──────── 标签 ────────

export async function listTags(): Promise<any> {
  try {
    const res = await TagsService.List()
    return res?.tags || []
  } catch (e) { throw fromBindingError(e) }
}
export async function listTagSuggestions(): Promise<any> {
  try {
    const res = await TagsService.Suggestions()
    return res?.suggestions || []
  } catch (e) { throw fromBindingError(e) }
}
export async function acceptSuggestion(id: number): Promise<void> {
  try { await TagsService.AcceptSuggestion(id) } catch (e) { throw fromBindingError(e) }
}
export async function rejectSuggestion(id: number): Promise<void> {
  try { await TagsService.RejectSuggestion(id) } catch (e) { throw fromBindingError(e) }
}

// ──────── 问答 ────────

export async function listQASessions(): Promise<any> {
  try {
    const res = await QAService.Sessions()
    return res?.sessions || []
  } catch (e) { throw fromBindingError(e) }
}
export async function getQAMessages(sessionId: number): Promise<any> {
  try {
    const res = await QAService.Messages(sessionId)
    return res?.messages || []
  } catch (e) { throw fromBindingError(e) }
}
export async function askQuestionStream(
  params: { question: string; mode: string; fileId?: number; sessionId?: number },
  onChunk: (chunk: string) => void,
  onDone: (result: { sessionId: number; sources: any[]; answer?: string }) => void,
  onError: (err: string) => void,
  signal: AbortSignal,
): Promise<void> {
  // 流式事件 id 由前端生成并随请求回传后端（qa:chunk:<id> / qa:done:<id> / qa:error:<id>）。
  // 事件名是订阅/emit 双向握手，id 必须同源，否则后端 emit 的事件前端永远收不到
  // （修复：此前两端各自生成 id，问答事件永不匹配、sending 卡死）。
  const id = Date.now() + '-' + Math.random().toString(36).slice(2, 8)
  const evChunk = 'qa:chunk:' + id, evDone = 'qa:done:' + id, evError = 'qa:error:' + id
  let cancelled = false
  const cleanup = () => {
    if (cancelled) return
    cancelled = true
    signal.removeEventListener('abort', onAbort)
    Events.Off(evChunk)
    Events.Off(evDone)
    Events.Off(evError)
  }
  const onAbort = () => { if (!cancelled) { cleanup(); onError('已取消') } }
  signal.addEventListener('abort', onAbort)
  try {
    Events.On(evChunk, (payload: any) => {
      if (cancelled) return
      const chunk = typeof payload === 'string' ? payload : payload?.data || payload
      onChunk(chunk as string)
    })
    Events.On(evDone, (payload: any) => {
      if (cancelled) return
      cleanup()
      const data = typeof payload === 'string' ? JSON.parse(payload) : payload?.data || payload
      onDone({ sessionId: data?.sessionId || 0, sources: data?.sources || [], answer: data?.answer || '' })
    })
    Events.On(evError, (payload: any) => {
      if (cancelled) return
      cleanup()
      const err = typeof payload === 'string' ? payload : payload?.data || '连接中断，请重试'
      onError(err as string)
    })
    await QAService.AskStream({ question: params.question, mode: params.mode, fileId: params.fileId || 0, sessionId: params.sessionId || 0, requestId: id }, '')
    // 后端流已结束但未发出 done/error（异常路径）：视为连接中断，避免 sending 永久卡死
    if (!cancelled) {
      cleanup()
      onError('连接中断，请重试')
    }
  } catch (e) {
    cleanup()
    onError(translateApiError(fromBindingError(e, '请求失败').message))
  }
}
export async function deleteQASession(id: number): Promise<void> {
  try { await QAService.DeleteSession(id) } catch (e) { throw fromBindingError(e) }
}

// ──────── 统计 ────────

export async function getStats(params?: { range?: string; from?: number; to?: number }): Promise<any> {
  try {
    return await StatsService.Get({ range: params?.range || '', from: params?.from || 0, to: params?.to || 0 })
  } catch (e) { throw fromBindingError(e) }
}
export async function exportStats(format: string, params?: { range?: string }): Promise<Blob> {
  try {
    const content = await StatsService.Export(format, { range: params?.range || '', from: 0, to: 0 })
    return new Blob([content as string], { type: 'text/plain' })
  } catch (e) { throw fromBindingError(e) }
}

// ──────── 提交 ────────

export async function autoCommit(): Promise<{ hash: string | null; message?: string; ai?: string }> {
  try {
    const res = await CommitsService.AutoCommit()
    if (res?.skipped) return { hash: null }
    return { hash: res?.hash || null, message: res?.message, ai: res?.ai }
  } catch (e) { throw fromBindingError(e) }
}
export interface CommitFileStatus { relPath: string; code: string }
export async function getCommitStatus(): Promise<{ files: CommitFileStatus[]; count: number }> {
  try {
    const res = await CommitsService.Status()
    return { files: res?.files || [], count: res?.count || 0 }
  } catch (e) { throw fromBindingError(e) }
}
export async function suggestCommitMessage(): Promise<string> {
  try {
    const res = await CommitsService.Suggest()
    return res?.suggestion || ''
  } catch (e) { throw fromBindingError(e) }
}
export async function manualCommit(message: string): Promise<string> {
  try {
    const res = await CommitsService.Manual({ message })
    return res?.hash || ''
  } catch (e) { throw fromBindingError(e) }
}
export async function getCommitList(): Promise<any[]> {
  try {
    const res = await CommitsService.List()
    return res?.commits || []
  } catch (e) { throw fromBindingError(e) }
}
export async function getCommitFiles(commitHash: string): Promise<any[]> {
  try {
    const res = await CommitsService.Files(commitHash)
    return res?.files || []
  } catch (e) { throw fromBindingError(e) }
}
export async function getCommitDiff(commitHash: string): Promise<any[]> {
  try {
    const res = await CommitsService.TreeAt(commitHash)
    return res?.files || []
  } catch (e) { throw fromBindingError(e) }
}

// ──────── 设置 ────────

export async function getSettings(): Promise<Record<string, any>> {
  try { return (await SettingsService.Get()) as Record<string, any> } catch (e) { throw fromBindingError(e) }
}
export async function updateSettings(settings: Record<string, any>): Promise<{ restartRequired: string[]; reindexRequired: boolean }> {
  try {
    const res = await SettingsService.UpdateSettings(settings)
    return { restartRequired: res?.restartRequired || [], reindexRequired: res?.reindexRequired || false }
  } catch (e) { throw fromBindingError(e) }
}
export async function updateSecrets(llmApiKey?: string, embedApiKey?: string, rerankApiKey?: string): Promise<void> {
  try { await SettingsService.UpdateSecrets({ llmApiKey: llmApiKey || '', embedApiKey: embedApiKey || '', rerankApiKey: rerankApiKey || '' }) } catch (e) { throw fromBindingError(e) }
}

// ──────── 测试 ────────

export async function testMarkitdown(pythonPath: string, command: string): Promise<any> {
  try { return await TestService.TestMarkItDown({ pythonPath, command }) } catch (e) { throw fromBindingError(e) }
}
export interface LLMTestParams {
  type: 'chat' | 'embed' | 'rerank'
  baseUrl?: string; model?: string; apiKey?: string; temperature?: number
}
export async function testLLM(params: LLMTestParams): Promise<any> {
  try {
    return await TestService.TestLLM({ type: params.type as any, baseUrl: params.baseUrl || '', apiKey: params.apiKey || '', model: params.model || '', temperature: params.temperature || 0, kind: '' })
  } catch (e) { throw fromBindingError(e) }
}
export async function fetchModels(params: { kind: 'chat' | 'embed' | 'rerank'; baseUrl: string; apiKey?: string }): Promise<string[]> {
  try {
    const res = await TestService.TestLLM({ type: 'models' as any, kind: params.kind, baseUrl: params.baseUrl, apiKey: params.apiKey || '', model: '', temperature: 0 })
    return res?.models || []
  } catch (e) { throw fromBindingError(e) }
}
export interface PythonDetectResult {
  path: string; ok: boolean; version?: string; markitdownCmd?: string; markitdownVersion?: string; error?: string
}
export async function detectPython(): Promise<PythonDetectResult> {
  try { return (await TestService.DetectPython()) as PythonDetectResult } catch (e) { throw fromBindingError(e) }
}
export interface MarkitdownProbeResult {
  path: string; version?: string; ok: boolean; error?: string
}
export async function probeMarkitdown(pythonPath?: string): Promise<MarkitdownProbeResult> {
  try { return (await TestService.ProbeMarkitdown(pythonPath || '')) as MarkitdownProbeResult } catch (e) { throw fromBindingError(e) }
}

// ──────── 任务队列 ────────

export interface QueueStatus { running: number; pending: number; paused: boolean; error?: string }
export async function getQueueStatus(): Promise<QueueStatus> {
  try { return (await QueueService.Status()) as QueueStatus } catch (e) { throw fromBindingError(e) }
}
export async function pauseQueue(): Promise<void> {
  try { await QueueService.Pause() } catch (e) { throw fromBindingError(e) }
}
export async function resumeQueue(): Promise<void> {
  try { await QueueService.Resume() } catch (e) { throw fromBindingError(e) }
}

// ──────── Wails 事件连接（替代原 SSE EventSource） ────────

const INTERNAL_EVENTS = [
  'index_progress', 'files_changed', 'tag_done', 'commit_done',
  'task_queue', 'settings_changed', 'stats_updated', 'qa_ready',
]
export function createSSEConnection(
  onEvent: (topic: string, data: any) => void,
  _opts?: { onOpen?: () => void; onClose?: (hadError: boolean) => void },
): () => void {
  const handlers = INTERNAL_EVENTS.map((topic) => {
    const eventName = 'memora:' + topic
    Events.On(eventName, (payload: any) => {
      const data = typeof payload === 'string' ? JSON.parse(payload) : payload?.data || payload
      onEvent(topic, data)
    })
    return () => Events.Off(eventName)
  })
  return () => { handlers.forEach((h) => h()) }
}