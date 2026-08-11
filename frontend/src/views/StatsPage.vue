<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { getStats, exportStats } from '@/api/client'
import type { StatsMetrics } from '@/types'
import Icon from '@/components/Icon.vue'

const router = useRouter()
const stats = ref<StatsMetrics | null>(null)
const enabled = ref(true)
const range = ref('week')
const loading = ref(false)
const exporting = ref(false)
const loadError = ref('')

onMounted(async () => {
  await loadStats()
})

async function loadStats() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await getStats({ range: range.value })
    enabled.value = res.enabled
    stats.value = res.metrics ?? null
  } catch (e: any) {
    stats.value = null
    loadError.value = e.message || '加载统计失败'
  } finally {
    loading.value = false
  }
}

async function doExport(format: string) {
  exporting.value = true
  loadError.value = ''
  try {
    const blob = await exportStats(format, { range: range.value })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `report.${format}`
    // 需挂载到 DOM 再 click，Firefox 下未挂载不触发下载
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  } catch (e: any) {
    loadError.value = e.message || '导出失败'
  } finally {
    exporting.value = false
  }
}

// 柱图最大提交数（全零时用 1 作为安全分母，修复 L-02 的 NaN%）
// stats.value.commitsByDay 可能为 null（后端无提交时返回 nil slice → JSON null）
const maxCount = computed(() => {
  const commits = stats.value?.commitsByDay || []
  if (commits.length === 0) return 1
  const m = Math.max(...commits.map((x) => x.count))
  return m > 0 ? m : 1
})

// 后端无数据时返回 nil slice → JSON null，模板直接取 .length 会白屏，统一兜底
const commitsByDay = computed(() => stats.value?.commitsByDay || [])
const hotFiles = computed(() => stats.value?.hotFiles || [])
const tagDistribution = computed(() => stats.value?.tagDistribution || [])
</script>

<template>
  <div class="stats-page">
    <div class="page-header">
      <div>
        <h2>统计</h2>
        <p class="page-sub">工作节奏与文档活跃度总览</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-ghost btn-sm" :disabled="exporting" @click="doExport('csv')">
          <Icon name="external" :size="13" />
          导出 CSV
        </button>
        <button class="btn btn-ghost btn-sm" :disabled="exporting" @click="doExport('md')">
          <Icon name="external" :size="13" />
          导出 Markdown
        </button>
      </div>
    </div>

    <div v-if="loadError" class="alert alert--error">{{ loadError }}</div>

    <div v-if="!enabled" class="empty-state">
      统计已关闭，可在「设置」里开启
      <button class="btn btn-primary btn-sm" style="margin-top: 10px" @click="router.push('/settings')">去设置</button>
    </div>

    <div v-else-if="loading" class="loading">加载中…</div>

    <div v-else-if="!stats" class="empty-state">
      还没有统计数据
      <span class="empty-desc">修改文件并自动保存版本后，这里会出现你的活跃趋势</span>
    </div>

    <div v-else class="stats-content">
      <div class="range-bar segmented">
        <button class="btn" :class="{ 'btn--active': range === 'week' }" @click="range = 'week'; loadStats()">本周</button>
        <button class="btn" :class="{ 'btn--active': range === 'month' }" @click="range = 'month'; loadStats()">本月</button>
        <button class="btn" :class="{ 'btn--active': range === 'quarter' }" @click="range = 'quarter'; loadStats()">本季度</button>
      </div>

      <!-- 概览卡片 -->
      <div class="stats-grid">
        <div class="stat-card card">
          <div class="stat-label">
            <Icon name="refresh" :size="14" />
            文件变更
          </div>
          <div class="stat-value">
            <span class="c-added">+{{ stats.fileChanges.added }}</span>
            <span class="c-modified">~{{ stats.fileChanges.modified }}</span>
            <span class="c-deleted">−{{ stats.fileChanges.deleted }}</span>
          </div>
        </div>
        <div class="stat-card card">
          <div class="stat-label">
            <Icon name="memory" :size="14" />
            活跃度
          </div>
          <div class="stat-value">{{ (stats.iterationRate * 100).toFixed(1) }}%</div>
          <div class="stat-hint">你整理和更新文件的频率</div>
        </div>
        <div class="stat-card card">
          <div class="stat-label">
            <Icon name="clock" :size="14" />
            时段分布
          </div>
          <div class="stat-hour">
            <span class="hour-item"><i class="h-dot h-morning"></i>{{ stats.hourBuckets.morning }} <em>上午</em></span>
            <span class="hour-item"><i class="h-dot h-afternoon"></i>{{ stats.hourBuckets.afternoon }} <em>下午</em></span>
            <span class="hour-item"><i class="h-dot h-evening"></i>{{ stats.hourBuckets.evening }} <em>晚上</em></span>
            <span class="hour-item"><i class="h-dot h-night"></i>{{ stats.hourBuckets.night }} <em>深夜</em></span>
          </div>
        </div>
      </div>

      <!-- 版本趋势（简易柱状） -->
      <div class="card">
        <div class="card-title">版本趋势</div>
        <div class="bar-chart">
          <div v-for="(d, i) in commitsByDay" :key="i" class="bar-item">
            <div class="bar-value">{{ d.count > 0 ? d.count : '' }}</div>
            <div class="bar-fill" :style="{ height: Math.min(100, (d.count / maxCount * 100)) + '%' }"></div>
            <div class="bar-label">{{ d.date.slice(-5) }}</div>
          </div>
        </div>
      </div>

      <div class="stats-two-col">
        <!-- 热点文件 -->
        <div class="card" v-if="hotFiles.length">
          <div class="card-title">热点文件</div>
          <div v-for="f in hotFiles" :key="f.relPath" class="hot-file">
            <span
              class="hot-file__path"
              :title="'去全部文件页查看：' + f.relPath"
              @click="router.push({ path: '/workspace', query: { highlight: f.relPath } })"
            >
              <Icon name="file" :size="13" />
              {{ f.relPath }}
            </span>
            <span class="hot-file-count">{{ f.count }}</span>
          </div>
        </div>

        <!-- 标签分布 -->
        <div class="card" v-if="tagDistribution.length">
          <div class="card-title">标签分布</div>
          <div v-for="t in tagDistribution" :key="t.tag" class="tag-dist-item">
            <span class="tag-chip tag-chip--sm">{{ t.tag }}</span>
            <span class="tag-dist-count">{{ t.count }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.stats-page {
  padding: 20px 24px 28px;
  overflow-y: auto;
  height: 100%;
}

.page-sub {
  font-size: 12px;
  color: var(--c-text-tertiary);
  margin: 2px 0 0;
}

.range-bar {
  margin-bottom: 16px;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}

.stat-card .stat-label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--c-text-tertiary);
  margin-bottom: 10px;
}

