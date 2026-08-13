<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useSettingsStore } from '@/stores/settings'
import { getRecentFiles, getFile, browseOpen, resolveFileId, searchFiles, browseSearch } from '@/api/client'
import type { FileItem } from '@/types'
import Icon from '@/components/Icon.vue'
import FileHistoryDialog from '@/components/FileHistoryDialog.vue'
import { statusLabel, statusClass, isAbnormal } from '@/utils/status'

const router = useRouter()
const route = useRoute()
const ws = useWorkspaceStore()
const settings = useSettingsStore()

const files = ref<FileItem[]>([])
const loading = ref(false)
const loadError = ref('')
const openingPath = ref('')
const openError = ref('')

// ── 统一搜索（S3：一个框同时搜文件名 + 文档内容）──
const searchQuery = ref('')
const searching = ref(false)
const hasSearched = ref(false)
const searchError = ref('')
const searchPartialWarn = ref('') // 单路搜索失败时保留另一路结果并标记（P2-12）
interface SearchHit {
  relPath: string
  kind: 'file' | 'content' // file = 文件名命中；content = 内容命中
  mtime?: number
  size?: number
  docType?: string
  hitText?: string
  tags?: FileItem['tags']
}
const searchHits = ref<SearchHit[]>([])

// ── 按文件过滤（聊天里点击文件链接跳转：?highlight=相对路径）──
const highlightRel = ref('') // 当前过滤的文件相对路径（空 = 正常列表）
const highlightedFile = ref<FileItem | null>(null)
const highlightLoading = ref(false)
const highlightError = ref('')

// 时间窗（小时）：0 = 全部
const windowOptions = [
  { label: '最近 5 小时', value: 5 },
  { label: '最近 24 小时', value: 24 },
  { label: '最近 7 天', value: 168 },
  { label: '全部', value: 0 },
]
const windowHours = ref(24)
let refreshTimer: ReturnType<typeof setInterval> | null = null

onMounted(async () => {
  await ws.fetchInfo()
  if (ws.initialized) {
    // 从设置加载时间窗
    if (settings.settings.recent?.windowHours !== undefined) {
      windowHours.value = settings.settings.recent.windowHours
    } else {
      await settings.fetch()
      windowHours.value = settings.settings.recent?.windowHours ?? 24
    }
    const hl = typeof route.query.highlight === 'string' ? route.query.highlight : ''
    if (hl) {
      await applyHighlight(hl)
    } else {
      await loadRecent()
    }
    // 定期自动刷新（每 60 秒）
    refreshTimer = setInterval(loadRecent, 60000)
  }
})

onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
  if (searchTimer) clearTimeout(searchTimer)
})

// 从聊天链接跳转过来时切换过滤
watch(() => route.query.highlight, async (val) => {
  const hl = typeof val === 'string' ? val : ''
  await applyHighlight(hl)
})

// 时间窗变化时同步到设置并重载
watch(windowHours, async () => {
  await settings.save({ 'recent.windowHours': windowHours.value })
  await loadRecent()
})

async function loadRecent() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await getRecentFiles({ window: windowHours.value, limit: 50 })
    files.value = res.items ?? []
  } catch (e: any) {
    loadError.value = e.message || '加载最近文件失败'
    files.value = []
  } finally {
    loading.value = false
  }
}

// 输入防抖：停顿 400ms 自动搜索
let searchTimer: ReturnType<typeof setTimeout> | null = null
function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    if (searchQuery.value.trim()) {
      doSearch()
    } else {
      hasSearched.value = false
      searchHits.value = []
      loadRecent()
    }
  }, 400)
}
function clearSearch() {
  searchQuery.value = ''
  hasSearched.value = false
  searchHits.value = []
  searchError.value = ''
  loadRecent()
}

