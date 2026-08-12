// 统一前端错误模型（Phase 2 / P1-13）：
//   - AppError：带稳定 code + 用户可读 message + requestId（用于定位后端操作链）
//   - parseEnvelope：把未知结构的响应体校验为 ApiEnvelope，避免原始 body/Blob 泄漏
// 所有 API 失败都必须以 AppError 形式抛出；界面只展示 AppError.message。

export interface ApiEnvelope<T = unknown> {
  ok: boolean
  code: string
  message?: string
  data?: T
  requestId?: string
}

export interface SSEPayload {
  topic: string
  data: unknown
}

// 与后端 contract 错误码对齐（稳定契约）
export const ERROR_CODES = {
  badRequest: 'bad_request',
  invalidParam: 'invalid_param',
  notFound: 'not_found',
  notConfigured: 'not_configured',
  unauthorized: 'unauthorized',
  forbidden: 'forbidden',
  conflict: 'conflict',
  rateLimited: 'rate_limited',
  timeout: 'timeout',
  canceled: 'canceled',
  llmError: 'llm_error',
  extractError: 'extract_error',
  internal: 'internal',
} as const

export type ErrorCode = (typeof ERROR_CODES)[keyof typeof ERROR_CODES]

export class AppError extends Error {
  readonly code: ErrorCode
  readonly status?: number
  readonly requestId?: string
  readonly detail?: unknown

  constructor(message: string, code: ErrorCode = ERROR_CODES.internal, opts?: { status?: number; requestId?: string; detail?: unknown }) {
    super(message)
    this.name = 'AppError'
    this.code = code
    this.status = opts?.status
    this.requestId = opts?.requestId
    this.detail = opts?.detail
  }
}

export function isAppError(e: unknown): e is AppError {
  return e instanceof AppError
}

// 运行时校验：把未知响应解析为信封。不合法 → 抛 AppError（不把原始内容当 message）。
export function parseEnvelope(raw: unknown): ApiEnvelope {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
    throw new AppError('服务响应格式异常', ERROR_CODES.internal, { detail: raw })
  }
  const obj = raw as Record<string, unknown>
  if (typeof obj.code !== 'string') {
    throw new AppError('服务响应格式异常', ERROR_CODES.internal, { detail: raw })
  }
  // 后端契约：成功响应 code="ok"，错误响应 code 为稳定错误码；两种都可能不带 ok 字段。
  // 优先按 code 判定，兼容旧版带 ok 字段的信封。
  const ok = obj.code === 'ok' || obj.ok === true
  const env: ApiEnvelope = { ok, code: obj.code }
  if (typeof obj.message === 'string') env.message = obj.message
  if (typeof obj.requestId === 'string') env.requestId = obj.requestId
  if ('data' in obj) env.data = obj.data
  return env
}

// 校验并解包成功数据；失败 → 抛 AppError（从信封取 code/message/requestId）。
export function unwrapData<T>(raw: unknown): T {
  const env = parseEnvelope(raw)
  if (!env.ok) {
    throw new AppError(env.message || '操作失败', normalizeCode(env.code), {
      status: httpStatusForCode(env.code),
      requestId: env.requestId,
      detail: env.data,
    })
  }
  return env.data as T
}

// 后端原始 code → 前端 ErrorCode；未知 code 一律 internal（不向用户暴露）。
function normalizeCode(code: string): ErrorCode {
  const values = Object.values(ERROR_CODES)
  return (values as string[]).includes(code) ? (code as ErrorCode) : ERROR_CODES.internal
}

// 错误码 → 建议的 HTTP 状态（与后端 StatusForCode 对齐；仅用于提示，不严格要求一致）。
function httpStatusForCode(code: string): number | undefined {
  switch (code) {
    case ERROR_CODES.badRequest:
    case ERROR_CODES.invalidParam:
    case ERROR_CODES.notConfigured:
      return 400
    case ERROR_CODES.notFound:
      return 404
    case ERROR_CODES.unauthorized:
      return 401
    case ERROR_CODES.forbidden:
      return 403
    case ERROR_CODES.conflict:
      return 409
    case ERROR_CODES.rateLimited:
      return 429
    case ERROR_CODES.timeout:
      return 504
    case ERROR_CODES.llmError:
      return 502
    case ERROR_CODES.extractError:
      return 422
    default:
      return undefined
  }
}