.stat-value {
  font-size: 22px;
  font-weight: 700;
  display: flex;
  gap: 14px;
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.01em;
}

.c-added { color: var(--c-success); }
.c-modified { color: var(--c-warning); }
.c-deleted { color: var(--c-danger); }

.stat-hint {
  font-size: 12px;
  color: var(--c-text-tertiary);
  margin-top: 4px;
}

.stat-hour {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
}

.hour-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  font-weight: 600;
  color: var(--c-text-primary);
  font-variant-numeric: tabular-nums;
}

.hour-item em {
  font-style: normal;
  font-size: 11px;
  color: var(--c-text-tertiary);
  font-weight: 400;
}

.hour-item i {
  width: 8px;
  height: 8px;
  border-radius: var(--r-xs);
  display: inline-block;
}

.h-dot {
  width: 8px;
  height: 8px;
  border-radius: var(--r-xs);
  display: inline-block;
}

.h-morning { background: var(--c-brand); }
.h-afternoon { background: var(--c-info); }
.h-evening { background: var(--c-warning); }
.h-night { background: var(--c-danger); }

.bar-chart {
  display: flex;
  align-items: flex-end;
  gap: 4px;
  height: 140px;
  padding: 10px 4px 0;
}

.bar-item {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  height: 100%;
  justify-content: flex-end;
  gap: 4px;
}

.bar-value {
  font-size: 10px;
  color: var(--c-text-tertiary);
  font-variant-numeric: tabular-nums;
}

.bar-fill {
  width: 100%;
  max-width: 26px;
  background: linear-gradient(180deg, var(--c-brand) 0%, var(--c-brand-hover) 100%);
  border-radius: var(--r-xs) var(--r-xs) 0 0;
  min-height: 2px;
  transition: height 0.3s ease;
}

.bar-label {
  font-size: 10px;
  color: var(--c-text-tertiary);
  margin-top: 2px;
  font-variant-numeric: tabular-nums;
}

.stats-two-col {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 12px;
}

.hot-file {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 10px;
  padding: 7px 2px;
  font-size: 13px;
  border-bottom: 1px solid var(--c-border);
}

.hot-file:last-child {
  border-bottom: none;
}

.hot-file__path {
  display: flex;
  align-items: center;
  gap: 7px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
  color: var(--c-text-primary);
}
.hot-file__path:hover {
  color: var(--c-brand);
  text-decoration: underline;
}

.hot-file-count {
  color: var(--c-text-secondary);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
}

.tag-dist-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 5px 0;
  border-bottom: 1px solid var(--c-border);
}

.tag-dist-item:last-child {
  border-bottom: none;
}

.tag-dist-count {
  font-size: 13px;
  color: var(--c-text-secondary);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.empty-desc {
  display: block;
  margin-top: 8px;
  font-size: 12.5px;
  color: var(--c-text-tertiary);
}
</style>