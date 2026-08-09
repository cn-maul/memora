import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { WorkspaceInfo } from '@/types'
import { getWorkspaceInfo, initWorkspace } from '@/api/client'
import type { InitRequest } from '@/types'

export const useWorkspaceStore = defineStore('workspace', () => {
  const info = ref<WorkspaceInfo | null>(null)
  const loading = ref(false)
  const error = ref('') // 请求失败时的错误信息（修复 M-05：失败不伪装成未初始化）
  const initialized = computed(() => info.value?.initialized ?? false)
  const path = computed(() => info.value?.workspacePath ?? '')

  async function fetchInfo() {
    loading.value = true
    try {
      info.value = await getWorkspaceInfo()
      error.value = ''
    } catch (e: any) {
      // 保留上一次成功的 info，仅记录错误，避免把请求失败误判为“未初始化”
      error.value = e.message || '加载工作区信息失败'
    } finally {
      loading.value = false
    }
  }

  async function init(req: InitRequest) {
    loading.value = true
    error.value = ''
    try {
      await initWorkspace(req)
      await fetchInfo()
    } catch (e: any) {
      error.value = e.message || '初始化失败'
      throw e
    } finally {
      loading.value = false
    }
  }

  return { info, loading, error, initialized, path, fetchInfo, init }
})