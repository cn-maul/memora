// ──────────────────────── 基础类型 ────────────────────────

export interface FileInfo {
  id: number
  relPath: string
  size: number
  mtime: number
  contentHash?: string
  docType: string
  indexStatus: string
  lastError?: string
  firstSeenAt: number
  lastIndexedAt?: number
}

export interface FileItem extends FileInfo {
  tags?: FileTag[]
}

export interface Chunk {
  id: number
  fileId: number
  seq: number
  tokenEst: number
  text: string
}

export interface TagInfo {
  id: number
  name: string
  source: string
  count?: number
  createdAt: number
}

export interface FileTag {
  name: string
  origin: string
}

export interface TagSuggestion {
  id: number
  name: string
  reason?: string
  fileId: number
  relPath?: string
  status: string
  createdAt: number
}

export interface CommitInfo {
  hash: string
  time: number
  message: string
  author: string
}

export interface CommitFile {
  path: string
  status: string // added|modified|deleted
}

export interface CommitItem extends CommitInfo {
  files?: CommitFile[]
}

export interface HeadInfo {
  hash: string
  branch?: string
  countFiles: number
  changedFiles: number
  hasCommits: boolean
}

export interface SearchResult {
  fileId: number
  relPath: string
  hitText: string
  score: number
  tags?: FileTag[]
  mtime: number
  matchedChunks: number
}

export interface TimelineNode {
  bucket: string
  label: string
  count: number
  added: number
  modified: number
  deleted: number
  summary?: string
  files: TimelineFile[]
}

export interface TimelineFile {
  relPath: string
  mtime: number
  commitHash?: string
}

export interface QASession {
  id: number
  createdAt: number
  mode: string
  fileId?: number
  messageCount: number
}

export interface QAMessage {
  id: number
  sessionId: number
  role: string
  content: string
  sources?: string
  createdAt: number
}

export interface StatsMetrics {
  commitsByDay: DayCount[]
  fileChanges: FileChanges
  hotFiles: HotFile[]
  hourBuckets: HourBuckets
  tagDistribution: TagCount[]
  iterationRate: number
}

export interface DayCount {
  date: string
  count: number
}

export interface FileChanges {
  added: number
  modified: number
  deleted: number
}

export interface HotFile {
  relPath: string
  count: number
}

export interface HourBuckets {
  morning: number
  afternoon: number
  evening: number
  night: number
}

export interface TagCount {
  tag: string
  count: number
}

// ──────────────────────── API 响应 ────────────────────────

export interface ApiResponse<T = any> {
  code: string
  data?: T
  message?: string
}

export interface PaginatedData<T> {
  page: number
  pageSize: number
  total: number
  items: T[]
}

export interface SSEvent {
  topic: string
  data: any
}

// ──────────────────────── 设置 ────────────────────────

export interface WorkspaceInfo {
  initialized: boolean
  workspacePath?: string
  dirtyCounts?: Record<string, number>
  head?: HeadInfo
  markitdownConfigured: boolean
  llmConfigured: boolean
  embedConfigured: boolean
}

export interface InitRequest {
  workspacePath: string
  markitdown?: {
    pythonPath?: string
    command?: string
  }
  llm?: {
    baseUrl?: string
    apiKey?: string
    model?: string
    temperature?: number
  }
  embed?: {
    baseUrl?: string
    apiKey?: string
    model?: string
    dimensions?: number
  }
}

export interface ProbeResult {
  ok: boolean
  message: string
}

// ──────────────────────── 文件浏览（资源管理器） ────────────────────────

export interface BrowseEntry {
  name: string
  relPath: string
  isDir: boolean
  size?: number
  mtime?: number
  docType?: string
  indexable?: boolean
  indexStatus?: string
}

export interface BrowseDirResponse {
  path: string
  entries: BrowseEntry[]
}

export interface BrowseSearchItem {
  relPath: string
  isDir: boolean
  size?: number
  mtime?: number
  docType?: string
  indexable?: boolean
}

export interface BrowseSearchResponse {
  query: string
  items: BrowseSearchItem[]
  total: number
}

export interface BrowsePickDirResponse {
  path: string
  cancelled: boolean
}