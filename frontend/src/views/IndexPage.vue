<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useFilesStore } from '@/stores/files'
import { useTagsStore } from '@/stores/tags'
import { searchFiles, updateFileTags, browseOpen, reindexAll, retryFile as apiRetryFile, acceptSuggestion, rejectSuggestion } from '@/api/client'
import type { SearchResult, FileItem } from '@/types'
import Icon from '@/components/Icon.vue'

const router = useRouter()
const ws = useWorkspaceStore()
const files = useFilesStore()
const tags = useTagsStore()
const suggestionBusy = ref<number | null>(null) // 正在处理中的建议 ID
const suggestionError = ref('')

// 语义/内容搜索
const searchQuery = ref('')
const searchResults = ref<SearchResult[]>([])
const searchTotal = ref(0)
const searching = ref(false)
const hasSearched = ref(false)

// 状态/标签筛选（与后端 FilesList 状态机一致：pending/extracting/embedding/indexed/failed/ignored）
const statusOptions = [
  { value: '', label: '全部状态' },
  { value: 'pending', label: '等待整理' },
  { value: 'extracting', label: '处理中' },
  { value: 'embedding', label: '处理中' },
  { value: 'indexed', label: '已整理' },
  { value: 'failed', label: '失败' },
  { value: 'ignored', label: '已忽略' },
]

const fileError = ref('')
const reindexing = ref(false)
const openError = ref('')
const tagEditor = ref<{ id: number; relPath: string; tags: string[] } | null>(null)
const tagInput = ref('')

onMounted(async () => {
  await ws.fetchInfo()
  await Promise.all([files.fetch(), tags.fetchTags(), tags.fetchSuggestions()])
})

// 工作区初始化后加载
watch(
  () => ws.initialized,
  async (v) => {
    if (v) {
      await Promise.all([files.fetch(), tags.fetchTags()])
    }
  },
)

// 标签建议：接受/拒绝（修复 H-01：后端已实现，前端此前未接线）
async function handleSuggestion(id: number, action: 'accept' | 'reject') {
  suggestionBusy.value = id
  suggestionError.value = ''
  try {
    if (action === 'accept') {
      await acceptSuggestion(id)
    } else {
      await rejectSuggestion(id)
    }
    await tags.fetchSuggestions()
    await tags.fetchTags()
    await files.fetch({ page: 0 })
  } catch (e: any) {
    suggestionError.value = e?.message || '处理标签建议失败'
  } finally {
    suggestionBusy.value = null
  }
}

// 状态筛选变化时重新加载（回到第一页）
watch(
  () => files.statusFilter,
  () => files.fetch({ page: 0 }),
)

// 标签筛选变化时重新加载（回到第一页）
watch(
  () => files.tagFilter,
  () => files.fetch({ page: 0 }),
)

// ──────── 语义搜索 ────────

async function doSearch() {
  const q = searchQuery.value.trim()
  if (!q) return
  searching.value = true
  fileError.value = ''
  hasSearched.value = true
  try {
    const res = await searchFiles({ q, tag: files.tagFilter || undefined })
    searchResults.value = res.items
    searchTotal.value = res.total
  } catch (e: any) {
    fileError.value = e.message || '搜索失败'
    searchResults.value = []
    searchTotal.value = 0
  } finally {
    searching.value = false
  }
}

function clearSearch() {
  searchQuery.value = ''
  searchResults.value = []
  searchTotal.value = 0
  hasSearched.value = false
}

// 标签过滤切换
function toggleTagFilter(name: string) {
  files.tagFilter = files.tagFilter === name ? '' : name
}

// ──────── 重建索引 ────────

async function handleReindex() {
  reindexing.value = true
  fileError.value = ''
  files.setReindexProgress({ phase: 'reset', done: 0, total: 0, current: '' })
  try {
    await reindexAll()
  } catch (e: any) {
    fileError.value = e.message || '重新整理失败'
    files.setReindexProgress(null)
  } finally {
    reindexing.value = false
  }
}

