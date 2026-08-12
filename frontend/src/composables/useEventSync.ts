import { onUnmounted, ref, type Ref } from 'vue'
import { createSSEConnection, getQueueStatus } from '@/api/client'
import { useFilesStore } from '@/stores/files'
import { useTagsStore } from '@/stores/tags'
import { useWorkspaceStore } from '@/stores/workspace'

/**
 * SSE 事件同步 composable：订阅后端事件并分发到前端 stores（文件 / 标签 / 工作区 / 队列）。
 * 原逻辑位于 App.vue（Phase 4 迁出，让 App.vue 只保留应用壳）：
 *
 * - `index_progress`：全量重建进度写入 files store；单文件索引进度节流刷新文件列表
 * - `index_progress` / `files_changed` / `tag_done`：刷新标签
 * - `files_changed` / `commit_done` / `task_queue`：刷新工作区信息 + 队列状态
 *
 * 连接生命周期（P2-17 剩余）：SSE 正常时纯事件驱动，无 5 秒常驻轮询。
 * - 断线（onClose）→ connected 置 false，降级为每 15 秒全量轮询兜底
 * - 重连成功（onOpen）→ 做一次全量对账并停止轮询，connected 置 true
 * - 轮询期间收到 SSE 事件（说明连接已恢复）→ 同样停止轮询，避免双刷
 *
 * 组件卸载时自动清理（SSE 连接 + 节流 timer + 降级轮询 timer），
 * 也可以显式调用返回的 cleanup()（幂等）。
 */
export function useEventSync(opts: {
  /** 工作区信息刷新完成后的通知回调（事件驱动，不轮询） */
  onWorkspaceChanged?: () => void
} = {}): { cleanup: () => void; connected: Ref<boolean> } {
  const filesStore = useFilesStore()
  const tagsStore = useTagsStore()
  const ws = useWorkspaceStore()

  // SSE 连接状态（供 App.vue 未来展示连接状态）：true=已连接，false=断线降级轮询中。
  // 初始视为已连接（首次连接成功前不额外刷新，由 App.vue 挂载时的首次加载负责）。
  const connected = ref(true)

  // 断线降级轮询频率：SSE 正常时不存在任何轮询，仅断线后低频兜底（Phase 5 要求，不做 5 秒常驻轮询）。
  const POLL_INTERVAL_MS = 15000

  // 重建完成提示的自动清除 timer（新重建时清理，避免竞态误清新进度）
  let reindexDoneTimer: ReturnType<typeof setTimeout> | null = null
  // 单文件索引进度刷新的节流 timer
  let incrementalRefreshTimer: ReturnType<typeof setTimeout> | null = null
  // 断线降级轮询 timer
  let pollTimer: ReturnType<typeof setTimeout> | null = null

  // 队列状态仅在 SSE 事件触发时刷新（结果当前不渲染，保持事件驱动与后端同步）
  function refreshQueue() {
    getQueueStatus().catch(() => {
      // 队列接口不可用时静默忽略（如后端未就绪）
    })
  }

  // 全量对账：一次性刷新文件 / 标签 / 工作区（复用各 store 现有方法，错误由 store 内部消化）
  function fetchAll() {
    filesStore.fetch()
    tagsStore.fetchTags()
    ws.fetchInfo()
  }

  function stopPolling() {
    if (pollTimer) {
      clearTimeout(pollTimer)
      pollTimer = null
    }
  }

  function schedulePoll() {
    pollTimer = setTimeout(() => {
      pollTimer = null
      // 轮询周期内已重连（connected 被置 true / 收到 SSE 事件）：不再续轮询，避免与事件驱动双刷
      if (!connected.value) {
        fetchAll()
        schedulePoll()
      }
    }, POLL_INTERVAL_MS)
  }

  function startPolling() {
    if (pollTimer) return // 断线抖动会多次触发 onClose，已有 timer 时不再重复建
    fetchAll() // 断线瞬间先立即对账一次，让界面尽快拿到最新数据
    schedulePoll()
  }

  const closeSSE = createSSEConnection(
    (topic: string, data: any) => {
      // 轮询期间收到 SSE 事件说明连接实际已恢复：停止降级轮询避免双刷。
      // 常规路径下重连会先触发 onOpen 停轮询，此处仅为竞态兜底。
      if (pollTimer) {
        stopPolling()
        connected.value = true
      }
      if (topic === 'index_progress' && data) {
        // 仅处理 FullReindex 的事件（带 phase 字段：reset/processing/done）。
        // 单文件索引进度事件（{done:true, fileId, relPath}）无 phase，忽略以免产生幽灵进度卡片。
        if (data.phase === 'reset' || data.phase === 'processing' || data.phase === 'done') {
          filesStore.setReindexProgress({
            phase: data.phase,
            done: data.done,
            total: data.total,
            current: data.current,
          })
          // 新重建开始或完成时清理旧的自动清除 timer，避免竞态误清新进度
          if (data.phase === 'reset' || data.phase === 'done') {
            if (reindexDoneTimer) clearTimeout(reindexDoneTimer)
            reindexDoneTimer = null
          }
          if (data.phase === 'reset' || data.phase === 'done') {
            filesStore.fetch()
          }
          // done 后 2 秒自动清除进度卡片
          if (data.phase === 'done') {
            reindexDoneTimer = setTimeout(() => filesStore.setReindexProgress(null), 2000)
          }
        } else if (data.done && data.fileId) {
          // 单文件索引进度（增量/重试完成）：节流刷新列表，让 IndexPage 状态列实时更新（修复 M-4）
          if (incrementalRefreshTimer) clearTimeout(incrementalRefreshTimer)
          incrementalRefreshTimer = setTimeout(() => filesStore.fetch(), 500)
        }
      }
      if (topic === 'index_progress' || topic === 'files_changed' || topic === 'tag_done') {
        tagsStore.fetchTags()
      }
      if (topic === 'files_changed' || topic === 'commit_done' || topic === 'task_queue') {
        ws.fetchInfo()
        opts.onWorkspaceChanged?.()
        refreshQueue()
      }
    },
    {
      // 重连成功（含首次连接建立）：从离线回到在线时做一次全量对账，随后回到纯事件驱动。
      // 首次连接时 connected 仍为 true（尚未断过线），不做冗余刷新，避免与 App.vue 挂载时的首次加载重复。
      onOpen: () => {
        stopPolling()
        if (!connected.value) {
          fetchAll()
        }
        connected.value = true
      },
      // 断线：降级为低频轮询兜底（数据仍保持同步），直到重连后全量对账。
      onClose: (hadError) => {
        if (!hadError) return
        connected.value = false
        startPolling()
      },
    },
  )

  function cleanup() {
    closeSSE()
    stopPolling()
    connected.value = false
    if (reindexDoneTimer) {
      clearTimeout(reindexDoneTimer)
      reindexDoneTimer = null
    }
    if (incrementalRefreshTimer) {
      clearTimeout(incrementalRefreshTimer)
      incrementalRefreshTimer = null
    }
  }

  // 组件卸载时自动清理；显式调用 cleanup() 同样安全（幂等，重复清理无副作用）
  onUnmounted(cleanup)

  return { cleanup, connected }
}
