<script setup lang="ts">
// 统一面包屑：资源管理器 / 全部文件 两页复用。
// items 末尾为当前目录（不可点、高亮），其余可点击导航；
// up 可选显示"上一级"按钮，触发 emit('up')。
import Icon from '@/components/Icon.vue'

export interface Crumb {
  label: string
  path: string
}

withDefaults(defineProps<{ items: Crumb[]; up?: boolean }>(), { up: false })

const emit = defineEmits<{
  (e: 'navigate', path: string): void
  (e: 'up'): void
}>()
</script>

<template>
  <nav class="crumbs">
    <button v-if="up" class="crumb crumb--up" title="上一级" @click="emit('up')">
      <Icon name="arrow-left" :size="13" />
      <span>上一级</span>
    </button>
    <template v-for="(c, i) in items" :key="c.path">
      <span v-if="i > 0" class="crumb__sep"><Icon name="chevron-right" :size="12" /></span>
      <span
        class="crumb"
        :class="{ 'crumb--current': i === items.length - 1 }"
        @click="i < items.length - 1 && emit('navigate', c.path)"
      >{{ c.label }}</span>
    </template>
  </nav>
</template>

<style scoped>
.crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  font-size: 13px;
  flex-wrap: wrap;
  min-height: 26px;
}

.crumb {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 7px;
  border-radius: var(--r-sm);
  color: var(--c-text-secondary);
  cursor: pointer;
  transition: background 0.14s ease, color 0.14s ease;
}

.crumb:hover:not(.crumb--current) {
  background: var(--c-bg-hover);
  color: var(--c-text-primary);
}

.crumb--current {
  color: var(--c-text-primary);
  font-weight: 600;
  cursor: default;
}

.crumb--current:hover {
  background: transparent;
}

.crumb--up {
  color: var(--c-info);
  font-weight: 500;
  margin-right: 4px;
}

.crumb--up:hover {
  background: var(--c-info-soft);
  color: var(--c-info);
}

.crumb__sep {
  display: inline-flex;
  align-items: center;
  color: var(--c-text-tertiary);
  opacity: 0.7;
}
</style>
