<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { browseDir, browseSearch, browseOpen } from '@/api/client'
import type { BrowseEntry, BrowseSearchItem } from '@/types'
import TreeBranch from '@/components/TreeBranch.vue'
import Icon, { type IconName } from '@/components/Icon.vue'

const router = useRouter()
const ws = useWorkspaceStore()

// 目录树
interface TreeNode {
  name: string
  relPath: string // 目录以 / 结尾
  expanded: boolean
  loading: boolean
  hasLoaded: boolean
  children: TreeNode[]
}
const root = ref<TreeNode>({
  name: '工作区',
  relPath: '/',
  expanded: true,
  loading: false,
  hasLoaded: false,
  children: [],
})
const currentPath = ref('') // 当前浏览的相对路径（目录，'' 表示根）
const entries = ref<BrowseEntry[]>([])
const listing = ref(false)
const browseError = ref('')

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
    await loadRoot()
  }
})

// 会话中工作区变化后重新加载
watch(() => ws.initialized, async (v) => {
  if (v) await loadRoot()
})

// ──────── 目录树 ────────

async function loadRoot() {
  root.value = { name: '工作区', relPath: '/', expanded: true, loading: false, hasLoaded: false, children: [] }
  currentPath.value = ''
  await Promise.all([refreshDir(''), loadChildren(root.value)])
}

async function refreshDir(subPath: string) {
  listing.value = true
  browseError.value = ''
  try {
    const res = await browseDir(subPath)
    entries.value = res.entries || []
  } catch (e: any) {
    browseError.value = e.message
    entries.value = []
  } finally {
    listing.value = false
  }
}

async function loadChildren(node: TreeNode) {
  if (node.hasLoaded || node.loading) return
  node.loading = true
  try {
    const res = await browseDir(node.relPath === '/' ? '' : node.relPath)
    node.children = (res.entries || [])
      .filter((e) => e.isDir)
      .map((d) => ({
        name: d.name,
        relPath: d.relPath,
        expanded: false,
        loading: false,
        hasLoaded: false,
        children: [],
      }))
    node.hasLoaded = true
  } catch {
    node.children = []
  } finally {
    node.loading = false
  }
}

async function navigateTo(subPath: string) {
  currentPath.value = subPath
  await refreshDir(subPath)
}

// 面包屑
const crumbs = computed(() => {
  const parts = currentPath.value ? currentPath.value.split('/').filter(Boolean) : []
  const result = [{ label: '工作区', path: '' }]
  let acc = ''
  for (const p of parts) {
    acc = acc ? `${acc}/${p}` : p
    result.push({ label: p, path: acc })
  }
  return result
})

// ──────── 搜索（按文件名/路径） ────────

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

// 打开文件（用系统默认应用，修复 H-05）
const openError = ref('')
const openingPath = ref('')

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

// ──────── 工具 ────────

