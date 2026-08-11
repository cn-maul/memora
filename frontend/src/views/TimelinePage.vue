<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { getCommitList } from '@/api/client'
import type { CommitItem } from '@/types'
import Icon from '@/components/Icon.vue'

const router = useRouter()

const commits = ref<CommitItem[]>([])
const loading = ref(false)
const loadError = ref('')

onMounted(async () => {
  await loadData()
})

async function loadData() {
  loading.value = true
  loadError.value = ''
  try {
    commits.value = await getCommitList()
  } catch (e: any) {
    commits.value = []
    loadError.value = e.message || '加载提交记录失败'
  } finally {
    loading.value = false
  }
}

// 展开的 commit hash 集合
const expanded = ref<Record<string, boolean>>({})

function toggle(hash: string) {
  expanded.value[hash] = !expanded.value[hash]
}

function shortHash(hash: string) {
  return hash.slice(0, 7)
}

function formatTime(ms: number) {
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function messageTitle(msg: string) {
  return (msg || '').split('\n')[0]
}

function viewFiles(hash: string) {
  router.push({ path: '/workspace', query: { commit: hash } })
}

// 每个提交的文件改动统计
function counts(files: CommitItem['files']) {
  const c = { added: 0, modified: 0, deleted: 0 }
  for (const f of files || []) {
    if (f.status === 'added') c.added++
    else if (f.status === 'deleted') c.deleted++
    else c.modified++
  }
  return c
}

const totalCommits = computed(() => commits.value.length)
const totalFiles = computed(() =>
  commits.value.reduce((sum, c) => sum + (c.files?.length || 0), 0),
)

function statusLabel(s: string) {
  const map: Record<string, string> = { added: '新增', modified: '修改', deleted: '删除' }
  return map[s] || s
}
function statusSign(s: string) {
  const map: Record<string, string> = { added: '+', modified: '~', deleted: '−' }
  return map[s] || '·'
}
</script>

<template>
  <div class="commit-page">
    <div class="page-header">
      <div>
        <h2>版本历史</h2>
        <p class="page-sub">你保存的每一个版本，以及每次改动了什么</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-ghost btn-sm" @click="loadData" :disabled="loading">
          <Icon name="refresh" :size="14" />
          {{ loading ? '加载中…' : '刷新' }}
        </button>
      </div>
    </div>

    <!-- 汇总条 -->
    <div v-if="commits.length" class="commit-summary">
      <div class="summary-item">
        <span class="summary-num">{{ totalCommits }}</span>
        <span class="summary-label">版本</span>
      </div>
      <div class="summary-item">
        <span class="summary-num">{{ totalFiles }}</span>
        <span class="summary-label">改动文件</span>
      </div>
    </div>

    <div v-if="loadError" class="alert alert--error">{{ loadError }}</div>

    <div v-if="loading" class="loading">加载中…</div>
    <div v-else-if="commits.length === 0" class="empty-state empty-state--icon">
      <span class="empty-state__icon"><Icon name="git-branch" :size="20" /></span>
      <span class="empty-state__title">还没有版本</span>
      <span class="empty-state__desc">修改任意文件后，系统会自动保存第一个版本；也可在左侧「版本历史」手动保存</span>
    </div>

    <div v-else class="timeline">
      <div v-for="c in commits" :key="c.hash" class="tl-item">
        <div class="tl-marker">
          <span class="tl-dot"></span>
          <span class="tl-line"></span>
        </div>

        <div class="tl-card card">
          <button class="tl-head" @click="toggle(c.hash)">
            <div class="tl-title-row">
              <Icon name="git-branch" :size="14" class="tl-branch" />
              <span class="tl-title">{{ messageTitle(c.message) || '（无备注）' }}</span>
            </div>
            <div class="tl-meta-row">
              <span class="tl-meta tl-time">
                <Icon name="clock" :size="12" />
                {{ formatTime(c.time) }}
              </span>
              <span class="tl-meta tl-author">{{ c.author }}</span>
              <span class="tl-hash" :title="c.hash">{{ shortHash(c.hash) }}</span>

              <span v-if="counts(c.files).added" class="tl-chip tl-chip--added">+{{ counts(c.files).added }} 新增</span>
              <span v-if="counts(c.files).modified" class="tl-chip tl-chip--modified">~{{ counts(c.files).modified }} 修改</span>
              <span v-if="counts(c.files).deleted" class="tl-chip tl-chip--deleted">−{{ counts(c.files).deleted }} 删除</span>

              <button class="btn btn-ghost btn-mini tl-view-files" title="查看该提交的全部文件" @click.stop="viewFiles(c.hash)">
                <Icon name="folder" :size="12" />
                查看文件
              </button>

              <Icon :name="expanded[c.hash] ? 'chevron-down' : 'chevron-right'" :size="14" class="tl-chevron" />
            </div>
          </button>

          <div v-if="expanded[c.hash]" class="tl-files">
            <div v-if="!c.files || c.files.length === 0" class="tl-files-empty">该提交没有文件改动</div>
            <div v-for="f in c.files || []" :key="f.status + f.path" class="tl-file">
              <span class="tl-file-sign" :class="`tf--${f.status}`">{{ statusSign(f.status) }}</span>
              <span class="tl-file-badge" :class="`tf--${f.status}`">{{ statusLabel(f.status) }}</span>
              <span class="tl-file-path" :title="f.path">{{ f.path }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.commit-page {
  padding: 20px 24px 28px;
  overflow-y: auto;
  height: 100%;
}

.page-sub {
  font-size: 12px;
  color: var(--c-text-tertiary);
  margin: 2px 0 0;
}

/* 汇总条 */
.commit-summary {
  display: flex;
  gap: 12px;
  margin-bottom: 16px;
}

.summary-item {
  display: flex;
  align-items: baseline;
  gap: 6px;
  padding: 10px 16px;
  background: var(--c-bg-panel);
  border: 1px solid var(--c-border);
  border-radius: var(--r-lg);
}

.summary-num {
  font-size: 20px;
  font-weight: 700;
  color: var(--c-brand);
  font-variant-numeric: tabular-nums;
}

.summary-label {
  font-size: 12px;
  color: var(--c-text-tertiary);
}

/* 时间线 */
.timeline {
  display: flex;
  flex-direction: column;
  gap: 0;
}

.tl-item {
  display: flex;
  gap: 14px;
}

.tl-marker {
  display: flex;
  flex-direction: column;
  align-items: center;
  width: 12px;
  flex-shrink: 0;
  padding-top: 18px;
}

.tl-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--c-brand);
  box-shadow: 0 0 0 3px var(--c-brand-soft);
  flex-shrink: 0;
}

