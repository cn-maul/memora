<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { getFileHistory, downloadHistoryVersion, resolveFileId, restoreFile } from '@/api/client'
import Icon from '@/components/Icon.vue'
import { safeErrorMsg, isNotFound, baseName } from '@/utils/fileHistory'

// 统一入口：RecentFilesPage 传 FileItem，WorkspacePage 传 BrowseEntry，二者字段子集一致
export interface FileHistoryTarget {
  relPath: string
  name?: string
  isDir?: boolean
  size?: number
  mtime?: number
  docType?: string
}

interface VersionInfo {
  hash: string
  time: number
  message: string
  author: string
}

const props = defineProps<{
  file: FileHistoryTarget | null
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'navigate-file', relPath: string): void
}>()

// ── 内部状态：版本历史 + 下载 + 行内恢复确认 ──
const versions = ref<VersionInfo[]>([])
const loading = ref(false) // 版本历史加载中
const historyError = ref('') // 版本历史加载失败（非 not_found 时展示，修复：失败伪装"暂无版本"）
const fileId = ref(0)
const downloading = ref<string | null>(null)
const restoring = ref<string | null>(null)
const confirmRestore = ref<VersionInfo | null>(null)
const error = ref('') // 下载/恢复失败
const notice = ref('') // 恢复成功提示

const isDir = computed(() => !!props.file?.isDir)
const title = computed(() => (props.file ? baseName(props.file.relPath) : ''))

// ── P1-08：请求代次守卫 ──
// 每次打开弹窗递增一次代次；resolveFileId → getFileHistory 链上的异步响应
// 仅当代次仍为最新时才回写状态，防止前一个文件慢响应覆盖后打开的文件。
let requestToken = 0

function resetState() {
  versions.value = []
  loading.value = false
  historyError.value = ''
  fileId.value = 0
  downloading.value = null
  restoring.value = null
  confirmRestore.value = null
  error.value = ''
  notice.value = ''
}

watch(
  () => props.open,
  (open) => {
    if (open && props.file) {
      void loadDetail()
    } else if (!open) {
      // 关闭时作废进行中的请求（P1-08），防止慢响应回写
      requestToken++
      resetState()
    }
  },
)

async function loadDetail() {
  const f = props.file
  if (!f) return
  const token = ++requestToken
  resetState()
  loading.value = true
  try {
    const id = await resolveFileId(f.relPath)
    if (token !== requestToken) return // 已切换文件/已关闭：丢弃旧响应
    fileId.value = id
    const history = await getFileHistory(id)
    if (token !== requestToken) return
    versions.value = history.commits
      .map((c: any) => ({ hash: c.hash, time: c.time, message: c.message, author: c.author }))
      .slice(0, 30)
  } catch (err: any) {
    if (token !== requestToken) return
    if (isNotFound(err)) {
      // 文件未索引则无版本历史，属预期空态
      versions.value = []
    } else {
      // 其它失败（网络/服务异常）明确提示，不伪装成"暂无版本"
      versions.value = []
      historyError.value = err?.message || '加载版本历史失败'
    }
  } finally {
    if (token === requestToken) loading.value = false
  }
}

// 一键恢复：小白点「恢复」→ 行内确认 → 后端自动先保存当前状态再恢复
function askRestore(v: VersionInfo) {
  error.value = ''
  notice.value = ''
  confirmRestore.value = v
}

async function doRestore() {
  const v = confirmRestore.value
  if (!v || fileId.value <= 0) return
  const token = requestToken
  restoring.value = v.hash
  error.value = ''
  notice.value = ''
  try {
    await restoreFile(fileId.value, v.hash)
    if (token !== requestToken) return
    confirmRestore.value = null
    notice.value = '已恢复 ✓ 当前文件已还原为该版本（若此前有改动，已先自动保存）'
  } catch (e: any) {
    if (token !== requestToken) return
    error.value = safeErrorMsg(e, '恢复失败')
  } finally {
    if (token === requestToken) restoring.value = null
  }
}

async function downloadVersion(v: VersionInfo) {
  const f = props.file
  if (!f) return
  const token = requestToken
  downloading.value = v.hash
  try {
    const blob = await downloadHistoryVersion(f.relPath, v.hash)
    if (token !== requestToken) return
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    const name = baseName(f.relPath)
    const extIdx = name.lastIndexOf('.')
    const base = extIdx > 0 ? name.slice(0, extIdx) : name
    const ext = extIdx > 0 ? name.slice(extIdx) : ''
    a.download = `${base}-${v.hash.slice(0, 7)}${ext}`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  } catch (e: any) {
    if (token !== requestToken) return
    error.value = safeErrorMsg(e, '下载失败，请重试')
  } finally {
    if (token === requestToken) downloading.value = null
  }
}

