<script setup lang="ts">
import { onMounted, onUnmounted, ref, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useWorkspaceStore } from '@/stores/workspace'
import { useTagsStore } from '@/stores/tags'
import { useQAStore } from '@/stores/qa'
import {
  getCommitStatus,
  suggestCommitMessage,
  manualCommit,
  type CommitFileStatus,
} from '@/api/client'
import { useEventSync } from '@/composables/useEventSync'
import ChatSurface from '@/components/ChatSurface.vue'
import Icon, { type IconName } from '@/components/Icon.vue'

const router = useRouter()
const route = useRoute()
const ws = useWorkspaceStore()
const tags = useTagsStore()
const qa = useQAStore()

// 设置页自带完整侧边栏，隐藏全局壳的两侧面板以腾出空间
const hideSidePanels = computed(() => route.path === '/settings')
// 右侧对话区只在主页（最近文件 /files）显示：
// 问答/统计/提交记录/索引等页面有自己的主内容区，右侧对话会让布局拥挤
// （需求：右侧边栏对话只在主页全部文件显示）
const hideChatPanel = computed(() => route.path !== '/files')

// 主导航（S2 收敛）：只放小白最常用的三个，其余收进「更多」
const mainNavItems: { path: string; label: string; icon: IconName }[] = [
  { path: '/files', label: '文件', icon: 'folder' },
  { path: '/qa', label: '问问题', icon: 'chat' },
  { path: '/timeline', label: '版本记录', icon: 'git-branch' },
]
const moreNavItems: { path: string; label: string; icon: IconName }[] = [
  { path: '/workspace', label: '全部文件', icon: 'folder-open' },
  { path: '/index', label: '搜索', icon: 'search' },
  { path: '/stats', label: '统计', icon: 'chart' },
  { path: '/settings', label: '设置', icon: 'settings' },
]
const moreOpen = ref(false)
const vcGuideVisible = ref(false)

function gitDirtySum(): number {
  if (!ws.info?.dirtyCounts) return 0
  return (
    (ws.info.dirtyCounts.modified || 0) +
    (ws.info.dirtyCounts.untracked || 0) +
    (ws.info.dirtyCounts.deleted || 0)
  )
}

function hasUncommitted(): boolean {
  return gitDirtySum() > 0
}
function codeLabel(code: string): string {
  const map: Record<string, string> = { M: '修改', A: '新增', D: '删除', '??': '未跟踪' }
  return map[code] || code
}
function codeClass(code: string): string {
  const map: Record<string, string> = { M: 'cm-m', A: 'cm-a', D: 'cm-d', '??': 'cm-u' }
  return map[code] || 'cm-m'
}
function codeIconName(code: string): 'plus' | 'x' | 'check' | 'refresh' {
  if (code === 'A' || code === '??') return 'plus'
  if (code === 'D') return 'x'
  return 'refresh'
}

// ──────── 手动提交对话框 ────────
const commitDialogOpen = ref(false)
const commitMessage = ref('')
const commitFiles = ref<CommitFileStatus[]>([])
const commitLoading = ref(false)
const commitSubmitting = ref(false)
const commitSuggesting = ref(false)
const commitError = ref('')
const commitSuccess = ref('')

async function openCommitDialog() {
  commitDialogOpen.value = true
  commitMessage.value = ''
  commitError.value = ''
  commitSuccess.value = ''
  await loadCommitStatus()
}

function closeCommitDialog() {
  commitDialogOpen.value = false
}

async function loadCommitStatus() {
  commitLoading.value = true
  commitError.value = ''
  try {
    const res = await getCommitStatus()
    commitFiles.value = res.files || []
  } catch (e: any) {
    commitFiles.value = []
    commitError.value = e.message || '获取变更状态失败'
  } finally {
    commitLoading.value = false
  }
}

