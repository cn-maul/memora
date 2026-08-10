<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { browseDir, browseSearch, browseOpen, getFileHistory, downloadHistoryVersion, resolveFileId, getCommitList, getCommitFiles } from '@/api/client'
import type { BrowseEntry, BrowseSearchItem, CommitItem, VersionFile } from '@/types'
import TreeBranch, { type TreeNode } from '@/components/TreeBranch.vue'
import Crumbs from '@/components/Crumbs.vue'
import Icon, { type IconName } from '@/components/Icon.vue'

const router = useRouter()
const route = useRoute()
const ws = useWorkspaceStore()

// 目录树
// TreeNode 复用 TreeBranch 导出的类型（单一事实来源），避免 isDir 可选/必填不一致导致的类型错误
const root = ref<TreeNode>({
  name: '工作区',
  relPath: '/',
  isDir: true, // 必须显式标记：TreeBranch 据此走"展开"分支，缺省会被当作文件点击（修复：树无法展开）
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

// ── 提交版本浏览 ──
// commitHash 为空 = 查看工作区（最新）；非空 = 查看某提交快照的全部文件
const commits = ref<CommitItem[]>([])
const commitHash = ref('')
const versionFiles = ref<VersionFile[]>([])
const versionLoading = ref(false)
const versionError = ref('')
const inVersionMode = computed(() => commitHash.value !== '')

onMounted(async () => {
  await ws.fetchInfo()
  if (ws.initialized) {
    const c = typeof route.query.commit === 'string' ? route.query.commit : ''
    if (c) {
      await selectCommit(c)
    } else {
      await loadRoot()
    }
    loadCommits()
  }
})

// 会话中工作区变化后重新加载
watch(() => ws.initialized, async (v) => {
  if (v) await loadRoot()
})

// 从提交记录"查看文件"跳转过来时切换版本
watch(() => route.query.commit, async (val) => {
  const c = typeof val === 'string' ? val : ''
  await selectCommit(c)
})

// ── 提交版本浏览 ──

async function loadCommits() {
  try {
    commits.value = (await getCommitList()) || []
  } catch {
    commits.value = []
  }
}

async function selectCommit(hash: string) {
  const leavingVersion = commitHash.value !== '' && hash === ''
  commitHash.value = hash || ''
  if (hash) {
    versionLoading.value = true
    versionError.value = ''
    try {
      versionFiles.value = await getCommitFiles(hash)
    } catch (e: any) {
      versionError.value = e.message || '加载提交文件失败'
      versionFiles.value = []
    } finally {
      versionLoading.value = false
    }
  } else if (leavingVersion) {
    // 回到工作区浏览：确保目录树已加载
    await loadRoot()
  }
}

function onCommitChange() {
  // 通过 URL query 驱动 watch，避免重复请求
  const q = commitHash.value ? { commit: commitHash.value } : {}
  router.replace({ path: '/workspace', query: q })
}

function shortHash(hash: string) {
  return hash.slice(0, 7)
}

function commitTitle(msg: string) {
  return (msg || '').split('\n')[0]
}

// ──────── 目录树 ────────

async function loadRoot() {
  root.value = { name: '工作区', relPath: '/', isDir: true, expanded: true, loading: false, hasLoaded: false, children: [] }
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
    const entries = res.entries || []
    const dirs = entries.filter((e) => e.isDir)
    // 子树只含子文件夹，文件在右侧列表展示。「空文件夹」仅当目录完全无内容时显示
    node.empty = entries.length === 0
    node.children = dirs.map((d) => ({
      name: d.name,
      relPath: d.relPath,
      isDir: true,
      expanded: false,
      loading: false,
      hasLoaded: false,
      children: [],
    }))
    node.hasLoaded = true
  } catch {
    node.children = []
    node.empty = true
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

// 文件详情弹窗
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
  const map: Record<string, IconName> = { pdf: 'file-pdf', docx: 'file-word', pptx: 'file-ppt', xlsx: 'file-excel', txt: 'file-text', md: 'file-text' }
  return map[e.docType || ''] || 'file'
}
</script>

<template>
  <div class="workspace-page">
    <div class="page-header">
      <div>
        <h2>全部文件</h2>
        <p v-if="ws.info" class="ws-path">{{ ws.info.workspacePath || '（未设置工作区）' }}</p>
      </div>
      <div class="header-actions">
        <!-- 版本选择：默认最新（工作区），可切换查看历史提交的全部文件 -->
        <select v-model="commitHash" class="select version-select" @change="onCommitChange" :title="inVersionMode ? '查看该提交的全部文件' : '查看最新工作区文件'">
          <option value="">最新版本（工作区）</option>
          <option v-for="c in commits" :key="c.hash" :value="c.hash">
            {{ shortHash(c.hash) }} · {{ commitTitle(c.message) }}
          </option>
        </select>
        <button class="btn btn-ghost btn-sm" @click="inVersionMode ? '' : loadRoot()" :disabled="inVersionMode">
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
      <!-- 工作区模式（最新）：搜索栏 + 目录树/文件列表浏览 -->
      <template v-if="!inVersionMode">
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
        <div v-else-if="searchResults.length === 0" class="empty-state empty-state--icon">
          <span class="empty-state__icon"><Icon name="search" :size="20" /></span>
          <span class="empty-state__title">未找到匹配的文件</span>
          <span class="empty-state__desc">「{{ searchQuery }}」没有命中，换个关键词试试</span>
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

      <!-- 资源管理器：一体式面板（目录树 ｜ 文件列表），各自独立滚动 -->
      <div v-else class="browser file-manager__body">
        <aside class="tree-pane file-manager__tree">
          <div class="tree-pane__head">
            <span class="tree-pane__title">目录</span>
            <button class="btn btn-ghost btn-mini tree-pane__refresh" title="刷新目录树" @click="loadRoot">
              <Icon name="refresh" :size="12" />
            </button>
          </div>
          <div class="tree-pane__scroll">
            <TreeBranch
              :node="root"
              :current-path="currentPath"
              :depth="0"
              @navigate="navigateTo"
              @load="loadChildren"
            />
          </div>
        </aside>

        <section class="list-pane file-manager__list">
          <div class="list-toolbar">
            <Crumbs :items="crumbs" @navigate="navigateTo" />
          </div>
          <div class="list-scroll">
            <div v-if="listing" class="loading">加载中…</div>
            <div v-else-if="entries.length === 0" class="empty-state empty-state--icon">
              <span class="empty-state__icon"><Icon name="folder-open" :size="20" /></span>
              <span class="empty-state__title">此目录为空</span>
              <span class="empty-state__desc">将文档放入该目录后会自动加入索引</span>
            </div>
            <div v-else class="file-list">
              <div class="file-list-head list-head">
                <span class="file-col-name">名称</span>
                <span>类型</span>
                <span class="file-col-right">大小</span>
                <span class="file-col-right">修改时间</span>
                <span class="file-col-right">操作</span>
              </div>
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
                <span v-else class="file-row-type"></span>
                <span v-if="!e.isDir" class="file-row-size">{{ formatSize(e.size) }}</span>
                <span v-else class="file-row-size"></span>
                <span class="file-row-time">{{ formatTime(e.mtime) }}</span>
                <span class="file-row-actions">
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
            </div>
          </div>
        </section>
      </div>
      </template><!-- /!inVersionMode -->

      <!-- 版本模式：某提交快照的全部文件列表 -->
      <template v-else>
        <div class="version-panel card">
          <div class="version-panel__head">
            <Icon name="git-branch" :size="14" />
            <span>提交 <span class="version-hash-label">{{ shortHash(commitHash) }}</span></span>
            <span class="version-commit-title" :title="commitTitle(commits.find(c => c.hash === commitHash)?.message || '')">
              {{ commitTitle(commits.find(c => c.hash === commitHash)?.message || '') }}
            </span>
            <span class="version-count">{{ versionFiles.length }} 个文件</span>
          </div>
          <div v-if="versionLoading" class="loading">加载中…</div>
          <div v-else-if="versionError" class="alert alert--error">{{ versionError }}</div>
          <div v-else-if="versionFiles.length === 0" class="empty-state">该提交没有文件</div>
          <div v-else class="version-rows">
            <div class="version-list-head">
              <span>路径</span>
              <span>类型</span>
              <span>大小</span>
            </div>
            <div v-for="f in versionFiles" :key="f.path" class="version-row">
              <span class="version-path" :title="f.path">{{ f.path }}</span>
              <span class="version-cell">
                <span class="doc-badge">{{ f.docType || '—' }}</span>
              </span>
              <span class="version-cell version-size">{{ formatSize(f.size) }}</span>
            </div>
          </div>
        </div>
      </template>
    </template><!-- /ws.initialized -->

    <div v-else-if="!ws.info" class="loading">加载工作区信息…</div>
    <div v-else class="empty-state">请先初始化工作区以浏览文件</div>

    <!-- 文件详情弹窗 -->
    <div v-if="detailEntry" class="modal-overlay" @click.self="detailEntry = null">
      <div class="modal modal--detail">
        <div class="modal-title">
          <Icon :name="detailEntry.isDir ? 'folder' : docIcon(detailEntry)" :size="16" />
          <span class="modal-title__name" :title="detailEntry.name">{{ detailEntry.name }}</span>
        </div>
        <div class="modal-meta">
          <span class="modal-meta__item">
            <Icon name="folder" :size="12" />
            {{ detailEntry.isDir ? '文件夹' : (detailEntry.docType || '文件') }}
          </span>
          <span v-if="!detailEntry.isDir" class="modal-meta__item">
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
          <button v-if="!detailEntry.isDir" class="btn btn-primary btn-sm" @click="openFromDetail">打开当前版本</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 页面本身不再整体滚动：搜索栏固定，内容区（树/列表/搜索）各自独立滚动 */
.workspace-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  padding: 20px 24px 28px;
  overflow: hidden;
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
  flex-shrink: 0;
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
  flex-shrink: 0;
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

/* 搜索结果：独立滚动区（v-if 切换时不撑破 flex 布局） */
.search-results {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
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

/* ──────── 一体式文件管理器：目录树 + 文件列表 ──────── */

.tree-pane__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 9px 10px 9px 14px;
  border-bottom: 1px solid var(--c-border);
  flex-shrink: 0;
}

.tree-pane__title {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--c-text-tertiary);
}

.tree-pane__refresh {
  padding: 2px 6px;
}

.tree-pane__scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 8px 6px;
}

