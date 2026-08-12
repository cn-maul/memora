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

export function translateApiError(msg: string): string {
  if (msg.includes('canceled') || msg.includes('cancel')) return '操作已取消'
  if (msg.includes('timeout')) return '请求超时'
  return msg || '未知错误'
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
export async function initWorkspace(req: { workspacePath: string }): Promise<void> {
  try { await WorkspaceService.Init({ workspace: req.workspacePath }) } catch (e) { throw fromBindingError(e) }
}

// ──────── 文件 ────────

export async function listFiles(
  params: { status?: string; tag?: string; page?: number; pageSize?: number; sort?: string },
  _opts?: { signal?: AbortSignal },
): Promise<any> {
  try {
    const res = await FilesService.List({ status: params.status || '', tag: params.tag || '', page: params.page || 0, pageSize: params.pageSize || 50, sort: params.sort || '' })
    return { data: { code: 'ok', data: res } }
  } catch (e) { throw fromBindingError(e) }
}
export async function getRecentFiles(params?: { window?: number; limit?: number }): Promise<any> {
  try {
    const res = await FilesService.Recent({ status: '', tag: '', page: 0, pageSize: params?.limit || 20, sort: '' })
    return { data: { code: 'ok', data: res } }
  } catch (e) { throw fromBindingError(e) }
}
export async function getFile(id: number): Promise<any> {
  try { return await FilesService.Get(id) } catch (e) { throw fromBindingError(e) }
}
export async function getFileHistory(id: number): Promise<any> {
  try {
    const res = await FilesService.History(id)
    return { data: { code: 'ok', data: { commits: res?.commits || [] } } }
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
    const res = await SearchService.Search({ q: params.q, tag: params.tag || '', page: params.page || 0, tagFilter: [] })
    return { data: { code: 'ok', data: res } }
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
    const res = await BrowseService.Dir({ path: path || '' })
    return { data: { code: 'ok', data: res } }
  } catch (e) { throw fromBindingError(e) }
}
export async function browseSearch(q: string, limit?: number): Promise<any> {
  try {
    const res = await BrowseService.SearchByName({ q, limit: limit || 100 })
    return { data: { code: 'ok', data: res } }
  } catch (e) { throw fromBindingError(e) }
}
export async function browseOpen(relPath: string): Promise<void> {
  try { await BrowseService.OpenFile(relPath) } catch (e) { throw fromBindingError(e) }
}
export async function browsePickDir(initial?: string): Promise<any> {
  try { return await BrowseService.PickDir(initial || '') } catch (e) { throw fromBindingError(e) }
}

// ──────── 标签 ────────

export async function listTags(): Promise<any> {
  try {
    const res = await TagsService.List()
    return { data: { code: 'ok', data: { tags: res?.tags || [] } } }
  } catch (e) { throw fromBindingError(e) }
}
export async function listTagSuggestions(): Promise<any> {
  try {
    const res = await TagsService.Suggestions()
    return { data: { code: 'ok', data: { suggestions: res?.suggestions || [] } } }
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
    return { data: { code: 'ok', data: { sessions: res?.sessions || [] } } }
  } catch (e) { throw fromBindingError(e) }
}
export async function getQAMessages(sessionId: number): Promise<any> {
  try {
    const res = await QAService.Messages(sessionId)
    return { data: { code: 'ok', data: { messages: res?.messages || [] } } }
  } catch (e) { throw fromBindingError(e) }
}
export async function askQuestionStream(
  params: { question: string; mode: string; fileId?: number; sessionId?: number },
  onChunk: (chunk: string) => void,
  onDone: (result: { sessionId: number; sources: any[] }) => void,
  onError: (err: string) => void,
  signal: AbortSignal,
): Promise<void> {
  const id = Date.now() + '-' + Math.random().toString(36).slice(2, 8)
  const evChunk = 'qa:chunk:' + id, evDone = 'qa:done:' + id, evError = 'qa:error:' + id
  let cancelled = false
  const cleanup = () => { cancelled = true; Events.Off(evChunk); Events.Off(evDone); Events.Off(evError) }
  signal.addEventListener('abort', () => { if (!cancelled) { cleanup(); onError('已取消') } })
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
      onDone({ sessionId: data?.sessionId || 0, sources: data?.sources || [] })
    })
    Events.On(evError, (payload: any) => {
      if (cancelled) return
      cleanup()
      const err = typeof payload === 'string' ? payload : payload?.data || '连接中断，请重试'
      onError(err as string)
    })
    await QAService.AskStream({ question: params.question, mode: params.mode, fileId: params.fileId || 0, sessionId: params.sessionId || 0 }, '')
  } catch (e) {
    cleanup()
    onError(fromBindingError(e, '请求失败').message)
  }
}
export async function deleteQASession(id: number): Promise<void> {
  try { await QAService.DeleteSession(id) } catch (e) { throw fromBindingError(e) }
}

// ──────── 统计 ────────

export async function getStats(params?: { range?: string; from?: number; to?: number }): Promise<any> {
  try {
    const res = await StatsService.Get({ range: params?.range || '', from: params?.from || 0, to: params?.to || 0 })
    return { data: { code: 'ok', data: res } }
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
export async function getCommitDiff(commitHash: string): Promise<any> {
  try {
    const res = await CommitsService.TreeAt(commitHash)
    return { data: { code: 'ok', data: { files: res?.files || [] } } }
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
  path: string; ok: boolean; version?: string; markitdownCmd?: string; error?: string
}
export async function detectPython(): Promise<PythonDetectResult> {
  try { return (await TestService.DetectPython()) as PythonDetectResult } catch (e) { throw fromBindingError(e) }
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