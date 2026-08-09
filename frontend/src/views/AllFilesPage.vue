<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { browseDir, browseSearch, browseOpen, getFileHistory, downloadHistoryVersion, resolveFileId } from '@/api/client'
import type { BrowseEntry, BrowseSearchItem } from '@/types'
import Icon, { type IconName } from '@/components/Icon.vue'

const router = useRouter()
const route = useRoute()
const ws = useWorkspaceStore()

// 当前浏览的相对目录（'' 表示根）
const currentPath = ref('')
const entries = ref<BrowseEntry[]>([])
const listing = ref(false)
const browseError = ref('')

// 高亮文件（来自问答跳转）
const highlightPath = ref('')

// 文件名搜索
const searchQuery = ref('')
const searchResults = ref<BrowseSearchItem[]>([])
const searchTotal = ref(0)
const searching = ref(false)
const showSearch = ref(false)
const searchError = ref('') // 搜索失败提示（修复：失败不再静默误报"未找到"）

onMounted(async () => {
  await ws.fetchInfo()
  if (ws.initialized) {
    await handleHighlight()
    if (currentPath.value === '') await refreshDir('')
  }
})

function stripFilename(p: string): string {
  const idx = p.lastIndexOf('/')
  return idx > 0 ? p.slice(0, idx) : ''
}

async function handleHighlight() {
  // 从 window.__highlightFile 获取高亮文件路径（来自聊天链接跳转）
  let hl = ''
  if ((window as any).__highlightFile) {
    hl = (window as any).__highlightFile as string
    ;(window as any).__highlightFile = ''
  } else {
    hl = route.query.highlight as string || ''
  }
  if (hl) {
    highlightPath.value = hl
    const dir = stripFilename(hl)
    await refreshDir(dir)
  }
}

watch(
  () => ws.initialized,
  async (v) => {
    if (v) {
      await handleHighlight()
      if (currentPath.value === '') await refreshDir('')
    }
  },
)

// 监听路由 highlight 变化（从聊天链接跳转过来时）
watch(
  () => route.query.highlight,
  async (hl) => {
    // 消费掉 window.__highlightFile，防止残留导致下次 onMounted 高亮旧文件
    ;(window as any).__highlightFile = ''
    if (hl) {
      highlightPath.value = hl as string
      const dir = stripFilename(hl as string)
      await refreshDir(dir)
    }
  },
)

// ──────── 目录加载 ────────

async function refreshDir(subPath: string) {
  listing.value = true
  browseError.value = ''
  try {
    const res = await browseDir(subPath)
    currentPath.value = subPath || ''
    entries.value = res.entries || []
  } catch (e: any) {
    browseError.value = e.message
    entries.value = []
  } finally {
    listing.value = false
  }
}

function goUp() {
  const parent = currentPath.value.split('/').filter(Boolean).slice(0, -1).join('/')
  refreshDir(parent)
}

// 面包屑
const crumbs = computed(() => {
  const parts = currentPath.value ? currentPath.value.split('/').filter(Boolean) : []
  const result = [{ label: '全部文件', path: '' }]
  let acc = ''
  for (const p of parts) {
    acc = acc ? `${acc}/${p}` : p
    result.push({ label: p, path: acc })
  }
  return result
})

// ──────── 搜索 ────────

async function doSearch() {
  const q = searchQuery.value.trim()
  if (!q) {
    showSearch.value = false
    return
  }
  searching.value = true
  showSearch.value = true
  searchError.value = '' // 修复：搜索失败不再静默误报"未找到"
  try {
    const res = await browseSearch(q)
    searchResults.value = res.items
    searchTotal.value = res.total
  } catch (e: any) {
    searchResults.value = []
    searchTotal.value = 0
    searchError.value = e.message || '搜索失败，请重试'
  } finally {
    searching.value = false
  }
}

function clearSearch() {
  searchQuery.value = ''
  showSearch.value = false
}

// ──────── 打开文件 ────────

const openError = ref('')
const openingPath = ref('')