async function retryFileItem(f: FileItem) {
  try {
    await apiRetryFile(f.id)
    files.fetch()
  } catch (e: any) {
    fileError.value = e.message || '重试失败'
  }
}

// ──────── 标签编辑 ────────

function openTagEditor(f: FileItem) {
  tagEditor.value = {
    id: f.id,
    relPath: f.relPath,
    tags: (f.tags || []).map((t) => t.name),
  }
  tagInput.value = ''
}

function closeTagEditor() {
  tagEditor.value = null
  tagInput.value = ''
}

function addTag() {
  if (!tagEditor.value || !tagInput.value.trim()) return
  const name = tagInput.value.trim()
  if (!tagEditor.value.tags.includes(name)) {
    tagEditor.value.tags.push(name)
  }
  tagInput.value = ''
}

function removeTag(name: string) {
  if (!tagEditor.value) return
  tagEditor.value.tags = tagEditor.value.tags.filter((t) => t !== name)
}

async function saveTags() {
  if (!tagEditor.value) return
  const { id, tags: newTags } = tagEditor.value
  const orig = files.items.find((f) => f.id === id)
  const origTags = (orig?.tags || []).map((t) => t.name)
  const add = newTags.filter((t) => !origTags.includes(t))
  const remove = origTags.filter((t) => !newTags.includes(t))
  fileError.value = ''
  try {
    await updateFileTags(id, add, remove)
    await files.fetch()
    await tags.fetchTags()
    closeTagEditor()
  } catch (e: any) {
    fileError.value = e.message || '保存标签失败'
  }
}

// ──────── 打开 / 问答 ────────

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

function goFileQA(f: FileItem) {
  router.push({ path: '/qa', query: { mode: 'file', fileId: String(f.id) } })
}

// ──────── 工具 ────────

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
    pending: '等待整理',
    extracting: '处理中',
    embedding: '处理中',
    indexed: '已整理',
    failed: '失败',
    ignored: '已忽略',
  }
  return map[s] || s
}

function statusClass(s: string) {
  const map: Record<string, string> = {
    indexed: 'status-chip--ok',
    extracting: 'status-chip--busy',
    embedding: 'status-chip--busy',
    failed: 'status-chip--err',
    ignored: 'status-chip--muted',
  }
  return map[s] || 'status-chip--muted'
}

// 相关度分数 → 通俗等级（对小白不展示百分比，避免误以为是"准确率"）
function scoreLevel(score: number): string {
  if (score >= 0.7) return '相关度高'
  if (score >= 0.4) return '相关度中'
  return '相关度低'
}
</script>

