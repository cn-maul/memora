// 文件详情/版本历史弹窗共用小工具（收敛各页面重复的 isNotFound/safeErrorMsg 约定）

// 下载/预览类请求失败时，错误体可能是 Blob、原始 HTTP 文本或完整 JSON body，
// 一律映射为友好提示，绝不把原始内容展示给用户（修复：[object Blob]/body 泄漏）
export function safeErrorMsg(e: any, fallback: string): string {
  const raw = e?.message
  if (typeof raw !== 'string' || !raw.trim()) return fallback
  const s = raw.trim()
  if (/\[object (Blob|File)\]/.test(s) || /request failed with status code/i.test(s)) return fallback
  if (/^[{[]/.test(s)) return fallback // 完整 JSON body 不直接展示
  return s
}

// 文件未索引/不存在（not_found）属预期的"无版本历史"空态；其余失败需明确提示
export function isNotFound(e: any): boolean {
  if (e?.code === 'not_found') return true
  const m = String(e?.message || '')
  return /文件不存在|未索引|not ?found/i.test(m)
}

// 路径最后一段（文件名）；兼容 / 与 \ 分隔
export function baseName(p: string): string {
  const idx = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'))
  return idx >= 0 ? p.slice(idx + 1) : p
}