async function handleSuggestMessage() {
  commitSuggesting.value = true
  commitError.value = ''
  commitSuccess.value = ''
  try {
    const suggestion = await suggestCommitMessage()
    commitMessage.value = suggestion
    commitSuccess.value = '已生成 AI 建议，可直接使用或修改'
  } catch (e: any) {
    commitError.value = e.message || 'AI 生成备注失败'
  } finally {
    commitSuggesting.value = false
  }
}

async function handleManualCommit() {
  const msg = commitMessage.value.trim()
  // 空备注也允许提交：小白写不出备注也能保存（后端自动生成统计备注）
  commitSubmitting.value = true
  commitError.value = ''
  commitSuccess.value = ''
  try {
    const hash = await manualCommit(msg)
    commitSuccess.value = hash ? '已保存 ✓' : '没有需要保存的改动'
    commitDialogOpen.value = false
    await ws.fetchInfo()
  } catch (e: any) {
    commitError.value = e.message || '保存失败'
  } finally {
    commitSubmitting.value = false
  }
}

// ──────── 侧栏折叠 ────────
const sidebarCollapsed = ref(false)
const sidebarEffectiveWidth = computed(() => (sidebarCollapsed.value ? 48 : sidebarWidth.value))
function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
}
const sidebarRef = ref<HTMLElement | null>(null)
const chatRef = ref<HTMLElement | null>(null)
const sidebarWidth = ref(260)
const chatWidth = ref(320)
const dragging = ref<'sidebar' | 'chat' | null>(null)

function startDrag(target: 'sidebar' | 'chat', e: MouseEvent) {
  const el = (target === 'sidebar' ? sidebarRef.value : chatRef.value) as HTMLElement | null
  if (!el) return
  const sideEl = el
  dragging.value = target
  document.body.style.cursor = 'col-resize'
  e.preventDefault()

  function onMove(ev: MouseEvent) {
    if (target === 'sidebar') {
      const w =
        ev.clientX - sideEl.getBoundingClientRect().left + sidebarWidth.value - sideEl.offsetWidth
      if (w >= 180 && w <= 420) sidebarWidth.value = w
    } else {
      const w = window.innerWidth - ev.clientX
      if (w >= 280 && w <= 560) chatWidth.value = w
    }
  }

  function onUp() {
    dragging.value = null
    document.body.style.cursor = ''
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
  }

  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
}

// ── 对话发送 ────────
// 侧栏聊天统一收敛到 ChatSurface 组件渲染；此处仅转发用户输入给 qa store。
function handleChatSend(text: string) {
  if (!text.trim() || qa.sending) return
  // 侧栏始终按全局问答发送：若当前恢复的会话是 file 模式，先新建会话，
  // 避免把全局消息写进文件问答会话（review 发现：复用 sessionId 会混合模式）
  let sessionId: number | undefined = qa.currentSessionId ?? undefined
  if (sessionId !== undefined) {
    const cur = qa.sessions.find((s) => s.id === sessionId)
    if (cur && cur.mode === 'file') sessionId = undefined
  }
  qa.send({ question: text, mode: 'global', sessionId })
}

// ──────── 明暗模式 ────────
const theme = ref<'dark' | 'light'>(localStorage.getItem('memora-theme') as 'dark' | 'light' || 'dark')

function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  localStorage.setItem('memora-theme', theme.value)
  applyTheme(theme.value)
}

function applyTheme(t: 'dark' | 'light') {
  document.documentElement.setAttribute('data-theme', t)
}

let eventSyncCleanup: (() => void) | null = null