.list-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--c-border);
  flex-shrink: 0;
}

.list-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}

/* 列表（已位于面板内，去掉重复的外边框） */
.file-list {
  overflow: hidden;
}

/* 表头：吸顶 + 与行网格对齐（具体列模板见下方 .file-list-head） */
.file-list-head {
  grid-template-columns: 24px minmax(0, 1fr) 70px 80px 150px auto;
}

.file-col-name {
  padding-left: 34px; /* 与行内 16px 图标 + 10px 间距对齐 */
}

.file-col-right {
  text-align: right;
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

.file-row-actions {
  display: flex;
  gap: 6px;
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

/* ──────── 提交版本浏览 ──────── */
.version-select {
  width: auto;
  max-width: 260px;
  padding: 5px 10px;
  font-size: 12.5px;
}

.version-panel {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  margin-bottom: 0;
}

.version-panel__head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--c-border);
  font-size: 13px;
  color: var(--c-text-secondary);
  flex-shrink: 0;
}

.version-panel__head svg { color: var(--c-brand); }

.version-hash-label {
  font-family: var(--font-mono, monospace);
  color: var(--c-info);
  background: var(--c-info-soft);
  padding: 1px 7px;
  border-radius: var(--r-full);
  font-size: 11px;
}

.version-commit-title {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-primary);
  font-weight: 600;
}

