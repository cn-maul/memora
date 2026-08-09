<script setup lang="ts">
import { computed } from 'vue'
import Icon from '@/components/Icon.vue'
import type { IconName } from '@/components/Icon.vue'

export interface TreeNode {
  name: string
  relPath: string
  isDir?: boolean
  docType?: string
  expanded: boolean
  loading: boolean
  hasLoaded: boolean
  children: TreeNode[]
}

const props = withDefaults(defineProps<{
  node: TreeNode
  depth: number
  searchQuery?: string
  currentPath?: string
}>(), { searchQuery: '', currentPath: '' })

const emit = defineEmits<{
  (e: 'load', node: TreeNode): void
  (e: 'open-file', path: string): void
  (e: 'navigate', path: string): void
}>()

async function toggle() {
  if (!props.node.isDir) {
    emit('open-file', props.node.relPath)
    emit('navigate', props.node.relPath)
    return
  }
  if (!props.node.hasLoaded && !props.node.loading) {
    emit('load', props.node)
  }
  props.node.expanded = !props.node.expanded
  emit('navigate', props.node.relPath === '/' ? '' : props.node.relPath)
}

const icon = computed<IconName>(() => {
  if (props.node.isDir) return props.node.expanded ? 'folder-open' : 'folder'
  const ext = props.node.name.split('.').pop()?.toLowerCase() || ''
  const map: Record<string, IconName> = {
    go: 'go', mod: 'go', sum: 'go',
    json: 'json',
    db: 'db',
    zip: 'archive', tar: 'archive', gz: 'archive',
    png: 'image', jpg: 'image', jpeg: 'image', gif: 'image', svg: 'image', webp: 'image',
    md: 'file', txt: 'file', doc: 'file', docx: 'file',
    bat: 'archive', sh: 'archive',
    ts: 'go', js: 'go', vue: 'go',
    css: 'json', html: 'json',
  }
  return map[ext] || 'file'
})

const iconColor = computed(() => {
  if (props.node.isDir) return undefined
  const ext = props.node.name.split('.').pop()?.toLowerCase() || ''
  const colorMap: Record<string, string> = {
    go: '#00add8',
    mod: '#00add8',
    sum: '#00add8',
    json: '#f5a623',
    db: '#f5c542',
    md: '#519aba',
    bat: '#e06c75',
    png: '#c678dd',
    jpg: '#c678dd',
    jpeg: '#c678dd',
    gif: '#c678dd',
    svg: '#c678dd',
    webp: '#c678dd',
    ts: '#3178c6',
    js: '#f7df1e',
    vue: '#42b883',
    zip: '#e06c75',
    tar: '#e06c75',
    gz: '#e06c75',
    sh: '#e06c75',
  }
  return colorMap[ext] || '#888'
})

const match = computed(() => {
  if (!props.searchQuery) return false
  return props.node.name.toLowerCase().includes(props.searchQuery.toLowerCase())
})

const isActive = computed(() => props.currentPath === props.node.relPath)
const indent = computed(() => `${8 + props.depth * 18}px`)
</script>

<template>
  <div>
    <div
      class="tree-row"
      :class="{ 'tree-row--match': match, 'tree-row--dir': node.isDir, 'tree-row--active': isActive }"
      :style="{ paddingLeft: indent }"
      @click="toggle"
    >
      <span v-if="node.isDir" class="tree-caret">
        <Icon :name="node.expanded ? 'chevron-down' : 'chevron-right'" :size="12" />
      </span>
      <span v-else class="tree-caret tree-caret--file"></span>

      <span class="tree-icon">
        <Icon :name="icon" :size="14" :color="iconColor" />
      </span>

      <span class="tree-label">{{ node.name }}</span>

      <span v-if="node.loading" class="tree-spinner"></span>
    </div>

    <div v-if="node.expanded && node.children.length === 0 && !node.loading" class="tree-empty" :style="{ paddingLeft: `${8 + (depth + 1) * 18 + 26}px` }">
      空文件夹
    </div>

    <TreeBranch
      v-for="child in node.children"
      :key="child.relPath"
      :node="child"
      :depth="depth + 1"
      :search-query="searchQuery"
      :current-path="currentPath"
      @load="emit('load', $event)"
      @open-file="emit('open-file', $event)"
      @navigate="emit('navigate', $event)"
    />
  </div>
</template>

<style scoped>
.tree-row {
  display: flex;
  align-items: center;
  gap: 4px;
  height: 28px;
  padding-right: 8px;
  border-radius: var(--r-sm);
  cursor: pointer;
  font-size: 13px;
  color: var(--c-text-secondary);
  transition: background 0.1s;
  white-space: nowrap;
  overflow: hidden;
}

.tree-row:hover {
  background: var(--c-bg-hover);
}

.tree-row--match {
  background: var(--c-brand-soft);
}

.tree-row--active {
  background: var(--c-brand-soft);
  color: var(--c-brand);
  font-weight: 600;
}

.tree-row--dir {
  font-weight: 500;
}

.tree-caret {
  width: 14px;
  height: 14px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  color: var(--c-text-tertiary);
}

.tree-row:hover .tree-caret {
  color: var(--c-text-tertiary);
}

.tree-row--dir .tree-caret {
  color: var(--c-icon-secondary);
}

.tree-caret--file {
  visibility: hidden;
}

.tree-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 16px;
  height: 16px;
}

.tree-label {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
}

.tree-spinner {
  width: 10px;
  height: 10px;
  border: 1.5px solid var(--c-brand-border);
  border-top-color: var(--c-brand);
  border-radius: 50%;
  animation: spin 0.7s linear infinite;
  flex-shrink: 0;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.tree-empty {
  font-size: 12px;
  color: var(--c-text-tertiary);
  font-style: italic;
  padding: 2px 0;
}
</style>