onMounted(async () => {
  applyTheme(theme.value)
  // SSE 事件同步（文件/标签/工作区/队列）：须在第一个 await 前同步调用，
  // 确保 composable 内部注册的 onUnmounted 能绑定到当前组件实例
  eventSyncCleanup = useEventSync().cleanup
  await ws.fetchInfo()
// 自动恢复上一次 AI 对话到侧栏聊天（需求：再次打开软件显示上次会话，而不是空的新对话）
	qa.restoreLastSession().then(() => {
		// 侧栏聊天由 ChatSurface 渲染，恢复完成后由其内部逻辑滚动到最新对话
	}).catch(() => {})
  tags.fetchTags()

  // 首次进入且已有版本记录：一次性提示「自动记录版本」的心智模型
  if (ws.initialized && ws.info?.head?.hasCommits) {
    let guideShown = false
    try {
      guideShown = localStorage.getItem('memora.vcGuideShown') === '1'
    } catch {
      // 隐私模式等场景忽略
    }
    if (!guideShown) {
      vcGuideVisible.value = true
      try {
        localStorage.setItem('memora.vcGuideShown', '1')
      } catch {
        // 忽略写入失败
      }
    }
  }
})

onUnmounted(() => {
  eventSyncCleanup?.()
})
</script>

<template>
  <div class="app">
    <!-- 左侧栏 -->
    <aside
      v-show="!hideSidePanels"
      ref="sidebarRef"
      class="sidebar"
      :style="{ width: sidebarEffectiveWidth + 'px' }"
    >
      <div class="sidebar-top">
        <button title="菜单" @click="toggleSidebar">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M3 12h18M3 6h18M3 18h18"/></svg>
        </button>
      </div>

      <nav class="nav">
        <router-link
          v-for="item in mainNavItems"
          :key="item.path"
          :to="item.path"
          class="nav-item"
          :class="{ 'nav-item--collapsed': sidebarCollapsed }"
          :title="sidebarCollapsed ? item.label : undefined"
          active-class="nav-item--active"
        >
          <Icon :name="item.icon" :size="16" />
          <span v-show="!sidebarCollapsed" class="nav-label">{{ item.label }}</span>
        </router-link>

        <!-- 更多（收起的低频页面） -->
        <button
          class="nav-item nav-item--more"
          :class="{ 'nav-item--collapsed': sidebarCollapsed, 'nav-item--active': moreOpen }"
          :title="sidebarCollapsed ? '更多' : undefined"
          @click="moreOpen = !moreOpen"
        >
          <Icon name="chevron-down" :size="16" />
          <span v-show="!sidebarCollapsed" class="nav-label">更多</span>
        </button>
        <div v-show="moreOpen && !sidebarCollapsed" class="more-menu">
          <router-link
            v-for="item in moreNavItems"
            :key="item.path"
            :to="item.path"
            class="nav-item nav-item--more-link"
            active-class="nav-item--active"
            @click="moreOpen = false"
          >
            <Icon :name="item.icon" :size="16" />
            <span class="nav-label">{{ item.label }}</span>
          </router-link>
        </div>
      </nav>

      <div class="git-section" v-show="!sidebarCollapsed">
        <div class="git-label">自动保存</div>

        <!-- 通俗状态：小白只看「待保存 / 已保存」 -->
        <div class="git-state-row" v-if="ws.initialized">
          <span class="git-state-dot" :class="{ 'git-state-dot--dirty': hasUncommitted() }"></span>
          <span class="git-state-text">
            {{
              hasUncommitted()
                ? `${gitDirtySum()} 个文件有改动，稍后自动保存`
                : ws.info?.head?.hasCommits
                  ? '已自动保存'
                  : '改动会自动保存成版本'
            }}
          </span>
        </div>

        <button
          v-if="hasUncommitted()"
          class="git-save-quick"
          @click="openCommitDialog()"
        >
          保存改动
        </button>
      </div>

      <div class="sidebar-footer">
        <button @click="router.push('/settings')" title="设置">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3"/><path d="M1 14h6M9 8h6M17 16h6"/></svg>
        </button>
        <button @click="toggleTheme" :title="theme === 'dark' ? '切换浅色模式' : '切换深色模式'" class="theme-toggle">
          <svg v-if="theme === 'dark'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="5"/><path d="M12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4"/></svg>
          <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
        </button>
      </div>
    </aside>

    <div
      v-show="!hideSidePanels"
      class="drag-handle"
      :class="{ dragging: dragging === 'sidebar' }"
      @mousedown="startDrag('sidebar', $event)"
    ></div>

    <!-- 路由出口 -->
    <main class="app-main">
      <router-view />
    </main>

    <!-- 主区与右侧对话之间的拖拽分割条 -->
    <div
      v-show="!hideSidePanels && !hideChatPanel"
      class="drag-handle chat-handle"
      :class="{ dragging: dragging === 'chat' }"
      @mousedown="startDrag('chat', $event)"
    ></div>

    <!-- 右侧：对话区（仅主页 /files 显示） -->
    <aside
      v-show="!hideSidePanels && !hideChatPanel"
      ref="chatRef"
      class="chat-panel"
      :style="{ width: chatWidth + 'px' }"
    >
      <template v-if="!hideChatPanel">
        <ChatSurface
          :messages="qa.messages as any[]"
          :sending="qa.sending"
          :thinking="qa.thinking"
          @send="handleChatSend"
          @cancel="qa.cancel"
        />
      </template>
    </aside>

    <!-- 手动提交对话框（小白友好：备注可选，留空也能保存） -->
    <div v-if="commitDialogOpen" class="commit-mask" @click.self="closeCommitDialog">
      <div class="commit-dialog card">
        <div class="commit-dialog__head">
          <div class="commit-dialog__title">
            <Icon name="git-branch" :size="16" />
            <span>保存当前改动</span>
          </div>
          <button class="commit-dialog__close" title="关闭" @click="closeCommitDialog">
            <Icon name="x" :size="16" />
          </button>
        </div>

        <div v-if="commitLoading" class="loading">加载变更状态…</div>
        <template v-else>
          <div v-if="commitFiles.length === 0 && !commitError" class="commit-dialog__empty">
            当前没有需要保存的改动
          </div>

          <div v-if="commitFiles.length" class="commit-dialog__files">
            <div v-for="f in commitFiles" :key="f.relPath" class="commit-file">
              <span class="commit-file__badge" :class="codeClass(f.code)">
                <Icon :name="codeIconName(f.code)" :size="11" />
                {{ codeLabel(f.code) }}
              </span>
              <span class="commit-file__path" :title="f.relPath">{{ f.relPath }}</span>
            </div>
          </div>

          <div v-if="commitError" class="alert alert--error commit-dialog__error">{{ commitError }}</div>
          <div v-if="commitSuccess" class="alert alert--success commit-dialog__success">{{ commitSuccess }}</div>

          <label class="commit-dialog__label">给这个版本写句说明（可不填）</label>
          <textarea
            v-model="commitMessage"
            class="input textarea commit-dialog__message"
            placeholder="例如：更新了预算表的 3 月数据…（不填也能保存）"
            rows="3"
          ></textarea>

          <div class="commit-dialog__actions">
            <button
              class="btn btn-ghost btn-sm"
              :disabled="commitSuggesting"
              @click="handleSuggestMessage"
            >
              <Icon name="memory" :size="13" />
              {{ commitSuggesting ? '生成中…' : '帮我写说明' }}
            </button>
            <div class="commit-dialog__actions-right">
              <button class="btn btn-ghost btn-sm" @click="closeCommitDialog">取消</button>
              <button
                class="btn btn-primary btn-sm"
                :disabled="commitSubmitting || commitFiles.length === 0"
                @click="handleManualCommit"
              >
                {{ commitSubmitting ? '保存中…' : '保存' }}
              </button>
            </div>
          </div>
        </template>
      </div>
    </div>

    <!-- 首次使用引导 toast：自动记录版本 -->
    <div v-if="vcGuideVisible" class="vc-guide-toast">
      <span class="vc-guide-toast__icon">🕰️</span>
      <div class="vc-guide-toast__body">
        <span class="vc-guide-toast__title">你的文件会自动记录版本</span>
        <span class="vc-guide-toast__text">
          修改或删除文件后，稍等片刻系统会自动保存一个版本。想找回旧内容，随时在左侧「版本记录」或文件详情里点「恢复此版本」。
        </span>
      </div>
      <button class="vc-guide-toast__close" title="知道了" @click="vcGuideVisible = false">×</button>
    </div>
  </div>
