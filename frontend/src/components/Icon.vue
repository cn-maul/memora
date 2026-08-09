<script setup lang="ts">
// 轻量内联 SVG 图标集（lucide 风格，仿 CatPaw 图标视觉）
import { computed } from 'vue'

export type IconName =
  | 'folder'
  | 'folder-open'
  | 'search'
  | 'clock'
  | 'chat'
  | 'chart'
  | 'settings'
  | 'memory'
  | 'file'
  | 'file-image'
  | 'file-pdf'
  | 'file-word'
  | 'file-excel'
  | 'file-ppt'
  | 'file-archive'
  | 'file-code'
  | 'file-audio'
  | 'file-video'
  | 'file-text'
  | 'go'
  | 'json'
  | 'db'
  | 'archive'
  | 'image'
  | 'refresh'
  | 'eye'
  | 'chevrons-right'
  | 'plus'
  | 'x'
  | 'check'
  | 'trash'
  | 'external'
  | 'chevron-right'
  | 'chevron-down'
  | 'git-branch'
  | 'arrow-left'
  | 'arrow-right'
  | 'download'

const props = withDefaults(defineProps<{ name: IconName; size?: number; color?: string }>(), { size: 16 })

const paths: Record<IconName, string> = {
  folder: '<path d="M4 7a2 2 0 0 1 2-2h4l2 2h6a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z"/>',
  'folder-open': '<path d="m6 14 1.5-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"/>',
  search:
    '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  chat:
    '<path d="M21 12a8 8 0 0 1-8 8H5l-3 2 1-3.5A8 8 0 1 1 21 12z"/>',
  chart: '<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="M8 16v-5M13 16V8M18 16v-9"/>',
  settings:
    '<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3"/><path d="M1 14h6M9 8h6M17 16h6"/>',
  memory:
    '<rect x="4" y="8" width="16" height="8" rx="2"/><path d="M8 8V6M12 8V6M16 8V6M8 18v-2M12 18v-2M16 18v-2"/>',
  file: '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/>',
  'file-image': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><circle cx="9" cy="10" r="1.5"/><path d="m21 15-2.5-2.5a2 2 0 0 0-2.83 0L6 22"/>',
  'file-pdf': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><path d="M9.5 18V11h1.5a1.5 1.5 0 0 1 0 3H9.5"/><path d="M14.5 13.5 16 11h.01"/><path d="M16.5 15.5h.01"/>',
  'file-word': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><path d="M7 11.5 9.5 18l2.5-6.5L14.5 18 17 11.5"/>',
  'file-excel': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><path d="M8 11l4 5M12 11l-4 5"/><path d="M14 12.5h4M14 15.5h4"/>',
  'file-ppt': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><path d="M8 12h8M8 12v4M8 16h4"/>',
  'file-archive': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><path d="M9 11h6M9 15h6M12 11v4M11 7h2M12 7v4"/>',
  'file-code': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><path d="m10 10-3 3 3 3M14 10l3 3-3 3"/>',
  'file-audio': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><circle cx="12" cy="15" r="2"/><path d="M12 12V8l3 1"/>',
  'file-video': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><path d="m9 11 5 3-5 3z"/>',
  'file-text': '<path d="M14 3v5h5"/><path d="M6 21h12a2 2 0 0 0 2-2V8l-6-6H6a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2z"/><path d="M9 12h6M9 15.5h4"/>',
  go: '<text x="6" y="17" font-family="sans-serif" font-size="13" font-weight="700" fill="currentColor" stroke="none">go</text>',
  json: '<path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v3c0 1.1.9 2 2 2h1"/><path d="M16 21h1a2 2 0 0 0 2-2v-3c0-1.1.9-2 2-2a2 2 0 0 1-2-2V5a2 2 0 0 0-2-2h-1"/>',
  db: '<ellipse cx="12" cy="5" rx="8" ry="3" fill="none"/><path d="M4 5v14c0 1.66 3.58 3 8 3s8-1.34 8-3V5" fill="none"/><path d="M4 12c0 1.66 3.58 3 8 3s8-1.34 8-3"/><path d="M4 19c0 1.66 3.58 3 8 3s8-1.34 8-3"/>',
  archive: '<rect x="2" y="3" width="20" height="5" rx="1" fill="none"/><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8"/><path d="M10 12h4"/>',
  image: '<rect x="3" y="3" width="18" height="18" rx="2" fill="none"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21"/>',
  refresh: '<path d="M21 12a9 9 0 1 1-2.6-6.3"/><path d="M21 3v6h-6"/>',
  eye: '<path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/>',
  'chevrons-right': '<path d="m6 17 5-5-5-5"/><path d="m13 17 5-5-5-5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  trash: '<path d="M3 6h18M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>',
  external: '<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/>',
  'chevron-right': '<path d="m9 18 6-6-6-6"/>',
  'chevron-down': '<path d="m6 9 6 6 6-6"/>',
  'arrow-left': '<path d="M19 12H5M12 19l-7-7 7-7"/>',
  'arrow-right': '<path d="M5 12h14M12 5l7 7-7 7"/>',
  download: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/>',
  'git-branch': '<line x1="6" y1="3" x2="6" y2="15"/><circle cx="6" cy="18" r="3"/><line x1="6" y1="12" x2="18" y2="6"/><circle cx="18" cy="10" r="3"/><circle cx="6" cy="6" r="3"/>',
}

const inner = computed(() => paths[props.name])
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    :style="color ? { color } : undefined"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    v-html="inner"
  />
</template>
