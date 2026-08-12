import { onUnmounted } from 'vue'
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
 * 组件卸载时自动清理（SSE 连接 + 节流 timer），也可以显式调用返回的 cleanup()（幂等）。
 * 说明：队列状态只随 SSE 事件刷新（事件驱动），P2-17 已删除原来的 5 秒 setInterval 轮询。
 */
export function useEventSync(opts: {
  /** 工作区信息刷新完成后的通知回调（事件驱动，不轮询） */
  onWorkspaceChanged?: () => void
} = {}): { cleanup: () => void } {
  const filesStore = useFilesStore()
  const tagsStore = useTagsStore()
  const ws = useWorkspaceStore()

  // 重建完成提示的自动清除 timer（新重建时清理，避免竞态误清新进度）
  let reindexDoneTimer: ReturnType<typeof setTimeout> | null = null
  // 单文件索引进度刷新的节流 timer
  let incrementalRefreshTimer: ReturnType<typeof setTimeout> | null = null

  // 队列状态仅在 SSE 事件触发时刷新（结果当前不渲染，保持事件驱动与后端同步）
  function refreshQueue() {
    getQueueStatus().catch(() => {
      // 队列接口不可用时静默忽略（如后端未就绪）
    })
  }

  const closeSSE = createSSEConnection((topic: string, data: any) => {
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
  })

  function cleanup() {
    closeSSE()
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

  return { cleanup }
}