<template>
  <div class="index-page">
    <div class="page-header">
      <div>
        <h2>内容整理</h2>
        <p class="page-sub">按内容搜索文档，查看整理进度与标签</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-ghost btn-sm" @click="files.fetch()">
          <Icon name="refresh" :size="14" />
        </button>
        <button class="btn btn-primary btn-sm" @click="handleReindex" :disabled="reindexing || !ws.initialized">
          <Icon name="refresh" :size="14" />
          {{ reindexing ? '整理中…' : '重新整理' }}
        </button>
      </div>
    </div>

    <div v-if="ws.info && !ws.initialized" class="init-banner card">
      <div class="init-banner__content">
        <strong>还没有选择要管理的文件夹</strong>
        <span class="init-banner-desc">选好文件夹并连接 AI 后，就能按内容搜索、自动整理和问答。</span>
      </div>
      <button class="btn btn-primary btn-sm" @click="router.push('/settings')">去设置</button>
    </div>

    <template v-if="ws.initialized">
      <div v-if="fileError" class="alert alert--error">{{ fileError }}</div>
      <div v-if="openError" class="alert alert--error">{{ openError }}</div>

      <!-- AI 未配置引导 -->
      <div v-if="!ws.info?.embedConfigured" class="ai-hint card">
        <Icon name="search" :size="14" />
        <span>「按内容搜索」需要先连接 AI 服务，点这里去设置（暂时跳过也能浏览文件）</span>
        <button class="btn btn-ghost btn-sm" @click="router.push('/settings')">去设置</button>
      </div>

      <!-- 按内容搜索（语义搜索） -->
      <div class="search-bar">
        <div class="search-input-wrap">
          <Icon name="search" :size="15" class="search-input-icon" />
          <input
            v-model="searchQuery"
            class="input search-input"
            placeholder="按内容搜索：输入你想找的内容，如「今年预算」"
            @keyup.enter="doSearch"
          />
        </div>
        <button class="btn btn-primary btn-sm" @click="doSearch" :disabled="searching || !searchQuery.trim()">
          {{ searching ? '搜索中…' : '搜索' }}
        </button>
        <button v-if="hasSearched" class="btn btn-ghost btn-sm" @click="clearSearch">
          <Icon name="arrow-left" :size="14" />
          返回文件列表
        </button>
      </div>

      <!-- 标签过滤墙 -->
      <div v-if="tags.tags.length" class="tag-wall">
        <span class="tag-wall__label">标签过滤</span>
        <span
          v-for="t in tags.tags"
          :key="t.id"
          class="tag-chip tag-wall__item"
          :class="{ 'tag-wall__item--picked': t.name === files.tagFilter }"
          @click="toggleTagFilter(t.name)"
        >{{ t.name }}</span>
        <button v-if="files.tagFilter" class="btn btn-ghost btn-sm tag-wall__clear" @click="toggleTagFilter(files.tagFilter)">
          <Icon name="x" :size="13" />
          清除筛选
        </button>
      </div>

      <!-- 标签建议（待处理） -->
      <div v-if="tags.suggestions.length" class="suggestion-wall card">
        <div class="suggestion-wall__head">
          <span class="suggestion-wall__label">标签建议</span>
          <span class="suggestion-wall__hint">由 AI 根据文档内容自动生成，接受后将作为该文件的标签</span>
        </div>
        <div v-if="suggestionError" class="alert alert--error">{{ suggestionError }}</div>
        <div v-for="s in tags.suggestions" :key="s.id" class="suggestion-item">
          <span class="suggestion-item__name">{{ s.name }}</span>
          <span v-if="s.relPath" class="suggestion-item__file" :title="s.relPath">{{ s.relPath }}</span>
          <span v-if="s.reason" class="suggestion-item__reason">{{ s.reason }}</span>
          <div class="suggestion-item__actions">
            <button
              class="btn btn-primary btn-sm"
              :disabled="suggestionBusy === s.id"
              @click="handleSuggestion(s.id, 'accept')"
            >
              <Icon name="check" :size="13" />
              接受
            </button>
            <button
              class="btn btn-ghost btn-sm"
              :disabled="suggestionBusy === s.id"
              @click="handleSuggestion(s.id, 'reject')"
            >
              <Icon name="x" :size="13" />
              拒绝
            </button>
          </div>
        </div>
      </div>

      <!-- 语义搜索结果 -->
      <div v-if="hasSearched" class="search-results">
        <div v-if="searching" class="loading">搜索中…</div>
        <div v-else-if="searchResults.length === 0" class="empty-state">
          <span class="empty-state__title">没有找到「{{ searchQuery }}」相关的内容</span>
          <span class="empty-state__desc">试试换个说法或更简短的关键词</span>
        </div>
        <template v-else>
          <div v-for="r in searchResults" :key="r.fileId" class="search-item card">
            <div class="search-item-main">