// 统一搜索：文件名 + 文档内容一起找
// P2-12：用 allSettled 收敛"一路失败拖垮全部"——语义搜索与文件名搜索各自独立展示，
// 一路失败时保留另一路的命中，并用醒目横幅标记失败的一路；两路全失败才报整体错误。
async function doSearch() {
  const q = searchQuery.value.trim()
  if (!q) {
    hasSearched.value = false
    searchHits.value = []
    searchError.value = ''
    searchPartialWarn.value = ''
    await loadRecent()
    return
  }
  searching.value = true
  searchError.value = ''
  searchPartialWarn.value = ''
  hasSearched.value = true
  const [contentRes, namesRes] = await Promise.allSettled([
    searchFiles({ q }),
    browseSearch(q, 50),
  ])
  const hits: SearchHit[] = []
  const seen = new Set<string>()
  const failed: string[] = []
  if (namesRes.status === 'fulfilled') {
    for (const n of namesRes.value.items) {
      if (n.isDir) continue
      if (seen.has(n.relPath)) continue
      seen.add(n.relPath)
      hits.push({
        relPath: n.relPath,
        kind: 'file',
        mtime: n.mtime,
        size: n.size,
        docType: n.docType,
      })
    }
  } else {
    failed.push('文件名搜索')
  }
  if (contentRes.status === 'fulfilled') {
    for (const r of contentRes.value.items) {
      if (seen.has(r.relPath)) continue
      seen.add(r.relPath)
      hits.push({
        relPath: r.relPath,
        kind: 'content',
        mtime: r.mtime,
        hitText: r.hitText,
        tags: r.tags,
      })
    }
  } else {
    failed.push('内容搜索')
  }
  searchHits.value = hits
  searching.value = false
  if (failed.length === 2) {
    searchHits.value = []
    searchError.value = '搜索失败，请重试'
  } else if (failed.length === 1) {
    searchPartialWarn.value = `${failed[0]}失败，以下仅展示另一部分结果`
  }
}

// 应用"仅显示该文件"：直接按路径取单个文件（可能不在最近时间窗内）
async function applyHighlight(relPath: string) {
  highlightRel.value = relPath
  if (!relPath) {
    highlightedFile.value = null
    highlightError.value = ''
    await loadRecent()
    return
  }
  highlightLoading.value = true
  highlightError.value = ''
  highlightedFile.value = null
  try {
    const fileId = await resolveFileId(relPath)
    highlightedFile.value = (await getFile(fileId)) ?? null
  } catch (e: any) {
    highlightedFile.value = null
    highlightError.value = e?.message || '无法定位该文件'
  } finally {
    highlightLoading.value = false
  }
}

function clearHighlight() {
  router.replace({ path: '/files' })
}

// 显示列表：搜索态显示搜索结果，过滤模式只显示目标文件，否则显示最近文件
const displayItems = computed<FileItem[]>(() => {
  if (highlightRel.value) {
    return highlightedFile.value ? [highlightedFile.value] : []
  }
  return files.value
})

function windowLabel(h: number): string {
  const opt = windowOptions.find(o => o.value === h)
  return opt ? opt.label : h > 0 ? `最近 ${h} 小时` : '全部'
}

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

// 文件详情弹窗（版本历史 + 下载 + 一键恢复）由共享 FileHistoryDialog 承担
const detailOpen = ref(false)
const detailFile = ref<FileItem | null>(null)

function openDetailModal(f: FileItem) {
  detailFile.value = f
  detailOpen.value = true
}

// 弹窗内「打开当前版本」→ 关闭弹窗并调起系统打开
function onNavigateFile(relPath: string) {
  detailOpen.value = false
  openFile(relPath)
}