// 文件详情弹窗（浏览条目即含全部可展示元数据）
const detailEntry = ref<BrowseEntry | null>(null)
const detailVersions = ref<Array<{ hash: string; time: number; message: string; author: string }>>([])
const detailLoadingHistory = ref(false)
const detailDownloading = ref<string | null>(null)
const detailError = ref('')

async function openFile(relPath: string) {
  openingPath.value = relPath
  openError.value = ''
  try {
    await browseOpen(relPath)
  } catch (e: any) {
    openError.value = e.message || '打开文件失败'
  } finally {
    openingPath.value = ''
  }
}

async function openDetailModal(e: BrowseEntry) {
  detailEntry.value = e
  detailVersions.value = []
  detailError.value = ''
  detailLoadingHistory.value = true
  try {
    const fileId = await resolveFileId(e.relPath)
    const history = await getFileHistory(fileId)
    detailVersions.value = history.commits
      .map((c: any) => ({
        hash: c.hash,
        time: c.time,
        message: c.message,
        author: c.author,
      }))
      .slice(0, 30)
  } catch {
    // 文件未索引则无版本历史，静默
  } finally {
    detailLoadingHistory.value = false
  }
}

async function downloadVersion(version: { hash: string; time: number }) {
  if (!detailEntry.value) return
  detailDownloading.value = version.hash
  try {
    const blob = await downloadHistoryVersion(detailEntry.value.relPath, version.hash)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    // 下载文件名带版本哈希前缀，与当前版本区分（修复 L-7）
    const extIdx = detailEntry.value.name.lastIndexOf('.')
    const base = extIdx > 0 ? detailEntry.value.name.slice(0, extIdx) : detailEntry.value.name
    const ext = extIdx > 0 ? detailEntry.value.name.slice(extIdx) : ''
    a.download = `${base}-${version.hash.slice(0, 7)}${ext}`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  } catch (e: any) {
    detailError.value = e.message || '下载失败'
  } finally {
    detailDownloading.value = null
  }
}

function openFromDetail() {
  if (detailEntry.value) {
    const rel = detailEntry.value.relPath
    detailEntry.value = null
    openFile(rel)
  }
}

// ──────── 工具 ────────

