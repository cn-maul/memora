import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getSettings, updateSettings, updateSecrets } from '@/api/client'

export const useSettingsStore = defineStore('settings', () => {
  const settings = ref<Record<string, any>>({})
  const loading = ref(false)
  const error = ref('') // 加载失败时的错误（修复 M-06：失败不变成空配置）

  async function fetch() {
    loading.value = true
    try {
      settings.value = await getSettings()
      error.value = ''
    } catch (e: any) {
      // 保留上一次成功快照，仅记录错误，避免保存覆盖真实配置
      error.value = e.message || '加载设置失败'
    } finally {
      loading.value = false
    }
  }

  async function save(data: Record<string, any>): Promise<string[]> {
    loading.value = true
    try {
      const res = await updateSettings(data)
      await fetch()
      return res.restartRequired ?? []
    } finally {
      loading.value = false
    }
  }

  async function saveSecrets(llmKey?: string, embedKey?: string) {
    loading.value = true
    try {
      await updateSecrets(llmKey, embedKey)
      await fetch()
    } finally {
      loading.value = false
    }
  }

  return { settings, loading, error, fetch, save, saveSecrets }
})