function formatSize(bytes?: number) {
  if (bytes === undefined || bytes === null) return ''
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function formatTime(ms?: number) {
  if (!ms) return '—'
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}


</script>

<template>
  <div class="recent-page">
    <div class="page-header">
      <div>
        <h2>最近文件</h2>
        <p class="page-sub">{{ hasSearched ? '同时搜索文件名和文档内容' : `${windowLabel(windowHours)}内修改的文件` }}</p>
      </div>
      <div class="header-actions">
        <div class="unified-search">
          <Icon name="search" :size="14" class="unified-search__icon" />
          <input
            v-model="searchQuery"
            class="input unified-search__input"
            placeholder="搜索文件名或内容…"
            @input="onSearchInput"
            @keyup.enter="doSearch"
          />
          <button v-if="searchQuery" class="unified-search__clear" title="清空" @click="clearSearch">
            <Icon name="x" :size="12" />
          </button>
        </div>
        <select v-if="!hasSearched" v-model.number="windowHours" class="select window-select" title="时间窗">
          <option v-for="o in windowOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
        <button class="btn btn-ghost btn-sm" @click="hasSearched ? doSearch() : loadRecent()" :disabled="loading || searching">
          <Icon name="refresh" :size="14" />
          {{ loading || searching ? '加载中…' : hasSearched ? '重搜' : '刷新' }}
        </button>
      </div>
    </div>

    <div v-if="ws.info && !ws.initialized" class="init-banner card">
      <div class="init-banner__content">
        <strong>还没有选择要管理的文件夹</strong>
        <span class="init-banner-desc">选好文件夹、连接 AI 后即可使用全部功能。</span>
      </div>
      <button class="btn btn-primary btn-sm" @click="router.push('/settings')">去设置</button>
    </div>

    <template v-if="ws.initialized">
      <div v-if="openError" class="alert alert--error">{{ openError }}</div>
      <div v-if="loadError" class="alert alert--error">{{ loadError }}</div>

      <!-- 按文件过滤横幅（聊天里点击文件链接进入） -->
      <div v-if="highlightRel" class="filter-banner">
        <Icon name="file" :size="14" class="filter-banner__icon" />
        <span class="filter-banner__label">仅显示该文件：</span>
        <span class="filter-banner__path" :title="highlightRel">{{ highlightRel }}</span>
        <button class="btn btn-ghost btn-mini filter-banner__clear" @click="clearHighlight">
          <Icon name="x" :size="12" />
          清除
        </button>
      </div>

      <div v-if="hasSearched">
        <div v-if="searchPartialWarn" class="alert alert--warning">{{ searchPartialWarn }}</div>
        <div v-if="searching" class="loading">搜索中…</div>
        <div v-else-if="searchError" class="alert alert--error">{{ searchError }}</div>
        <div v-else-if="searchHits.length === 0" class="empty-state empty-state--icon">
          <span class="empty-state__icon"><Icon name="search" :size="20" /></span>
          <span class="empty-state__title">没有找到「{{ searchQuery }}」</span>
          <span class="empty-state__desc">换个关键词试试，搜索会同时匹配文件名和文档内容</span>
        </div>
        <div v-else class="file-list file-list--panel">
          <div class="file-list-head list-head">
            <span class="file-col-name">文件</span>
            <span>匹配</span>
            <span class="file-col-right">大小</span>
            <span class="file-col-right">修改时间</span>
            <span class="file-col-right">操作</span>
          </div>
          <div class="file-rows">
            <div v-for="(h, i) in searchHits" :key="i" class="file-row">
              <span class="file-row-cell file-row-name-cell">
                <span class="file-row-name" :title="h.relPath">
                  <Icon name="file" :size="14" class="file-row-icon" />
                  <span class="file-row-name__text">{{ h.relPath }}</span>
                </span>
                <span v-if="h.tags?.length" class="file-row-tags">
                  <span v-for="t in h.tags" :key="t.name" class="tag-chip tag-chip--mini">{{ t.name }}</span>
                </span>
              </span>
              <span class="file-row-cell">
                <span v-if="h.kind === 'content'" class="search-match">
                  <Icon name="chat" :size="12" />
                  {{ h.hitText || '内容命中' }}
                </span>
                <span v-else class="search-match search-match--file">
                  <Icon name="folder" :size="12" />
                  文件名命中
                </span>
              </span>
              <span class="file-row-cell file-row-size">{{ formatSize(h.size) }}</span>
              <span class="file-row-cell file-row-time">{{ formatTime(h.mtime) }}</span>
              <span class="file-row-cell file-row-actions">
                <button class="btn btn-ghost btn-mini" @click.stop="openFile(h.relPath)" :disabled="openingPath === h.relPath">
                  {{ openingPath === h.relPath ? '打开中…' : '打开' }}
                </button>
              </span>
            </div>
          </div>
        </div>
      </div>

      <template v-else>
      <div v-if="highlightRel ? highlightLoading : loading" class="loading">加载中…</div>
      <div v-else-if="highlightRel && highlightError" class="alert alert--error">{{ highlightError }}</div>
      <div v-else-if="displayItems.length === 0 && !loadError" class="empty-state empty-state--icon">
        <span class="empty-state__icon"><Icon name="clock" :size="20" /></span>
        <span class="empty-state__title">{{ highlightRel ? '无法显示该文件' : `${windowLabel(windowHours)}内有修改的文件` }}</span>
        <span class="empty-state__desc">{{ highlightRel ? '文件可能已删除或未索引' : '尚无文件，文档修改后会自动出现在这里' }}</span>
      </div>
      <div v-else class="file-list file-list--panel">
        <div class="file-list-head list-head">
          <span class="file-col-name">文件</span>
          <span>类型</span>
          <span class="file-col-right">大小</span>
          <span>状态</span>
          <span class="file-col-right">修改时间</span>
          <span class="file-col-right">操作</span>
        </div>
        <div class="file-rows">
          <div v-for="f in displayItems" :key="f.id" class="file-row" :class="{ 'file-row--highlight': highlightRel }">
            <span class="file-row-cell file-row-name-cell">
              <span class="file-row-name" :title="f.relPath">
                <Icon name="file" :size="14" class="file-row-icon" />
                <span class="file-row-name__text">{{ f.relPath }}</span>
              </span>
              <span v-if="f.tags?.length" class="file-row-tags">
                <span v-for="t in f.tags" :key="t.name" class="tag-chip tag-chip--mini">{{ t.name }}</span>
              </span>
            </span>
            <span class="file-row-cell file-doc">{{ f.docType }}</span>
            <span class="file-row-cell file-row-size">{{ formatSize(f.size) }}</span>
            <span class="file-row-cell">
              <span v-if="isAbnormal(f.indexStatus)" class="status-chip" :class="statusClass(f.indexStatus)">{{ statusLabel(f.indexStatus) }}</span>
              <span v-if="f.lastError" class="file-err" :title="f.lastError">{{ f.lastError }}</span>
            </span>
            <span class="file-row-cell file-row-time">{{ formatTime(f.mtime) }}</span>
            <span class="file-row-cell file-row-actions">
              <button class="btn btn-ghost btn-mini" @click.stop="openFile(f.relPath)" :disabled="openingPath === f.relPath">
                {{ openingPath === f.relPath ? '打开中…' : '打开' }}
              </button>
              <button class="btn btn-ghost btn-mini" @click.stop="openDetailModal(f)">详情</button>
            </span>
          </div>
        </div>
      </div>
      </template>
    </template>
    <div v-else-if="!ws.info" class="loading">加载工作区信息…</div>

    <!-- 文件详情弹窗（共享组件：版本历史 + 下载 + 恢复） -->
    <FileHistoryDialog
      :file="detailFile"
      :open="detailOpen"
      @close="detailOpen = false"
      @navigate-file="onNavigateFile"
    />
  </div>
</template>

<style scoped>
.recent-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  padding: 20px 24px 28px;
  overflow: hidden;
}

