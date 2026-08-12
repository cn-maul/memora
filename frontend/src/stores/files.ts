import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { FileItem } from '@/types'
import { listFiles } from '@/api/client'

export const useFilesStore = defineStore('files', () => {
  const items = ref<FileItem[]>([])
  const total = ref(0)
  const page = ref(0)
  const pageSize = ref(50)
  const statusFilter = ref('')
  const tagFilter = ref('')
  const loading = ref(false)
  const error = ref('')

  // 表头点击排序：field + 方向。点击同列循环 asc → desc → 默认（time:desc）
  type SortField = 'name' | 'type' | 'status' | 'time'
  const sortField = ref<SortField>('time')
  const sortDir = ref<'asc' | 'desc'>('desc')

  // 视图查询隔离（P2-19）：latest-request-wins + AbortController。
  // 同一 items 列表被 QA 文件选择、索引页表格、筛选/排序和 SSE 刷新共同驱动，
  // 并发请求下旧请求若晚于新请求返回会覆盖新结果（竞态）。
  // reqSeq 只允许"最新一次请求"写入结果；新请求发出时同时 abort 旧请求。
  let reqSeq = 0
  let activeCtrl: AbortController | null = null

  function cycleSort(field: SortField) {
    if (sortField.value === field) {
      if (sortDir.value === 'desc') sortDir.value = 'asc'
      else {
        sortField.value = 'time'
        sortDir.value = 'desc'
      }
    } else {
      sortField.value = field
      sortDir.value = 'desc'
    }
    fetch({ page: 0 }) // 排序变化回到第一页，避免停留在不足一页的旧页码
  }

  function sortParam(): string {
    return `${sortField.value}:${sortDir.value}`
  }

  // 全量重建索引进度（由 App.vue 的 SSE 回调更新，IndexPage 展示）
  const reindexProgress = ref<{ phase: string; done: number; total: number; current: string } | null>(null)

  function setReindexProgress(p: { phase: string; done?: number; total?: number; current?: string } | null) {
    if (!p) {
      reindexProgress.value = null
      return
    }
    reindexProgress.value = {
      phase: p.phase,
      done: p.done ?? reindexProgress.value?.done ?? 0,
      total: p.total ?? reindexProgress.value?.total ?? 0,
      current: p.current ?? '',
    }
  }

  async function fetch(params?: { status?: string; tag?: string; page?: number; pageSize?: number; sort?: string }) {
    const seq = ++reqSeq
    // 新请求取代旧请求：先中止旧请求，避免其残留响应继续占用连接/干扰状态
    activeCtrl?.abort()
    const ctrl = new AbortController()
    activeCtrl = ctrl
    loading.value = true
    error.value = ''
    try {
      const res = await listFiles(
        {
          status: params?.status ?? statusFilter.value,
          tag: params?.tag ?? tagFilter.value,
          page: params?.page ?? page.value,
          pageSize: params?.pageSize ?? pageSize.value,
          sort: params?.sort ?? sortParam(),
        },
        { signal: ctrl.signal },
      )
      if (seq !== reqSeq) return // 已有更新的请求：丢弃本次结果（latest-request-wins）
      items.value = res.items ?? []
      total.value = res.total ?? 0
      page.value = res.page ?? 0
    } catch (e: any) {
      if (seq !== reqSeq) return // 过期请求的错误同样丢弃，避免旧错误污染新请求状态
      if (e?.code === 'canceled' || e?.code === 'ERR_CANCELED') return // 被新请求取消，非真实失败
      // 加载失败：记录错误供页面区分"无数据"与"加载失败"，避免误导为空列表
      error.value = e?.message || '加载文件列表失败'
    } finally {
      // 仅最新请求负责收尾：旧请求不得覆盖新请求的 loading 状态
      if (seq === reqSeq) {
        activeCtrl = null
        loading.value = false
      }
    }
  }

  function nextPage() {
    fetch({ page: page.value + 1, pageSize: pageSize.value })
  }

  function prevPage() {
    if (page.value > 0) fetch({ page: page.value - 1, pageSize: pageSize.value })
  }

  return { items, total, page, pageSize, statusFilter, tagFilter, loading, error, sortField, sortDir, sortParam, cycleSort, nextPage, prevPage, reindexProgress, setReindexProgress, fetch }
})