</template>

<style scoped>
.app {
  width: 100vw;
  height: 100vh;
  display: flex;
  overflow: hidden;
}

/* 拖动分隔条 */
.drag-handle {
  width: 4px;
  flex-shrink: 0;
  cursor: col-resize;
  background: var(--c-bg-elevated);
  position: relative;
  z-index: 5;
  transition: background 0.15s;
}
.drag-handle:hover,
.drag-handle.dragging {
  background: var(--c-brand-border);
}
.drag-handle::after {
  content: "";
  position: absolute;
  inset: 0 -3px;
  cursor: col-resize;
}
.drag-handle.dragging::after {
  position: fixed;
  z-index: 999;
}

/* ── 路由主区 ── */
.app-main {
  flex: 1;
  min-width: 0;
  height: 100%;
  overflow: hidden;
  background: var(--c-bg-page);
  display: flex;
  flex-direction: column;
}
.app-main > :deep(.settings-layout),
.app-main > :deep(.workspace-page),
.app-main > :deep(.index-page),
.app-main > :deep(.timeline-page),
.app-main > :deep(.qa-page),
.app-main > :deep(.stats-page) {
  flex: 1;
  min-height: 0;
}

/* ── 左侧栏 ── */
.sidebar {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  background: var(--c-bg-page);
  border-right: 1px solid var(--c-border);
}
.sidebar-top {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 10px 12px;
  height: 42px;
}
.sidebar-top button {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: var(--r-sm);
  color: var(--c-icon-secondary);
  border: none;
  background: transparent;
  cursor: pointer;
  padding: 0;
  margin: 0;
  line-height: 1;
  transition: background 0.1s;
}
.sidebar-top button:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-secondary);
}
.sidebar-top button svg {
  width: 16px;
  height: 16px;
  display: block;
}