function navigateToCurrentVersion() {
  if (props.file) {
    const rel = props.file.relPath
    emit('close')
    emit('navigate-file', rel)
  }
}

function close() {
  emit('close')
}

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
</script>

<template>
  <div v-if="open && file" class="modal-overlay" @click.self="close">
    <div class="modal modal--detail">
      <div class="modal-title">
        <Icon :name="isDir ? 'folder' : 'file'" :size="16" />
        <span class="modal-title__name" :title="file.relPath">{{ title }}</span>
      </div>
      <div class="modal-meta">
        <span class="modal-meta__item">
          <Icon name="file" :size="12" />
          {{ isDir ? '文件夹' : (file.docType || '文件') }}
        </span>
        <span v-if="!isDir && file.size !== undefined" class="modal-meta__item">
          <Icon name="file" :size="12" />
          {{ formatSize(file.size) }}
        </span>
        <span v-if="file.mtime" class="modal-meta__item">
          <Icon name="clock" :size="12" />
          {{ formatTime(file.mtime) }}
        </span>
      </div>

      <div class="detail-section">
        <div class="detail-section__head">
          <span class="detail-section__title">版本历史</span>
          <span class="detail-section__hint">每次自动提交即一个版本</span>
        </div>
        <div v-if="loading" class="loading">加载历史中…</div>
        <div v-else-if="historyError" class="alert alert--error">{{ historyError }}</div>
        <div v-else-if="versions.length === 0" class="detail-empty">暂无版本历史（该文件未发生过自动提交）</div>
        <div v-else class="version-list">
          <div v-for="(v, i) in versions" :key="v.hash" class="version-item">
            <span class="version-num">v{{ versions.length - i }}</span>
            <div class="version-info">
              <div class="version-message" :title="v.message">{{ v.message }}</div>
              <div class="version-meta">
                <span>{{ formatTime(v.time) }}</span>
                <span class="version-hash" :title="v.hash">{{ v.hash.slice(0, 7) }}</span>
              </div>
            </div>
            <div class="version-actions">
              <button class="btn btn-ghost btn-sm" @click="downloadVersion(v)" :disabled="downloading === v.hash">
                <Icon name="download" :size="13" />
                {{ downloading === v.hash ? '下载中…' : '下载' }}
              </button>
              <button class="btn btn-primary btn-sm" @click="askRestore(v)" :disabled="restoring === v.hash">
                {{ restoring === v.hash ? '恢复中…' : '恢复此版本' }}
              </button>
            </div>
            <div v-if="confirmRestore && confirmRestore.hash === v.hash" class="restore-confirm">
              <span class="restore-confirm__text">将把文件恢复为这个版本；若当前有未保存改动，会先自动保存。</span>
              <div class="restore-confirm__actions">
                <button class="btn btn-primary btn-sm" :disabled="restoring !== null" @click="doRestore">
                  {{ restoring === v.hash ? '恢复中…' : '确认恢复' }}
                </button>
                <button class="btn btn-ghost btn-sm" :disabled="restoring !== null" @click="confirmRestore = null">取消</button>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div v-if="notice" class="alert alert--success">{{ notice }}</div>
      <div v-if="error" class="alert alert--error">{{ error }}</div>

      <div class="modal-actions">
        <button class="btn btn-ghost btn-sm" @click="close">关闭</button>
        <button v-if="!isDir" class="btn btn-primary btn-sm" @click="navigateToCurrentVersion">打开当前版本</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ──────── 文件详情弹窗（版本历史 + 下载 + 恢复）──── 全局 .modal-overlay/.modal 复用 ──────── */
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

.version-actions { display: flex; gap: 6px; }

/* 行内恢复确认条 */
.restore-confirm {
  grid-column: 1 / -1;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
  padding: 8px 10px;
  border-radius: var(--r-sm);
  background: var(--c-bg-panel);
  border: 1px solid var(--c-warning);
}
.restore-confirm__text { font-size: 12px; color: var(--c-text-secondary); line-height: 1.5; }
.restore-confirm__actions { display: flex; gap: 6px; flex-shrink: 0; }
.version-num { font-size: 11px; font-weight: 700; color: var(--c-brand); background: var(--c-brand-soft); padding: 2px 6px; border-radius: var(--r-xs); }
.version-info { min-width: 0; }
.version-message { font-size: 12px; color: var(--c-text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.version-meta { display: flex; align-items: center; gap: 8px; font-size: 11px; color: var(--c-text-tertiary); margin-top: 2px; }
.version-hash { font-family: monospace; color: var(--c-text-secondary); }

.modal-actions { display: flex; justify-content: flex-end; gap: 10px; margin-top: 14px; }
</style>