.version-count {
  font-size: 12px;
  color: var(--c-text-tertiary);
  flex-shrink: 0;
}

.version-rows {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}

.version-list-head,
.version-row {
  display: grid;
  grid-template-columns: minmax(200px, 1fr) 90px 90px;
  align-items: center;
  gap: 10px;
  padding: 8px 14px;
  font-size: 13px;
}

.version-list-head {
  position: sticky;
  top: 0;
  z-index: 2;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.03em;
  color: var(--c-text-tertiary);
  background: var(--c-bg-secondary);
  border-bottom: 1px solid var(--c-border);
  white-space: nowrap;
}

.version-row {
  border-bottom: 1px solid var(--c-border);
  transition: background 0.12s ease;
}

.version-row:last-child { border-bottom: none; }
.version-row:hover { background: var(--c-bg-hover); }

.version-path {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-primary);
  font-weight: 500;
  font-family: var(--font-mono, monospace);
}

.version-cell { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.version-size { color: var(--c-text-tertiary); font-size: 12px; text-align: right; }

/* ──────── 文件详情弹窗 ──────── */
.modal--detail { width: 480px; max-width: 90vw; }

.modal-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 8px;
}

.modal-title__name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.modal-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 16px;
  margin-bottom: 16px;
  font-size: 12px;
  color: var(--c-text-tertiary);
}

.modal-meta__item { display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; }
.modal-meta__item svg { color: var(--c-icon-secondary); }

.detail-section { margin-bottom: 14px; font-size: 13px; }

.detail-section__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--c-border);
}

.detail-section__title { font-weight: 600; font-size: 14px; }
.detail-section__hint { font-size: 11px; color: var(--c-text-tertiary); }

.detail-empty { font-size: 12px; color: var(--c-text-tertiary); padding: 16px 0; text-align: center; }

.version-list { display: flex; flex-direction: column; gap: 8px; max-height: 360px; overflow-y: auto; }

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

.version-num { font-size: 11px; font-weight: 700; color: var(--c-brand); background: var(--c-brand-soft); padding: 2px 6px; border-radius: var(--r-xs); }

.version-info { min-width: 0; }
.version-message { font-size: 12px; color: var(--c-text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.version-meta { display: flex; align-items: center; gap: 8px; font-size: 11px; color: var(--c-text-tertiary); margin-top: 2px; }
.version-hash { font-family: monospace; color: var(--c-text-secondary); }

.modal-actions { display: flex; justify-content: flex-end; gap: 10px; margin-top: 14px; }
</style>