.page-sub { font-size: 12px; color: var(--c-text-tertiary); margin: 2px 0 0; }

.window-select { width: auto; padding: 5px 10px; font-size: 12.5px; }

/* 统一搜索框（S3） */
.unified-search {
  position: relative;
  display: flex;
  align-items: center;
}
.unified-search__icon {
  position: absolute;
  left: 9px;
  color: var(--c-text-tertiary);
  pointer-events: none;
}
.unified-search__input {
  width: 220px;
  padding: 5px 28px 5px 30px;
  font-size: 12.5px;
  border-radius: var(--r-md);
}
.unified-search__clear {
  position: absolute;
  right: 7px;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: none;
  background: none;
  color: var(--c-text-tertiary);
  cursor: pointer;
  border-radius: 50%;
}
.unified-search__clear:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}

/* 搜索结果：命中类型徽记 */
.search-match {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 320px;
  padding: 2px 8px;
  border-radius: var(--r-sm);
  background: var(--c-brand-soft);
  color: var(--c-brand);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.search-match--file {
  background: var(--c-bg-hover);
  color: var(--c-text-secondary);
}

/* 按文件过滤横幅 */
.filter-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  margin-bottom: 12px;
  background: var(--c-info-soft);
  border: 1px solid transparent;
  border-radius: var(--r-md);
  font-size: 13px;
  flex-shrink: 0;
}
.filter-banner__icon { color: var(--c-info); flex-shrink: 0; }
.filter-banner__label { color: var(--c-text-secondary); flex-shrink: 0; }
.filter-banner__path {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-primary);
  font-family: var(--font-mono, monospace);
  font-size: 12.5px;
}
.filter-banner__clear { flex-shrink: 0; }