/* 导航 */
.nav {
  padding: 4px 8px;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 7px 10px;
  border-radius: var(--r-md);
  color: var(--c-text-secondary);
  font-size: 13px;
  cursor: pointer;
  text-decoration: none;
  transition: background 0.1s, color 0.1s;
}
.nav-item:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}
.nav-item svg {
  color: var(--c-icon-secondary);
  flex-shrink: 0;
}
/* 折叠态：仅居中显示图标，仍可点击切换页面 */
.nav-item--collapsed {
  justify-content: center;
  padding: 7px 0;
}
.nav-item--active {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}
.nav-item--active svg {
  color: var(--c-text-secondary);
}
.nav-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ── 「更多」折叠菜单（S2）── */
.nav-item--more {
  background: none;
  border: none;
  width: 100%;
}
.more-menu {
  margin: 2px 0 4px 8px;
  padding-left: 8px;
  border-left: 1px solid var(--c-border);
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.nav-item--more-link {
  font-size: 12.5px;
  padding: 6px 10px;
}

/* ── 版本控制 ── */
.git-section {
  margin-top: 8px;
  padding: 0 6px;
}
.git-label {
  font-size: 11px;
  color: var(--c-text-tertiary);
  padding: 4px 10px 6px;
  letter-spacing: 0.5px;
}
/* 通俗状态行：待保存 / 已保存 */
.git-state-row {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 6px 10px;
  border-radius: var(--r-sm);
  background: var(--c-bg-panel);
  border: 1px solid var(--c-border);
  margin-bottom: 4px;
}
.git-state-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--c-success);
  flex-shrink: 0;
}
.git-state-dot--dirty {
  background: var(--c-warning);
}
.git-state-text {
  font-size: 12px;
  color: var(--c-text-secondary);
  line-height: 1.4;
}
/* 保存快捷按钮（仅在有改动时出现） */
.git-save-quick {
  margin: 2px 10px;
  padding: 6px 10px;
  border-radius: var(--r-sm);
  border: none;
  background: var(--c-accent);
  color: #fff;
  font-size: 12.5px;
  cursor: pointer;
  transition: opacity 0.1s;
}
.git-save-quick:hover {
  opacity: 0.9;
}
/* 高级折叠区：分支 / HEAD 等专业信息 */
.git-advanced {
  margin: 2px 0 0;
}
.git-advanced__summary {
  cursor: pointer;
  user-select: none;
  font-size: 11px;
  color: var(--c-text-tertiary);
  padding: 4px 10px;
}
.git-advanced__summary:hover {
  color: var(--c-text-secondary);
}
.git-advanced__body {
  padding-bottom: 4px;
}
.git-branch-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  border-radius: var(--r-sm);
  color: var(--c-text-secondary);
  font-size: 12.5px;
}
.git-branch-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.git-head-row {
  padding: 3px 10px 1px;
}