<div class="search-item-top">
                <span class="search-item-path" :title="r.relPath">{{ r.relPath }}</span>
                <span v-if="r.score !== undefined" class="search-item-score">
                  {{ scoreLevel(r.score) }}
                </span>
              </div>
              <div v-if="r.hitText" class="search-item-hit">{{ r.hitText }}</div>
              <div v-if="r.tags?.length" class="search-item-tags">
                <span v-for="t in r.tags" :key="t.name" class="tag-chip">{{ t.name }}</span>
              </div>
            </div>
            <div class="search-item-actions">
              <button class="btn btn-ghost btn-sm" @click="openFile(r.relPath)">
                <Icon name="external" :size="13" />
                打开
              </button>
              <button class="btn btn-ghost btn-sm" @click="router.push({ path: '/qa', query: { mode: 'file', fileId: String(r.fileId) } })">
                <Icon name="chat" :size="13" />
                问答
              </button>
            </div>
          </div>
          <div v-if="searchResults.length > 0 && searchTotal > searchResults.length" class="pagination-note">
            共 {{ searchTotal }} 条命中，仅显示前 {{ searchResults.length }} 条
          </div>
        </template>
      </div>

      <!-- 文件列表（索引文件为主数据源） -->
      <div v-else class="index-list">
        <!-- 全量重建索引进度 -->
        <div v-if="files.reindexProgress" class="reindex-progress card">
          <template v-if="files.reindexProgress.phase === 'done'">
            <Icon name="check" :size="14" class="reindex-progress__icon" />
            <span>整理完成，共处理 {{ files.reindexProgress.total }} 个文件</span>
          </template>
          <template v-else-if="files.reindexProgress.phase === 'reset'">
            <Icon name="refresh" :size="14" class="reindex-progress__icon spin" />
            <span>正在准备…</span>
          </template>
          <template v-else>
            <Icon name="refresh" :size="14" class="reindex-progress__icon spin" />
            <span class="reindex-progress__count">正在整理 {{ files.reindexProgress.done }}/{{ files.reindexProgress.total }}</span>
            <span class="reindex-progress__file" :title="files.reindexProgress.current">{{ files.reindexProgress.current }}</span>
            <div class="reindex-progress__bar">
              <div
                class="reindex-progress__fill"
                :style="{ width: (files.reindexProgress.total ? (files.reindexProgress.done / files.reindexProgress.total) * 100 : 0) + '%' }"
              ></div>
            </div>
          </template>
        </div>

        <div class="filter-bar">
          <select v-model="files.statusFilter" class="select filter-select">
            <option v-for="s in statusOptions" :key="s.value" :value="s.value">{{ s.label }}</option>
          </select>
          <div class="filter-bar__tail">
            <span class="filter-bar__hint">每页</span>
            <select v-model.number="files.pageSize" class="select filter-select" @change="files.fetch({ page: 0, pageSize: files.pageSize })">
              <option :value="10">10</option>
              <option :value="20">20</option>
              <option :value="50">50</option>
              <option :value="100">100</option>
            </select>
          </div>
        </div>

        <div v-if="files.loading" class="loading">加载中…</div>
        <div v-else-if="files.error" class="alert alert--error">{{ files.error }}</div>
        <div v-else-if="files.items.length === 0" class="empty-state">
          <span class="empty-state__title">还没有需要整理的文件</span>
          <span class="empty-state__desc">把文档放进工作文件夹后，点右上角「重新整理」就会出现在这里</span>
        </div>
        <div v-else class="file-table">
          <div class="file-table-head">
            <span class="file-col-name sortable" @click="files.cycleSort('name')">
              文件
              <span class="sort-arrow" v-if="files.sortField === 'name'">{{ files.sortDir === 'asc' ? '▲' : '▼' }}</span>
            </span>
            <span class="sortable" @click="files.cycleSort('type')">
              类型
              <span class="sort-arrow" v-if="files.sortField === 'type'">{{ files.sortDir === 'asc' ? '▲' : '▼' }}</span>
            </span>
            <span class="sortable" @click="files.cycleSort('status')">
              状态
              <span class="sort-arrow" v-if="files.sortField === 'status'">{{ files.sortDir === 'asc' ? '▲' : '▼' }}</span>
            </span>
            <span class="file-col-tags">标签</span>
            <span>大小</span>
            <span class="sortable" @click="files.cycleSort('time')">
              整理时间
              <span class="sort-arrow" v-if="files.sortField === 'time'">{{ files.sortDir === 'asc' ? '▲' : '▼' }}</span>
            </span>
            <span>操作</span>
          </div>
          <div v-for="f in files.items" :key="f.id" class="file-table-row">
            <span class="file-name" :title="f.relPath">
              <Icon name="file" :size="14" class="file-name__icon" />
              {{ f.relPath }}
            </span>
            <span class="file-cell file-doc">{{
              f.docType
            }}</span>
            <span class="file-cell">
              <span class="status-chip" :class="statusClass(f.indexStatus)">{{ statusLabel(f.indexStatus) }}</span>
              <span v-if="f.lastError" class="file-err" :title="f.lastError">{{ f.lastError }}</span>
            </span>
            <span class="file-cell file-tags">
              <span v-for="t in (f.tags || [])" :key="t.name" class="tag-chip tag-chip--sm">{{ t.name }}</span>
              <button class="btn btn-ghost btn-mini" @click="openTagEditor(f)">
                <Icon name="plus" :size="12" />
              </button>
            </span>
            <span class="file-cell">{{ formatSize(f.size) }}</span>
            <span class="file-cell file-time">{{ formatTime(f.lastIndexedAt) }}</span>
            <span class="file-cell file-actions">
              <button
                class="btn btn-ghost btn-mini"
                @click="openFile(f.relPath)"
                :disabled="openingPath === f.relPath"
              >
                {{ openingPath === f.relPath ? '打开中…' : '打开' }}
              </button>
              <button class="btn btn-ghost btn-mini" @click="goFileQA(f)" :disabled="f.indexStatus !== 'indexed'">问答</button>
              <button
                v-if="f.indexStatus === 'failed'"
                class="btn btn-ghost btn-mini btn-mini--warn"
                title="重新整理这个文件"
                @click="retryFileItem(f)"
              >
                重试
              </button>
            </span>
          </div>
        </div>
        <div v-if="files.total > files.items.length" class="pagination-bar">
          <span class="pagination-bar__note">共 {{ files.total }} 个文件，第 {{ files.page + 1 }} 页 / {{ Math.ceil(files.total / files.pageSize) }} 页</span>
          <div class="pagination-bar__actions">
            <button class="btn btn-ghost btn-sm" @click="files.prevPage()" :disabled="files.page === 0">
              <Icon name="arrow-left" :size="13" />
              上一页
            </button>
            <button class="btn btn-ghost btn-sm" @click="files.nextPage()" :disabled="files.page * files.pageSize + files.items.length >= files.total">
              下一页
              <Icon name="arrow-right" :size="13" />
            </button>
          </div>
        </div>
      </div>
    </template>

    <div v-else class="empty-state">还没有选择要管理的文件夹，请到「设置」里开始使用</div>

    <!-- 标签编辑弹窗 -->
    <div v-if="tagEditor" class="modal-overlay" @click.self="closeTagEditor">
      <div class="modal modal--tags">
        <div class="modal-title">编辑标签</div>
        <div class="modal-path" :title="tagEditor.relPath">{{ tagEditor.relPath }}</div>
        <div class="modal-tags">
          <span v-for="t in tagEditor.tags" :key="t" class="tag-chip tag-chip--editable">
            {{ t }}
            <span class="tag-chip__remove" @click="removeTag(t)">×</span>
          </span>
          <span v-if="tagEditor.tags.length === 0" class="modal-empty-tags">暂无标签</span>
        </div>
        <div class="modal-add">
          <input
            v-model="tagInput"
            class="input"
            placeholder="输入新标签后回车添加"
            @keyup.enter="addTag"
          />
          <button class="btn btn-ghost btn-sm" @click="addTag">
            <Icon name="plus" :size="13" />
            添加
          </button>
        </div>
        <div class="modal-actions">
          <button class="btn btn-ghost btn-sm" @click="closeTagEditor">取消</button>
          <button class="btn btn-primary btn-sm" @click="saveTags">
            <Icon name="check" :size="13" />
            保存
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.index-page {
  padding: 20px 24px 28px;
  overflow-y: auto;
  height: 100%;
}