function formatTime(ms?: number) {
  if (!ms) return ''
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function formatSize(bytes?: number) {
  if (bytes === undefined || bytes === null) return ''
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function docIcon(e: BrowseEntry) {
  if (e.isDir) return 'folder'
  const map: Record<string, IconName> = { pdf: 'file', docx: 'file', txt: 'file', md: 'file' }
  return map[e.docType || ''] || 'file'
}
</script>

<template>
  <div class="workspace-page">
    <div class="page-header">
      <div>
        <h2>文件资源管理器</h2>
        <p v-if="ws.info" class="ws-path">{{ ws.info.workspacePath || '（未设置工作区）' }}</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-ghost btn-sm" @click="loadRoot">
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
        <span class="init-banner-desc">选择目录并配置模型端点后即可开始使用。</span>
      </div>
      <button class="btn btn-primary btn-sm" @click="router.push('/settings')">去设置</button>
    </div>

    <template v-if="ws.initialized">
      <!-- 文件名搜索栏 -->
      <div class="search-bar">
        <div class="search-input-wrap">
          <Icon name="search" :size="15" class="search-input-icon" />
          <input
            v-model="searchQuery"
            class="input search-input"
            placeholder="按文件名 / 路径搜索…（实时扫描磁盘，不依赖索引）"
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
        <button v-else class="btn btn-ghost btn-sm" @click="loadRoot">
          <Icon name="refresh" :size="14" />
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
              @click.stop="navigateTo(r.relPath); clearSearch(); showSearch = false; searchQuery = ''"
            >
              进入
            </button>
          </div>
          <div v-if="searchResults.length > 0 && searchTotal > searchResults.length" class="pagination-note">
            共 {{ searchTotal }} 条命中，仅显示前 {{ searchResults.length }} 条
          </div>
        </div>
      </div>

      <!-- 资源管理器：左目录树 + 右文件列表 -->
      <div v-else class="browser">
        <aside class="tree-pane">
          <div class="tree-pane-title">目录</div>
          <TreeBranch
            :node="root"
            :current-path="currentPath"
            :depth="0"
            @navigate="navigateTo"
            @load="loadChildren"
          />
        </aside>

        <section class="list-pane">
          <nav class="breadcrumb">
            <span
              v-for="(c, i) in crumbs"
              :key="c.path"
              class="crumb"
              :class="{ 'crumb--current': i === crumbs.length - 1 }"
              @click="i < crumbs.length - 1 && navigateTo(c.path)"
            >
              <template v-if="i > 0"><Icon name="chevron-right" :size="12" /></template>
              {{ c.label }}
            </span>
          </nav>

          <div v-if="listing" class="loading">加载中…</div>
          <div v-else-if="entries.length === 0" class="empty-state">此目录为空</div>
          <div v-else class="file-list">
            <div
              v-for="e in entries"
              :key="e.relPath"
              class="file-row"
              :class="{ 'file-row--dir': e.isDir }"
              @click="e.isDir && navigateTo(e.relPath)"
            >
              <span class="file-row-icon">
                <Icon :name="docIcon(e)" :size="16" />
              </span>
              <span class="file-row-name" :title="e.relPath">{{ e.name }}</span>
              <span v-if="!e.isDir" class="file-row-type">
                <span class="doc-badge">{{ e.docType || '—' }}</span>
              </span>
              <span v-if="!e.isDir" class="file-row-size">{{ formatSize(e.size) }}</span>
              <span class="file-row-time">{{ formatTime(e.mtime) }}</span>
              <button
                v-if="!e.isDir"
                class="btn btn-ghost btn-sm file-row-open"
                @click.stop="openFile(e.relPath)"
                :disabled="openingPath === e.relPath"
              >
                {{ openingPath === e.relPath ? '打开中…' : '打开' }}
              </button>
            </div>
          </div>
        </section>
      </div>
    </template>

    <div v-else-if="!ws.info" class="loading">加载工作区信息…</div>
    <div v-else class="empty-state">请先初始化工作区以浏览文件</div>
  </div>
</template>

<style scoped>
.workspace-page {
  padding: 20px 24px 28px;
  overflow-y: auto;
  height: 100%;
}

.ws-path {
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
  margin-bottom: 16px;
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

.browser {
  display: grid;
  grid-template-columns: 230px 1fr;
  gap: 16px;
  align-items: start;
}

.tree-pane {
  padding: 12px;
  max-height: calc(100vh - 220px);
  overflow-y: auto;
  background: var(--c-bg-secondary);
  border: 1px solid var(--c-border);
  border-radius: var(--r-lg);
  position: sticky;
  top: 0;
}

.tree-pane-title {
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.04em;
  color: var(--c-text-tertiary);
  text-transform: uppercase;
  margin-bottom: 8px;
  padding: 0 4px;
}

.list-pane {
  min-width: 0;
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

.file-row {
  display: grid;
  grid-template-columns: 24px minmax(0, 1fr) 70px 80px 150px auto;
  align-items: center;
  gap: 10px;
  padding: 9px 14px;
  border-bottom: 1px solid var(--c-border);
  font-size: 13px;
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

.file-row-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--c-icon-secondary);
}

.file-row-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-primary);
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

.file-row-open {
  white-space: nowrap;
  justify-self: end;
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
}
</style>