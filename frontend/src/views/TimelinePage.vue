<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { getCommitList } from '@/api/client'
import type { CommitItem } from '@/types'
import Icon from '@/components/Icon.vue'

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

function statusLabel(s: string) {
  const map: Record<string, string> = { added: '新增', modified: '修改', deleted: '删除' }
  return map[s] || s
}

function statusClass(s: string) {
  const map: Record<string, string> = {
    added: 'cf-a cf-added',
    modified: 'cf-m cf-modified',
    deleted: 'cf-d cf-deleted',
  }
  return map[s] || 'cf-m'
}

function statusSign(s: string) {
  const map: Record<string, string> = { added: '+', modified: '~', deleted: '−' }
  return map[s] || '·'
}

// 新增/修改/删除分组统计
function messageTitle(msg: string) {
  return (msg || '').split('\n')[0]
}
</script>

<template>
  <div class="commit-page">
    <div class="page-header">
      <div>
        <h2>提交记录</h2>
        <p class="page-sub">各个版本的提交、备注与改动文件</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-ghost btn-sm" @click="loadData">
          <Icon name="refresh" :size="14" />
          刷新
        </button>
      </div>
    </div>

    <div v-if="loadError" class="alert alert--error">{{ loadError }}</div>

    <div v-if="loading" class="loading">加载中…</div>
    <div v-else-if="commits.length === 0" class="empty-state">暂无提交记录</div>

    <div v-else class="commit-list">
      <div v-for="c in commits" :key="c.hash" class="commit-item card">
        <!-- 头部：备注 + 元信息 -->
        <button class="commit-head" @click="toggle(c.hash)">
          <span class="commit-title">{{ messageTitle(c.message) || '（无备注）' }}</span>
          <span class="commit-meta">
            <span class="commit-time">
              <Icon name="clock" :size="12" />
              {{ formatTime(c.time) }}
            </span>
            <span class="commit-hash">{{ shortHash(c.hash) }}</span>
            <Icon :name="expanded[c.hash] ? 'chevron-down' : 'chevron-right'" :size="14" class="commit-chevron" />
          </span>
        </button>

        <!-- 文件明细 -->
        <div v-if="expanded[c.hash]" class="commit-files">
          <div v-if="!c.files || c.files.length === 0" class="commit-files__empty">该提交没有文件改动</div>
          <div v-for="f in c.files || []" :key="f.status + f.path" class="commit-file">
            <span class="commit-file__sign" :class="statusClass(f.status)">{{ statusSign(f.status) }}</span>
            <span class="commit-file__badge" :class="statusClass(f.status)">{{ statusLabel(f.status) }}</span>
            <span class="commit-file__path" :title="f.path">{{ f.path }}</span>
          </div>
          <div v-if="c.files && c.files.length > 12" class="commit-files-note">
            共 {{ c.files.length }} 个文件改动
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

.header-actions {
  display: flex;
  gap: 8px;
}

.commit-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding-top: 4px;
}

.commit-item {
  overflow: hidden;
}

.commit-item:hover {
  border-color: var(--c-border-strong);
}

.commit-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
  padding: 12px 16px;
  cursor: pointer;
  transition: background 0.12s;
}

.commit-head:hover {
  background: var(--c-bg-hover);
}

.commit-title {
  flex: 1;
  min-width: 0;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  font-weight: 600;
  color: var(--c-text-primary);
}

.commit-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
  font-size: 12px;
  color: var(--c-text-tertiary);
}

.commit-time {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
}

.commit-hash {
  font-family: var(--font-mono, monospace);
  color: var(--c-info);
  background: var(--c-info-soft);
  padding: 1px 8px;
  border-radius: var(--r-full);
  font-size: 11px;
}

.commit-chevron {
  color: var(--c-text-tertiary);
  flex-shrink: 0;
}

.commit-files {
  border-top: 1px solid var(--c-border);
  max-height: 320px;
  overflow-y: auto;
}

.commit-files__empty {
  padding: 12px 16px;
  font-size: 12.5px;
  color: var(--c-text-tertiary);
}

.commit-file {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 16px;
  font-size: 12.5px;
  border-bottom: 1px solid var(--c-border);
}

.commit-file:last-child {
  border-bottom: none;
}

.commit-file:hover {
  background: var(--c-bg-hover);
}

.commit-file__sign {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  border-radius: 4px;
  font-size: 12px;
  flex-shrink: 0;
}

.commit-file__sign.cf-added { background: var(--c-success-soft); color: var(--c-success); }
.commit-file__sign.cf-modified { background: var(--c-warning-soft); color: var(--c-warning); }
.commit-file__sign.cf-deleted { background: var(--c-danger-soft); color: var(--c-danger); }

.commit-file__badge {
  flex-shrink: 0;
}

.commit-file__path {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-secondary);
  font-family: var(--font-mono, monospace);
}

.commit-files-note {
  padding: 10px 16px;
  font-size: 12px;
  color: var(--c-text-tertiary);
  text-align: center;
}
</style>