.page-sub {
  font-size: 12px;
  color: var(--c-text-tertiary);
  margin: 2px 0 0;
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

/* AI 未配置引导条 */
.ai-hint {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  margin-bottom: 14px;
  font-size: 12.5px;
  color: var(--c-text-secondary);
  border-color: var(--c-warning);
  background: var(--c-warning-soft);
}
.ai-hint span {
  flex: 1;
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

/* 标签过滤墙 */
.tag-wall {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  padding: 10px 12px;
  margin-bottom: 14px;
  background: var(--c-bg-secondary);
  border: 1px solid var(--c-border);
  border-radius: var(--r-lg);
}

.tag-wall__label {
  font-size: 12px;
  font-weight: 600;
  color: var(--c-text-tertiary);
  margin-right: 2px;
}

.tag-wall__item {
  cursor: pointer;
  transition: all 0.12s ease;
}

.tag-wall__item:hover {
  filter: brightness(1.05);
}

.tag-wall__item.tag-wall__item--picked {
  background: var(--c-brand);
  color: var(--c-on-brand);
}

.tag-wall__clear {
  color: var(--c-text-tertiary);
}
.tag-wall__clear:hover {
  color: var(--c-danger);
}

/* 标签建议面板（修复 H-01：后端已实现，前端此前未接线） */
.suggestion-wall {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 14px;
  margin-bottom: 16px;
}
.suggestion-wall__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
}
.suggestion-wall__label {
  font-weight: 600;
  font-size: 13px;
}
.suggestion-wall__hint {
  font-size: 12px;
  color: var(--c-text-secondary);
}
.suggestion-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid var(--c-border);
  border-radius: var(--r-sm);
  background: var(--c-bg-card);
  font-size: 13px;
}
.suggestion-item__name {
  font-weight: 600;
  flex-shrink: 0;
}
.suggestion-item__file {
  color: var(--c-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 40%;
}
.suggestion-item__reason {
  color: var(--c-text-secondary);
  font-size: 12px;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.suggestion-item__actions {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

.search-results {
  margin-bottom: 16px;
}

.search-item {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 14px;
  margin-bottom: 8px;
  font-size: 13px;
  transition: border-color 0.15s ease, transform 0.12s ease;
}

.search-item:hover {
  border-color: var(--c-border-strong);
}

.search-item-main {
  min-width: 0;
  flex: 1;
}

.search-item-top {
  display: flex;
  align-items: baseline;
  gap: 10px;
}

.search-item-path {
  font-weight: 600;
  color: var(--c-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}

.search-item-score {
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 600;
  color: var(--c-brand);
  background: var(--c-brand-soft);
  border-radius: var(--r-full);
  padding: 2px 8px;
  min-width: 46px;
  text-align: center;
}

.search-item-hit {
  margin-top: 6px;
  font-size: 12.5px;
  color: var(--c-text-secondary);
  line-height: 1.6;
  background: var(--c-bg-secondary);
  border-radius: var(--r-sm);
  padding: 6px 10px;
  max-height: 60px;
  overflow: hidden;
  white-space: pre-wrap;
}

.search-item-tags {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
  margin-top: 8px;
}

.search-item-actions {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

.filter-select {
  max-width: 160px;
}

.reindex-progress {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
  padding: 12px 14px;
  font-size: 13px;
  color: var(--c-text-secondary);
  flex-wrap: wrap;
}

.reindex-progress__icon {
  color: var(--c-brand);
  flex-shrink: 0;
}

.reindex-progress__count {
  font-weight: 600;
  color: var(--c-text-primary);
  white-space: nowrap;
}

.reindex-progress__file {
  flex: 1;
  min-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-tertiary);
  font-size: 12px;
}

.reindex-progress__bar {
  width: 100%;
  height: 6px;
  border-radius: var(--r-full);
  background: var(--c-bg-secondary);
  overflow: hidden;
}

.reindex-progress__fill {
  height: 100%;
  border-radius: var(--r-full);
  background: var(--c-brand);
  transition: width 0.3s ease;
}

.spin {
  animation: reindex-spin 1s linear infinite;
}

@keyframes reindex-spin {
  to {
    transform: rotate(360deg);
  }
}

.file-table {
  border: 1px solid var(--c-border);
  border-radius: var(--r-lg);
  background: var(--c-bg-panel);
  overflow: hidden;
  overflow-x: auto;
}

.file-table-head,
.file-table-row {
  display: grid;
  grid-template-columns: minmax(200px, 2fr) 64px 110px minmax(140px, 1fr) 72px 130px auto;
  align-items: center;
  gap: 10px;
  padding: 9px 14px;
  font-size: 13px;
}

.file-table-head {
  font-weight: 600;
  color: var(--c-text-tertiary);
  background: var(--c-bg-secondary);
  border-bottom: 1px solid var(--c-border);
  font-size: 12px;
  letter-spacing: 0.02em;
}

/* 首列表头用 padding 对齐行内文件名前的图标（14px icon + 8px gap） */
.file-col-name {
  padding-left: 22px;
}

.file-table-row {
  border-bottom: 1px solid var(--c-border);
  transition: background 0.12s ease;
}

.file-table-row:last-child {
  border-bottom: none;
}

.file-table-row:hover {
  background: var(--c-bg-hover);
}

.file-name {
  display: flex;
  align-items: center;
  gap: 8px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-primary);
  font-weight: 500;
}

.file-name-icon,
.file-name__icon {
  color: var(--c-icon-secondary);
  flex-shrink: 0;
}

.file-cell {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.file-doc {
  color: var(--c-text-secondary);
  text-transform: uppercase;
  font-weight: 600;
  font-size: 12px;
}

.file-time {
  color: var(--c-text-tertiary);
  font-size: 12px;
}

.file-err {
  display: block;
  font-size: 11px;
  color: var(--c-danger);
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 160px;
}

.file-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  align-items: center;
  overflow: visible;
}

.tag-chip--sm {
  font-size: 11px;
  padding: 1px 8px;
}

.file-actions {
  display: flex;
  gap: 6px;
}

/* 弹窗 */
.modal--tags {
  width: 460px;
  max-width: 90vw;
}

.modal-title {
  font-size: 16px;
  font-weight: 700;
  margin-bottom: 4px;
}

.modal-path {
  font-size: 12px;
  color: var(--c-text-secondary);
  margin-bottom: 16px;
  word-break: break-all;
}

.modal-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 12px;
  min-height: 32px;
}

.modal-empty-tags {
  color: var(--c-text-tertiary);
  font-size: 13px;
}

.tag-chip--editable {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 10px;
  border: 1px solid var(--c-brand-border);
}

.tag-chip__remove {
  cursor: pointer;
  color: var(--c-brand);
  font-weight: 700;
  font-size: 14px;
  line-height: 1;
  opacity: 0.7;
}

.tag-chip__remove:hover {
  opacity: 1;
}

.modal-add {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
}

.modal-add .input {
  flex: 1;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.filter-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.filter-bar__tail {
  display: flex;
  align-items: center;
  gap: 8px;
}

.filter-bar__hint {
  font-size: 12px;
  color: var(--c-text-tertiary);
}

/* 表头可点击排序 */
.file-table-head .sortable {
  cursor: pointer;
  user-select: none;
  display: flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
}

.file-table-head .sortable:hover {
  color: var(--c-brand);
}

.sort-arrow {
  font-size: 10px;
  color: var(--c-brand);
}

/* 翻页栏 */
.pagination-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 14px;
  font-size: 12px;
  color: var(--c-text-tertiary);
}

.pagination-bar__actions {
  display: flex;
  gap: 8px;
}
</style>