import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { TagInfo, TagSuggestion } from '@/types'
import { listTags, listTagSuggestions } from '@/api/client'

export const useTagsStore = defineStore('tags', () => {
  const tags = ref<TagInfo[]>([])
  const suggestions = ref<TagSuggestion[]>([])

  async function fetchTags() {
    try {
      tags.value = (await listTags()) ?? []
    } catch {
      tags.value = []
    }
  }

  async function fetchSuggestions() {
    try {
      suggestions.value = (await listTagSuggestions()) ?? []
    } catch {
      suggestions.value = []
    }
  }

  return { tags, suggestions, fetchTags, fetchSuggestions }
})