.git-head {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 7px;
  border-radius: var(--r-xs);
  background: var(--c-bg-elevated);
  font-size: 10.5px;
  color: var(--c-text-tertiary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.git-head__label {
  color: var(--c-text-tertiary);
  flex-shrink: 0;
}

.git-head__hash {
  color: var(--c-brand);
  font-family: Consolas, monospace;
  font-weight: 600;
  letter-spacing: -0.2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.git-head__count {
  color: var(--c-text-secondary);
  font-weight: 500;
  white-space: nowrap;
}

.git-head__sep {
  color: var(--c-text-tertiary);
  flex-shrink: 0;
}

.git-status-row {
  display: flex;
  flex-wrap: nowrap;
  gap: 4px;
  padding: 3px 10px 1px;
}
.git-status-item {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 1px 4px;
  border-radius: var(--r-xs);
  background: var(--c-bg-elevated);
  font-size: 10.5px;
  color: var(--c-text-tertiary);
  flex-shrink: 0;
  white-space: nowrap;
}
.git-status-num {
  font-weight: 600;
  color: var(--c-text-secondary);
  font-variant-numeric: tabular-nums;
}
.git-status-text {
  color: var(--c-icon-secondary);
  font-size: 10px;
  white-space: nowrap;
}
.git-status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: transparent;
  flex-shrink: 0;
}
.git-status-dot.dot--modified {
  background: var(--c-warning);
  box-shadow: 0 0 0 2px var(--c-warning-soft);
}
.git-status-dot.dot--untracked {
  background: var(--c-warning);
  box-shadow: 0 0 0 2px var(--c-warning-soft);
}
.git-status-dot.dot--deleted {
  background: var(--c-danger);
  box-shadow: 0 0 0 2px var(--c-danger-soft);
}
.git-btn-row {
  display: flex;
  gap: 4px;
  padding: 4px 10px 2px;
}

.git-commit-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 3px 6px;
  border-radius: var(--r-xs);
  font-size: 10.5px;
  font-weight: 600;
  cursor: pointer;
  transition: background 0.1s, color 0.1s;
  white-space: nowrap;
  line-height: 1.2;
}

.git-commit-btn--auto {
  background: var(--c-brand-soft);
  color: var(--c-brand);
}
.git-commit-btn--auto:hover:not(:disabled) {
  background: var(--c-brand-border);
}

.git-commit-btn--manual {
  background: var(--c-bg-hover);
  color: var(--c-text-secondary);
}
.git-commit-btn--manual:hover:not(:disabled) {
  background: var(--c-bg-active);
  color: var(--c-text-secondary);
}

.git-commit-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.git-commit-btn svg {
  width: 12px;
  height: 12px;
  flex-shrink: 0;
}

.git-result {
  padding: 3px 10px 2px;
  font-size: 10.5px;
  color: var(--c-icon-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.git-result--success {
  color: var(--c-brand);
}

/* ── 手动提交对话框 ── */
.commit-mask {
  position: fixed;
  inset: 0;
  z-index: 100;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.5);
  backdrop-filter: blur(2px);
}

.commit-dialog {
  width: 440px;
  max-width: calc(100vw - 40px);
  max-height: 70vh;
  overflow-y: auto;
  padding: 18px 20px;
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.5);
}

.commit-dialog__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}