function formatSize(bytes?: number) {
  if (bytes === undefined || bytes === null) return ''
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function formatTime(ms?: number) {
  if (!ms) return ''
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function fileTypeIcon(name: string): IconName {
  const ext = name.split('.').pop()?.toLowerCase() || ''
  const map: Record<string, IconName> = {
    jpg: 'file-image', jpeg: 'file-image', png: 'file-image', gif: 'file-image',
    webp: 'file-image', svg: 'file-image', bmp: 'file-image', ico: 'file-image', heic: 'file-image',
    pdf: 'file-pdf',
    doc: 'file-word', docx: 'file-word', rtf: 'file-word', odt: 'file-word',
    xls: 'file-excel', xlsx: 'file-excel', xlsm: 'file-excel', csv: 'file-excel', ods: 'file-excel',
    ppt: 'file-ppt', pptx: 'file-ppt', key: 'file-ppt', odp: 'file-ppt',
    zip: 'file-archive', rar: 'file-archive', '7z': 'file-archive', tar: 'file-archive',
    gz: 'file-archive', bz2: 'file-archive', xz: 'file-archive',
    js: 'file-code', ts: 'file-code', jsx: 'file-code', tsx: 'file-code', vue: 'file-code',
    html: 'file-code', htm: 'file-code', css: 'file-code', scss: 'file-code', less: 'file-code',
    py: 'file-code', go: 'file-code', java: 'file-code', c: 'file-code', cpp: 'file-code',
    h: 'file-code', hpp: 'file-code', cs: 'file-code', rs: 'file-code', rb: 'file-code',
    php: 'file-code', sh: 'file-code', bat: 'file-code', ps1: 'file-code', json: 'file-code',
    yml: 'file-code', yaml: 'file-code', xml: 'file-code', sql: 'file-code',
    mp3: 'file-audio', wav: 'file-audio', flac: 'file-audio', aac: 'file-audio',
    ogg: 'file-audio', m4a: 'file-audio', wma: 'file-audio',
    mp4: 'file-video', mkv: 'file-video', avi: 'file-video', mov: 'file-video',
    webm: 'file-video', wmv: 'file-video', flv: 'file-video', m4v: 'file-video',
    txt: 'file-text', md: 'file-text', log: 'file-text', ini: 'file-text',
    cfg: 'file-text', conf: 'file-text', json5: 'file-text',
  }
  return map[ext] || 'file'
}

// 文件类型主题色（Office 风格：xlsx 绿、docx 蓝、pptx 橙……）
function fileColor(name: string): string {
  const ext = name.split('.').pop()?.toLowerCase() || ''
  const map: Record<string, string> = {
    // Excel 绿
    xls: '#1aa169', xlsx: '#1aa169', xlsm: '#1aa169', csv: '#1aa169', ods: '#1aa169',
    // Word 蓝
    doc: '#2b7cd3', docx: '#2b7cd3', rtf: '#2b7cd3', odt: '#2b7cd3',
    // PPT 橙
    ppt: '#e06c1a', pptx: '#e06c1a', key: '#e06c1a', odp: '#e06c1a',
    // PDF 红
    pdf: '#e04848',
    // 图片 紫
    jpg: '#9b59f0', jpeg: '#9b59f0', png: '#9b59f0', gif: '#9b59f0', webp: '#9b59f0',
    svg: '#9b59f0', bmp: '#9b59f0', ico: '#9b59f0', heic: '#9b59f0',
    // 压缩包 琥珀
    zip: '#d49a2a', rar: '#d49a2a', '7z': '#d49a2a', tar: '#d49a2a', gz: '#d49a2a',
    bz2: '#d49a2a', xz: '#d49a2a',
    // 代码 靛蓝
    js: '#3b7bf7', ts: '#3b7bf7', jsx: '#3b7bf7', tsx: '#3b7bf7', vue: '#3b7bf7',
    html: '#3b7bf7', htm: '#3b7bf7', css: '#3b7bf7', scss: '#3b7bf7', less: '#3b7bf7',
    py: '#3b7bf7', go: '#3b7bf7', java: '#3b7bf7', c: '#3b7bf7', cpp: '#3b7bf7',
    h: '#3b7bf7', hpp: '#3b7bf7', cs: '#3b7bf7', rs: '#3b7bf7', rb: '#3b7bf7',
    php: '#3b7bf7', sh: '#3b7bf7', bat: '#3b7bf7', ps1: '#3b7bf7', json: '#3b7bf7',
    yml: '#3b7bf7', yaml: '#3b7bf7', xml: '#3b7bf7', sql: '#3b7bf7',
    // 音频 青
    mp3: '#14b8a6', wav: '#14b8a6', flac: '#14b8a6', aac: '#14b8a6', ogg: '#14b8a6',
    m4a: '#14b8a6', wma: '#14b8a6',
    // 视频 玫红
    mp4: '#ec4899', mkv: '#ec4899', avi: '#ec4899', mov: '#ec4899', webm: '#ec4899',
    wmv: '#ec4899', flv: '#ec4899', m4v: '#ec4899',
    // 文本 灰蓝
    txt: '#64718a', md: '#64718a', log: '#64718a', ini: '#64718a', cfg: '#64718a',
    conf: '#64718a', json5: '#64718a',
  }
  return map[ext] || '#7a8599'
}

function docIcon(e: { isDir: boolean; name: string }): IconName {
  if (e.isDir) return 'folder'
  return fileTypeIcon(e.name)
}

// 文件图标/文件夹颜色
function iconColor(e: { isDir: boolean; name: string }): string {
  if (e.isDir) return '#e6a729'
  return fileColor(e.name)
}

// 文件扩展名（类型列展示，如 .xlsx、.png）
function fileExt(name: string): string {
  const i = name.lastIndexOf('.')
  if (i <= 0) return ''
  return name.slice(i).toLowerCase()
}

// 索引状态文案与样式
function statusLabel(e: BrowseEntry): string {
  if (e.isDir) return ''
  if (!e.indexable) return '不支持'
  const map: Record<string, string> = {
    indexed: '已索引',
    pending: '待索引',
    indexing: '索引中',
    failed: '失败',
    ignored: '已忽略',
  }
  return map[e.indexStatus || ''] || '待索引'
}

function statusClass(e: BrowseEntry): string {
  if (e.isDir) return ''
  if (!e.indexable) return 'idx--unsupported'
  const map: Record<string, string> = {
    indexed: 'idx--ok',
    indexing: 'idx--busy',
    failed: 'idx--err',
    pending: 'idx--pending',
  }
  return map[e.indexStatus || ''] || 'idx--pending'
}
</script>

<template>
  <div class="all-files-page">
    <div class="page-header">
      <div>
        <h2>全部文件</h2>
        <p v-if="ws.info" class="page-sub">{{ ws.info.workspacePath || '（未设置工作区）' }}</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-ghost btn-sm" @click="refreshDir(currentPath)">
          <Icon name="refresh" :size="14" />
          刷新
        </button>
        <button class="btn btn-ghost btn-sm" @click="router.push('/settings')">
          <Icon name="settings" :size="14" />
          设置
        </button>
      </div>
    </div>

    <div v-if="ws.info && !ws.initialized" class="init-banner card">
      <div class="init-banner__content">
        <strong>工作区尚未初始化</strong>
        <span class="init-banner-desc">请先在「设置」中配置工作区与模型端点。</span>
      </div>
      <button class="btn btn-primary btn-sm" @click="router.push('/settings')">去设置</button>
    </div>

    <template v-if="ws.initialized">
      <!-- 文件名搜索 -->
      <div class="search-bar">
        <div class="search-input-wrap">
          <Icon name="search" :size="15" class="search-input-icon" />
          <input
            v-model="searchQuery"
            class="input search-input"
            placeholder="按文件名 / 路径搜索全部文件…（实时扫描磁盘，不依赖索引）"
            @keyup.enter="doSearch"
          />
        </div>
        <button class="btn btn-primary btn-sm" @click="doSearch" :disabled="searching || !searchQuery.trim()">
          {{ searching ? '搜索中…' : '搜索' }}
        </button>
        <button v-if="showSearch" class="btn btn-ghost btn-sm" @click="clearSearch">
          <Icon name="arrow-left" :size="14" />
          返回目录
        </button>
      </div>

      <div v-if="openError" class="alert alert--error">{{ openError }}</div>
      <div v-if="browseError" class="alert alert--error">{{ browseError }}</div>

      <!-- 搜索结果 -->
      <div v-if="showSearch" class="search-results">
        <div v-if="searching" class="loading">加载中…</div>
        <div v-else-if="searchError" class="alert alert--error">{{ searchError }}</div>
        <div v-else-if="searchResults.length === 0" class="empty-state">
          未找到匹配「{{ searchQuery }}」的文件
        </div>
        <div v-else>
          <div v-for="r in searchResults" :key="r.relPath" class="search-item card">
            <span class="search-item-icon">
              <Icon :name="r.isDir ? 'folder' : 'file'" :size="17" />
            </span>
            <span class="search-item-path">{{ r.relPath }}</span>
            <span v-if="!r.isDir" class="search-item-size">{{ formatSize(r.size) }}</span>
            <button
              v-if="!r.isDir"
              class="btn btn-ghost btn-sm"
              @click.stop="openFile(r.relPath)"
              :disabled="openingPath === r.relPath"
            >
              {{ openingPath === r.relPath ? '打开中…' : '打开' }}
            </button>
            <button
              v-else
              class="btn btn-ghost btn-sm"
              @click.stop="refreshDir(r.relPath); clearSearch()"
            >
              进入
            </button>
          </div>
          <div v-if="searchResults.length > 0 && searchTotal > searchResults.length" class="pagination-note">
            共 {{ searchTotal }} 条命中，仅显示前 {{ searchResults.length }} 条
          </div>
        </div>
      </div>

      <!-- 文件列表：显示工作区全部文件 -->
      <div v-else class="file-browser">
        <nav class="breadcrumb">
          <span v-if="currentPath" class="crumb" @click="goUp">
            <Icon name="arrow-left" :size="13" />
            上一级
          </span>
          <span
            v-for="(c, i) in crumbs"
            :key="c.path"
            class="crumb"
            :class="{ 'crumb--current': i === crumbs.length - 1 }"
            @click="i < crumbs.length - 1 && refreshDir(c.path)"
          >
            <template v-if="i > 0"><Icon name="chevron-right" :size="12" /></template>
            {{ c.label }}
          </span>
        </nav>

        <div class="file-list">
          <div class="file-list-head">
            <span>名称</span>
            <span>类型</span>
            <span>大小</span>
            <span>修改时间</span>
            <span>索引状态</span>
            <span>操作</span>
          </div>

          <div v-if="listing" class="loading">加载中…</div>
          <div v-else-if="entries.length === 0" class="empty-state">此目录为空</div>
          <template v-else>
            <div
              v-for="e in entries"
              :key="e.relPath"
              class="file-row"
              :class="{ 'file-row--dir': e.isDir, 'file-row--highlight': e.relPath === highlightPath }"
              @click="e.isDir && refreshDir(e.relPath)"
            >
              <span class="file-row-name" :title="e.relPath">
                <Icon :name="docIcon(e)" :size="16" :color="iconColor(e)" class="file-row-icon" />
                {{ e.name }}
              </span>
              <span class="file-row-cell">
                <span v-if="!e.isDir" class="doc-badge">{{ fileExt(e.name) }}</span>
              </span>
              <span class="file-row-cell file-row-size">{{ e.isDir ? '' : formatSize(e.size) }}</span>
              <span class="file-row-cell file-row-time">{{ formatTime(e.mtime) }}</span>
              <span class="file-row-cell">
                <span v-if="!e.isDir" class="idx-badge" :class="statusClass(e)">{{ statusLabel(e) }}</span>
              </span>
              <span class="file-row-cell file-row-actions">
                <button
                  v-if="!e.isDir"
                  class="btn btn-ghost btn-mini"
                  @click.stop="openFile(e.relPath)"
                  :disabled="openingPath === e.relPath"
                >
                  {{ openingPath === e.relPath ? '打开中…' : '打开' }}
                </button>
                <button v-if="!e.isDir" class="btn btn-ghost btn-mini" @click.stop="openDetailModal(e)">详情</button>
              </span>
            </div>
          </template>
        </div>

        <p class="file-hint">全部文件均显示；仅受支持的文档类型（PDF / DOCX / TXT / MD）会建立索引。</p>
      </div>
    </template>

    <div v-else-if="!ws.info" class="loading">加载工作区信息…</div>
    <div v-else class="empty-state">请先初始化工作区以浏览文件</div>

    <!-- 文件详情弹窗 -->
    <div v-if="detailEntry" class="modal-overlay" @click.self="detailEntry = null">
      <div class="modal modal--detail">
        <div class="modal-title">
          <Icon :name="docIcon(detailEntry)" :size="16" :color="iconColor(detailEntry)" />
          <span class="modal-title__name" :title="detailEntry.name">{{ detailEntry.name }}</span>
        </div>

        <!-- 版本历史 -->
        <div class="detail-section">
          <div class="detail-section__head">
            <span class="detail-section__title">版本历史</span>
            <span class="detail-section__hint">每次自动提交即一个版本</span>
          </div>
          <div v-if="detailLoadingHistory" class="loading">加载历史中…</div>
          <div v-else-if="detailVersions.length === 0" class="detail-empty">暂无版本历史（该文件未发生过自动提交）</div>
          <div v-else class="version-list">
            <div
              v-for="(v, i) in detailVersions"
              :key="v.hash"
              class="version-item"
            >
              <span class="version-num">v{{ detailVersions.length - i }}</span>
              <div class="version-info">
                <div class="version-message" :title="v.message">{{ v.message }}</div>
                <div class="version-meta">
                  <span>{{ formatTime(v.time) }}</span>
                  <span class="version-hash" :title="v.hash">{{ v.hash.slice(0, 7) }}</span>
                </div>
              </div>
              <button
                class="btn btn-ghost btn-sm"
                @click="downloadVersion(v)"
                :disabled="detailDownloading === v.hash"
              >
                <Icon name="download" :size="13" />
                {{ detailDownloading === v.hash ? '下载中…' : '下载' }}
              </button>
            </div>
          </div>
        </div>

        <div v-if="detailError" class="alert alert--error">{{ detailError }}</div>

        <div class="modal-actions">
          <button class="btn btn-ghost btn-sm" @click="detailEntry = null">关闭</button>
          <button class="btn btn-primary btn-sm" @click="openFromDetail">打开当前版本</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.all-files-page {
  padding: 20px 24px 28px;
  overflow-y: auto;
  height: 100%;
}

.page-sub {
  font-size: 12px;
  color: var(--c-text-tertiary);
  margin: 2px 0 0;
  max-width: 480px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.init-banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
  background: var(--c-info-soft);
  border-color: transparent;
  font-size: 14px;
  color: var(--c-info);
}

.init-banner__content {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.init-banner-desc {
  color: var(--c-text-secondary);
  font-size: 13px;
}

.search-bar {
  display: flex;
  gap: 8px;
  margin-bottom: 14px;
}

.search-input-wrap {
  position: relative;
  flex: 1;
}

.search-input-icon {
  position: absolute;
  left: 12px;
  top: 50%;
  transform: translateY(-50%);
  color: var(--c-text-tertiary);
  pointer-events: none;
}

.search-input {
  padding-left: 34px;
}

.search-results {
  margin-bottom: 16px;
}

.search-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  margin-bottom: 8px;
  font-size: 13px;
}

.search-item:hover {
  border-color: var(--c-border-strong);
}

.search-item-icon {
  color: var(--c-icon-secondary);
  flex-shrink: 0;
}

.search-item-path {
  flex: 1;
  word-break: break-all;
  color: var(--c-text-primary);
}

.search-item-size {
  color: var(--c-text-tertiary);
  font-size: 12px;
}

/* 文件列表 */
.file-browser {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.breadcrumb {
  display: flex;
  align-items: center;
  gap: 2px;
  font-size: 13px;
  flex-wrap: wrap;
  color: var(--c-info);
  min-height: 24px;
}

.crumb {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  cursor: pointer;
  padding: 2px 6px;
  border-radius: var(--r-xs);
  color: var(--c-info);
  transition: background 0.14s;
}

.crumb:hover {
  background: var(--c-info-soft);
  text-decoration: none;
}

.crumb--current {
  color: var(--c-text-primary);
  font-weight: 600;
  cursor: default;
}

.crumb--current:hover {
  background: transparent;
}

.file-list {
  border: 1px solid var(--c-border);
  border-radius: var(--r-lg);
  background: var(--c-bg-panel);
  overflow: hidden;
}

.file-list-head,
.file-row {
  display: grid;
  grid-template-columns: minmax(200px, 1fr) 72px 84px 150px 96px auto;
  align-items: center;
  gap: 10px;
  padding: 9px 14px;
  font-size: 13px;
}

.file-list-head {
  font-weight: 600;
  color: var(--c-text-tertiary);
  background: var(--c-bg-secondary);
  border-bottom: 1px solid var(--c-border);
  font-size: 12px;
  letter-spacing: 0.02em;
}

/* 表头与数据列对齐：首项偏移匹配文件名前的图标宽度+间距，右值列右对齐 */
.file-list-head span:first-child {
  padding-left: 24px; /* 16px icon + 8px gap */
}
.file-list-head span:nth-child(3),
.file-list-head span:nth-child(4),
.file-list-head span:nth-child(6) {
  text-align: right;
}

.file-row {
  border-bottom: 1px solid var(--c-border);
  transition: background 0.12s ease;
}

.file-row:last-child {
  border-bottom: none;
}

.file-row:hover {
  background: var(--c-bg-hover);
}

.file-row--dir {
  cursor: pointer;
}
.file-row--highlight {
  background: var(--c-brand-soft) !important;
  border-left: 3px solid var(--c-brand);
  border-radius: 0;
}
.file-row--highlight .file-row-name {
  color: var(--c-brand);
}

.file-row-name {
  display: flex;
  align-items: center;
  gap: 8px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-primary);
  font-weight: 500;
}

.file-row-icon {
  color: var(--c-icon-secondary);
  flex-shrink: 0;
}

.file-row-cell {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.file-row-size,
.file-row-time {
  color: var(--c-text-tertiary);
  font-size: 12px;
  text-align: right;
}

.file-row-time {
  white-space: nowrap;
}

.file-row-actions {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
}

.doc-badge {
  display: inline-block;
  padding: 1px 8px;
  font-size: 11px;
  font-weight: 600;
  border-radius: var(--r-full);
  background: var(--c-bg-elevated);
  color: var(--c-text-secondary);
  letter-spacing: 0.03em;
  text-transform: uppercase;
}

.idx-badge {
  display: inline-block;
  padding: 1px 8px;
  font-size: 11px;
  font-weight: 600;
  border-radius: var(--r-full);
  white-space: nowrap;
}

.idx--ok {
  background: var(--c-success-soft);
  color: var(--c-success);
}

.idx--busy {
  background: var(--c-info-soft);
  color: var(--c-info);
}

.idx--err {
  background: var(--c-danger-soft, rgba(224, 108, 117, 0.14));
  color: var(--c-danger);
}

.idx--pending {
  background: var(--c-bg-elevated);
  color: var(--c-text-secondary);
}

.idx--unsupported {
  background: transparent;
  color: var(--c-text-tertiary);
  font-weight: 400;
}

.file-hint {
  font-size: 12px;
  color: var(--c-text-tertiary);
  margin: 4px 2px 0;
}

/* ──────── 文件详情弹窗 ──────── */

.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}

.modal {
  background: var(--c-bg-panel);
  border: 1px solid var(--c-border);
  border-radius: var(--r-lg);
  padding: 20px;
  width: 480px;
  max-width: 90vw;
  max-height: 80vh;
  overflow-y: auto;
  box-shadow: var(--shadow-pop);
}

.modal-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 16px;
}

.modal-title__name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.detail-grid {
  display: flex;
  flex-direction: column;
  gap: 10px;
  font-size: 13px;
}

.detail-section {
  margin-bottom: 14px;
  font-size: 13px;
}

.detail-section__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--c-border);
}

.detail-section__title {
  font-weight: 600;
  font-size: 14px;
}

.detail-section__hint {
  font-size: 11px;
  color: var(--c-text-tertiary);
}

.detail-empty {
  font-size: 12px;
  color: var(--c-text-tertiary);
  padding: 16px 0;
  text-align: center;
}

.version-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 360px;
  overflow-y: auto;
}

.version-item {
  display: grid;
  grid-template-columns: auto 1fr auto;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid var(--c-border);
  border-radius: var(--r-md);
  background: var(--c-bg-secondary);
}

.version-num {
  font-size: 11px;
  font-weight: 700;
  color: var(--c-brand);
  background: var(--c-brand-soft, rgba(0, 0, 0, 0.04));
  padding: 2px 6px;
  border-radius: var(--r-xs);
}

.version-info {
  min-width: 0;
}

.version-message {
  font-size: 12px;
  color: var(--c-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.version-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  color: var(--c-text-tertiary);
  margin-top: 2px;
}

.version-hash {
  font-family: monospace;
  color: var(--c-text-secondary);
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 14px;
}
</style>