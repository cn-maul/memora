// 索引状态 → 友好文案（S1 文案统一）
// 后端状态机：pending / extracting / embedding / indexed / failed / ignored
export const STATUS_LABEL: Record<string, string> = {
  pending: '等待处理',
  extracting: '正在准备',
  embedding: '正在准备',
  indexed: '可搜索',
  failed: '出问题了',
  ignored: '已跳过',
}

export function statusLabel(s?: string): string {
  if (!s) return ''
  return STATUS_LABEL[s] || s
}

// 正常状态（indexed / pending）在列表中不占格子，只有异常或进行中才显示
export function isAbnormal(s?: string): boolean {
  return s === 'failed' || s === 'extracting' || s === 'embedding'
}

export function statusClass(s?: string): string {
  const map: Record<string, string> = {
    indexed: 'status-chip--ok',
    extracting: 'status-chip--busy',
    embedding: 'status-chip--busy',
    failed: 'status-chip--err',
    pending: 'status-chip--muted',
    ignored: 'status-chip--muted',
  }
  return map[s || ''] || 'status-chip--muted'
}
