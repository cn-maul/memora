import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { TagInfo, TagSuggestion } from '@/types'
import { listTags, listTagSuggestions } from '@/api/client'

export const useTagsStore = defineStore('tags', () => {
  const tags = ref<TagInfo[]>([])
  const suggestions = ref<TagSuggestion[]>([])
  const error = ref('') // 加载标签/建议失败时的错误（修复：失败不再静默伪装成"没有标签"）

  async function fetchTags() {
    try {
      tags.value = (await listTags()) ?? []
      error.value = ''
    } catch (e: any) {
      error.value = e?.message || '加载标签失败'
    }
  }

  async function fetchSuggestions() {
    try {
      suggestions.value = (await listTagSuggestions()) ?? []
      error.value = ''
    } catch (e: any) {
      error.value = e?.message || '加载标签建议失败'
    }
  }

  return { tags, suggestions, error, fetchTags, fetchSuggestions }
})