.commit-dialog__title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 15px;
  font-weight: 700;
  color: var(--c-text-primary);
}

.commit-dialog__title :deep(svg) {
  color: var(--c-brand);
}

.commit-dialog__close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: var(--r-md);
  color: var(--c-text-tertiary);
  transition: background 0.1s, color 0.1s;
}
.commit-dialog__close:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}

.commit-dialog__empty {
  padding: 14px;
  border: 1px dashed var(--c-border-strong);
  border-radius: var(--r-md);
  color: var(--c-text-secondary);
  font-size: 13px;
  text-align: center;
  margin-bottom: 12px;
}

.commit-dialog__files {
  max-height: 180px;
  overflow-y: auto;
  border: 1px solid var(--c-border);
  border-radius: var(--r-md);
  background: var(--c-bg-secondary);
  padding: 8px;
  margin-bottom: 12px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.commit-file {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
}

.commit-file__badge {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: var(--r-full);
  font-size: 11px;
  font-weight: 600;
}
.commit-file__badge.cm-m { background: var(--c-warning-soft); color: var(--c-warning); }
.commit-file__badge.cm-a { background: var(--c-success-soft); color: var(--c-success); }
.commit-file__badge.cm-d { background: var(--c-danger-soft); color: var(--c-danger); }
.commit-file__badge.cm-u { background: var(--c-info-soft); color: var(--c-info); }

.commit-file__path {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--c-text-secondary);
}

.commit-dialog__error,
.commit-dialog__success {
  margin-bottom: 12px;
}

.commit-dialog__label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: var(--c-text-secondary);
  margin-bottom: 6px;
}

.commit-dialog__message {
  width: 100%;
  resize: vertical;
  min-height: 64px;
  margin-bottom: 14px;
  font-family: inherit;
}

.commit-dialog__actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.commit-dialog__actions-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.sidebar-footer {
  margin-top: auto;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 14px;
  border-top: 1px solid var(--c-border);
}
.sidebar-footer button {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--c-icon-secondary);
  border: none;
  background: transparent;
  cursor: pointer;
  padding: 5px 8px;
  border-radius: var(--r-sm);
  font-size: 12.5px;
  line-height: 1.2;
  margin: 0;
  transition: background 0.1s;
}
.sidebar-footer button:hover {
  background: var(--c-bg-hover);
  color: var(--c-text-secondary);
}
.sidebar-footer button svg {
  width: 15px;
  height: 15px;
  display: block;
}

/* ── 右侧对话区 ── */
.chat-panel {
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  background: var(--c-bg-page);
  position: relative;
  overflow: hidden;
}

/* ── 首次使用引导 toast ── */
.vc-guide-toast {
  position: fixed;
  right: 20px;
  bottom: 20px;
  z-index: 1000;
  display: flex;
  align-items: flex-start;
  gap: 10px;
  max-width: 380px;
  padding: 12px 14px;
  border-radius: var(--r-lg);
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-brand-border);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.18);
  animation: guide-in 0.25s ease-out;
}
@keyframes guide-in {
  from { opacity: 0; transform: translateY(8px); }
  to { opacity: 1; transform: none; }
}
.vc-guide-toast__icon {
  font-size: 20px;
  line-height: 1;
  flex-shrink: 0;
}
.vc-guide-toast__body {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.vc-guide-toast__title {
  font-size: 13px;
  font-weight: 600;
  color: var(--c-text-primary);
}
.vc-guide-toast__text {
  font-size: 12px;
  line-height: 1.6;
  color: var(--c-text-secondary);
}
.vc-guide-toast__close {
  border: none;
  background: none;
  color: var(--c-text-tertiary);
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
  padding: 0 2px;
  flex-shrink: 0;
}
.vc-guide-toast__close:hover {
  color: var(--c-text-primary);
}
</style>