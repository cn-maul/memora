<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useSettingsStore } from '@/stores/settings'
import { getRecentFiles, getFile, browseOpen, getFileHistory, downloadHistoryVersion, resolveFileId } from '@/api/client'
import type { FileItem } from '@/types'
import Icon from '@/components/Icon.vue'

const router = useRouter()
const route = useRoute()
const ws = useWorkspaceStore()
const settings = useSettingsStore()

const files = ref<FileItem[]>([])
const loading = ref(false)
const loadError = ref('')
const openingPath = ref('')
const openError = ref('')

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

// 显示列表：过滤模式下只显示目标文件，否则显示最近文件
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

// 文件详情弹窗（版本历史 + 下载）
const detailEntry = ref<FileItem | null>(null)
const detailVersions = ref<Array<{ hash: string; time: number; message: string; author: string }>>([])
const detailLoadingHistory = ref(false)
const detailDownloading = ref<string | null>(null)
const detailError = ref('')

function baseName(p: string) {
  const idx = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'))
  return idx >= 0 ? p.slice(idx + 1) : p
}

async function openDetailModal(f: FileItem) {
  detailEntry.value = f
  detailVersions.value = []
  detailError.value = ''
  detailLoadingHistory.value = true
  try {
    const fileId = await resolveFileId(f.relPath)
    const history = await getFileHistory(fileId)
    detailVersions.value = history.commits
      .map((c: any) => ({ hash: c.hash, time: c.time, message: c.message, author: c.author }))
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
    const name = baseName(detailEntry.value.relPath)
    const extIdx = name.lastIndexOf('.')
    const base = extIdx > 0 ? name.slice(0, extIdx) : name
    const ext = extIdx > 0 ? name.slice(extIdx) : ''
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

function statusLabel(s: string) {
  const map: Record<string, string> = {
    pending: '待索引', extracting: '提取中', embedding: '嵌入中',
    indexed: '已索引', failed: '失败', ignored: '已忽略',
  }
  return map[s] || s
}

function statusClass(s: string) {
  const map: Record<string, string> = {
    indexed: 'status-chip--ok', extracting: 'status-chip--busy', embedding: 'status-chip--busy',
    failed: 'status-chip--err', pending: 'status-chip--muted', ignored: 'status-chip--muted',
  }
  return map[s] || 'status-chip--muted'
}
</script>

<template>
  <div class="recent-page">
    <div class="page-header">
      <div>
        <h2>最近文件</h2>
        <p class="page-sub">{{ windowLabel(windowHours) }}内修改的文件</p>
      </div>
      <div class="header-actions">
        <select v-model.number="windowHours" class="select window-select" title="时间窗">
          <option v-for="o in windowOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>
        <button class="btn btn-ghost btn-sm" @click="loadRecent" :disabled="loading">
          <Icon name="refresh" :size="14" />
          {{ loading ? '加载中…' : '刷新' }}
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

      <div v-if="highlightRel ? highlightLoading : loading" class="loading">加载中…</div>
      <div v-else-if="highlightRel && highlightError" class="alert alert--error">{{ highlightError }}</div>
      <div v-else-if="displayItems.length === 0" class="empty-state empty-state--icon">
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
            <span class="file-row-name" :title="f.relPath">
              <Icon name="file" :size="14" class="file-row-icon" />
              {{ f.relPath }}
            </span>
            <span class="file-row-cell file-doc">{{ f.docType }}</span>
            <span class="file-row-cell file-row-size">{{ formatSize(f.size) }}</span>
            <span class="file-row-cell">
              <span class="status-chip" :class="statusClass(f.indexStatus)">{{ statusLabel(f.indexStatus) }}</span>
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
    <div v-else-if="!ws.info" class="loading">加载工作区信息…</div>
    <div v-else class="empty-state">请先初始化工作区</div>

    <!-- 文件详情弹窗 -->
    <div v-if="detailEntry" class="modal-overlay" @click.self="detailEntry = null">
      <div class="modal modal--detail">
        <div class="modal-title">
          <Icon name="file" :size="16" />
          <span class="modal-title__name" :title="detailEntry.relPath">{{ baseName(detailEntry.relPath) }}</span>
        </div>
        <div class="modal-meta">
          <span class="modal-meta__item">
            <Icon name="file" :size="12" />
            {{ detailEntry.docType || '文件' }}
          </span>
          <span class="modal-meta__item">
            <Icon name="file" :size="12" />
            {{ formatSize(detailEntry.size) }}
          </span>
          <span class="modal-meta__item">
            <Icon name="clock" :size="12" />
            {{ formatTime(detailEntry.mtime) }}
          </span>
        </div>

        <div class="detail-section">
          <div class="detail-section__head">
            <span class="detail-section__title">版本历史</span>
            <span class="detail-section__hint">每次自动提交即一个版本</span>
          </div>
          <div v-if="detailLoadingHistory" class="loading">加载历史中…</div>
          <div v-else-if="detailVersions.length === 0" class="detail-empty">暂无版本历史（该文件未发生过自动提交）</div>
          <div v-else class="version-list">
            <div v-for="(v, i) in detailVersions" :key="v.hash" class="version-item">
              <span class="version-num">v{{ detailVersions.length - i }}</span>
              <div class="version-info">
                <div class="version-message" :title="v.message">{{ v.message }}</div>
                <div class="version-meta">
                  <span>{{ formatTime(v.time) }}</span>
                  <span class="version-hash" :title="v.hash">{{ v.hash.slice(0, 7) }}</span>
                </div>
              </div>
              <button class="btn btn-ghost btn-sm" @click="downloadVersion(v)" :disabled="detailDownloading === v.hash">
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
.recent-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  padding: 20px 24px 28px;
  overflow: hidden;
}

.page-sub { font-size: 12px; color: var(--c-text-tertiary); margin: 2px 0 0; }

.window-select { width: auto; padding: 5px 10px; font-size: 12.5px; }

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
.file-row-icon { color: var(--c-icon-secondary); flex-shrink: 0; }
.file-row-cell { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-doc { color: var(--c-text-secondary); text-transform: uppercase; font-weight: 600; font-size: 12px; }
.file-row-size { color: var(--c-text-tertiary); font-size: 12px; text-align: right; }
.file-row-time { color: var(--c-text-tertiary); font-size: 12px; text-align: right; }
.file-row-actions { display: flex; gap: 6px; justify-content: flex-end; }
.file-err { display: block; font-size: 11px; color: var(--c-danger); margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* ──────── 文件详情弹窗 ──────── */
.modal--detail { width: 480px; max-width: 90vw; }

.modal-title { display: flex; align-items: center; gap: 8px; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.modal-title__name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.modal-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 16px; margin-bottom: 16px; font-size: 12px; color: var(--c-text-tertiary); }
.modal-meta__item { display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; }
.modal-meta__item svg { color: var(--c-icon-secondary); }

.detail-section { margin-bottom: 14px; font-size: 13px; }
.detail-section__head { display: flex; align-items: baseline; gap: 10px; margin-bottom: 10px; padding-bottom: 8px; border-bottom: 1px solid var(--c-border); }
.detail-section__title { font-weight: 600; font-size: 14px; }
.detail-section__hint { font-size: 11px; color: var(--c-text-tertiary); }

.detail-empty { font-size: 12px; color: var(--c-text-tertiary); padding: 16px 0; text-align: center; }

.version-list { display: flex; flex-direction: column; gap: 8px; max-height: 360px; overflow-y: auto; }
.version-item { display: grid; grid-template-columns: auto 1fr auto; align-items: center; gap: 10px; padding: 8px 10px; border: 1px solid var(--c-border); border-radius: var(--r-md); background: var(--c-bg-secondary); }
.version-num { font-size: 11px; font-weight: 700; color: var(--c-brand); background: var(--c-brand-soft); padding: 2px 6px; border-radius: var(--r-xs); }
.version-info { min-width: 0; }
.version-message { font-size: 12px; color: var(--c-text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.version-meta { display: flex; align-items: center; gap: 8px; font-size: 11px; color: var(--c-text-tertiary); margin-top: 2px; }
.version-hash { font-family: monospace; color: var(--c-text-secondary); }

.modal-actions { display: flex; justify-content: flex-end; gap: 10px; margin-top: 14px; }
</style>