.tl-line {
  flex: 1;
  width: 2px;
  background: var(--c-border);
  margin-top: 4px;
  min-height: 12px;
}

.tl-item:last-child .tl-line {
  display: none;
}

/* 卡片 */
.tl-card {
  flex: 1;
  min-width: 0;
  margin-bottom: 10px;
  padding: 0;
  overflow: hidden;
}

.tl-head {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
  padding: 12px 16px;
  cursor: pointer;
  text-align: left;
  background: transparent;
  border: none;
  transition: background 0.12s;
}

.tl-head:hover {
  background: var(--c-bg-hover);
}

.tl-title-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.tl-branch {
  color: var(--c-brand);
  flex-shrink: 0;
}

.tl-title {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  font-weight: 600;
  color: var(--c-text-primary);
}

.tl-meta-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--c-text-tertiary);
}

.tl-meta {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
}

.tl-author {
  color: var(--c-text-secondary);
}

.tl-hash {
  font-family: var(--font-mono, monospace);
  color: var(--c-info);
  background: var(--c-info-soft);
  padding: 1px 7px;
  border-radius: var(--r-full);
  font-size: 11px;
}

.tl-chip {
  display: inline-flex;
  align-items: center;
  padding: 1px 8px;
  border-radius: var(--r-full);
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
}

.tl-chip--added { background: var(--c-success-soft); color: var(--c-success); }
.tl-chip--modified { background: var(--c-warning-soft); color: var(--c-warning); }
.tl-chip--deleted { background: var(--c-danger-soft); color: var(--c-danger); }

.tl-chevron {
  color: var(--c-text-tertiary);
  margin-left: auto;
  flex-shrink: 0;
}

.tl-view-files {
  color: var(--c-info);
  border-color: transparent;
  padding: 2px 8px;
  font-size: 11.5px;
}

.tl-view-files:hover {
  background: var(--c-info-soft);
  color: var(--c-info);
}

/* 展开的文件明细 */
.tl-files {
  border-top: 1px solid var(--c-border);
  max-height: 320px;
  overflow-y: auto;
  padding: 6px 0;
  background: var(--c-bg-secondary);
}

.tl-files-empty {
  padding: 12px 16px;
  font-size: 12.5px;
  color: var(--c-text-tertiary);
}

.tl-file {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 16px;
  font-size: 12.5px;
  border-bottom: 1px solid var(--c-border);
}

.tl-file:last-child { border-bottom: none; }

.tl-file-sign {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  border-radius: 4px;
  font-size: 12px;
  flex-shrink: 0;
}

.tf--added { background: var(--c-success-soft); color: var(--c-success); }
.tf--modified { background: var(--c-warning-soft); color: var(--c-warning); }
.tf--deleted { background: var(--c-danger-soft); color: var(--c-danger); }

.tl-file-badge {
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 600;
}

.tl-file-path {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-secondary);
  font-family: var(--font-mono, monospace);
}
</style>