/* 过滤模式下的行：品牌色高亮 */
.file-row--highlight {
  background: var(--c-brand-soft) !important;
  border-left: 3px solid var(--c-brand);
}

.init-banner {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  margin-bottom: 16px; background: var(--c-info-soft); border-color: transparent;
  font-size: 14px; color: var(--c-info); flex-shrink: 0;
}
.init-banner__content { display: flex; flex-direction: column; gap: 2px; }
.init-banner-desc { color: var(--c-text-secondary); font-size: 13px; }

.file-list--panel {
  flex: 1; min-height: 0; display: flex; flex-direction: column; overflow: hidden;
  background: var(--c-bg-panel); border: 1px solid var(--c-border); border-radius: var(--r-lg);
}
.file-list-head { grid-template-columns: minmax(200px, 1fr) 64px 80px 100px 130px auto; }
.file-col-name { padding-left: 22px; }
.file-col-right { text-align: right; }
.file-rows { flex: 1; min-height: 0; overflow-y: auto; }
.file-row {
  display: grid; grid-template-columns: minmax(200px, 1fr) 64px 80px 100px 130px auto;
  align-items: center; gap: 10px; padding: 9px 14px; font-size: 13px;
  border-bottom: 1px solid var(--c-border); transition: background 0.12s ease;
}
.file-row:last-child { border-bottom: none; }
.file-row:hover { background: var(--c-bg-hover); }
.file-row-name { display: flex; align-items: center; gap: 8px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--c-text-primary); font-weight: 500; }
.file-row-name-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  justify-content: center;
  min-width: 0;
}
.file-row-name__text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-row-tags { display: flex; gap: 4px; flex-wrap: wrap; }
.tag-chip--mini { font-size: 10.5px; padding: 1px 6px; }
.file-row-icon { color: var(--c-icon-secondary); flex-shrink: 0; }
.file-row-cell { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-doc { color: var(--c-text-secondary); text-transform: uppercase; font-weight: 600; font-size: 12px; }
.file-row-size { color: var(--c-text-tertiary); font-size: 12px; text-align: right; }
.file-row-time { color: var(--c-text-tertiary); font-size: 12px; text-align: right; }
.file-row-actions { display: flex; gap: 6px; justify-content: flex-end; }
.file-err { display: block; font-size: 11px; color: var(--c-danger); margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>