// 错误模型测试：unwrapData / isAppError / translateApiError / safeErrorMsg
// 覆盖 Phase 2（P1-13）统一错误模型 + fileHistory 的安全错误提示约定。
import { describe, it, expect } from 'vitest'
import { unwrapData, isAppError, AppError, ERROR_CODES } from '@/utils/errors'
import { translateApiError } from '@/api/client'
import { safeErrorMsg } from '@/utils/fileHistory'

// 断言函数抛 AppError 并返回该错误（便于进一步校验 code/message/requestId）
function expectAppError(fn: () => unknown): AppError {
  try {
    fn()
  } catch (e) {
    expect(isAppError(e)).toBe(true)
    return e as AppError
  }
  throw new Error('预期抛出 AppError，但没有抛')
}

describe('unwrapData', () => {
  it('成功响应（code=ok）解包出 data', () => {
    const data = unwrapData<{ id: number }>({ code: 'ok', data: { id: 42 } })
    expect(data).toEqual({ id: 42 })
  })

  it('兼容旧版 ok:true 信封', () => {
    const data = unwrapData<{ items: number[] }>({ ok: true, code: 'ok', data: { items: [1, 2] } })
    expect(data).toEqual({ items: [1, 2] })
  })

  it('{code,message} 错误响应 → 抛 AppError（携带 code/message/requestId）', () => {
    const err = expectAppError(() =>
      unwrapData({ code: 'not_found', message: '文件不存在', requestId: 'req-1' }),
    )
    expect(err.code).toBe(ERROR_CODES.notFound)
    expect(err.message).toBe('文件不存在')
    expect(err.requestId).toBe('req-1')
    // not_found 建议 404 状态
    expect(err.status).toBe(404)
  })

  it('未知错误码归一为 internal，不向用户泄露原始 code', () => {
    const err = expectAppError(() =>
      unwrapData({ code: 'internal.unexpected', message: '内部错误' }),
    )
    expect(err.code).toBe(ERROR_CODES.internal)
    expect(err.message).toBe('内部错误')
  })

  it('各稳定错误码正确映射', () => {
    expect(expectAppError(() => unwrapData({ code: 'bad_request', message: 'x' })).code).toBe('bad_request')
    expect(expectAppError(() => unwrapData({ code: 'invalid_param', message: 'x' })).code).toBe('invalid_param')
    expect(expectAppError(() => unwrapData({ code: 'unauthorized', message: 'x' })).code).toBe('unauthorized')
    expect(expectAppError(() => unwrapData({ code: 'rate_limited', message: 'x' })).code).toBe('rate_limited')
    expect(expectAppError(() => unwrapData({ code: 'llm_error', message: 'x' })).code).toBe('llm_error')
  })

  it('格式异常响应（null/字符串/数组/无 code 对象）→ 抛 internal，不把原始内容当 message', () => {
    expect(() => unwrapData(null)).toThrow(AppError)
    expect(() => unwrapData('not-json')).toThrow(AppError)
    expect(() => unwrapData([1, 2])).toThrow(AppError)
    const err = expectAppError(() => unwrapData({ data: 1 })) // 缺 code
    expect(err.code).toBe(ERROR_CODES.internal)
  })
})

describe('isAppError', () => {
  it('AppError 实例 → true', () => {
    expect(isAppError(new AppError('x'))).toBe(true)
  })

  it('普通 Error / 其它值 → false', () => {
    expect(isAppError(new Error('x'))).toBe(false)
    expect(isAppError('x')).toBe(false)
    expect(isAppError(undefined)).toBe(false)
    expect(isAppError(null)).toBe(false)
  })
})

describe('translateApiError（消息 → 小白友好文案）', () => {
  it('空消息回退通用文案', () => {
    expect(translateApiError('')).toBe('操作失败，请重试')
    expect(translateApiError(undefined as unknown as string)).toBe('操作失败，请重试')
  })

  it('网络/超时 → 无法连接服务', () => {
    expect(translateApiError('Network Error')).toBe('无法连接服务，请检查网络或稍后重试')
    expect(translateApiError('timeout of 30000ms exceeded')).toBe('无法连接服务，请检查网络或稍后重试')
    expect(translateApiError('无法连接到服务器')).toBe('无法连接服务，请检查网络或稍后重试')
  })

  it('聊天端点未配置 → 引导去设置 AI 助手', () => {
    expect(translateApiError('聊天端点未配置')).toBe('AI 助手还未连接，请到「设置 → AI 助手」里连接')
  })

  it('嵌入端点未配置 → 引导去设置内容整理模型', () => {
    expect(translateApiError('嵌入端点未配置')).toBe('「按内容搜索」还未连接，请到「设置 → 内容整理模型」里连接')
  })

  it('工作区未初始化 → 引导去设置选择文件夹', () => {
    expect(translateApiError('工作区未初始化')).toBe('还没有选择要管理的文件夹，请到「设置」里选择')
    expect(translateApiError('仓库未初始化')).toBe('还没有选择要管理的文件夹，请到「设置」里选择')
  })

  it('鉴权失败 / API Key 问题', () => {
    expect(translateApiError('Request failed with status code 401')).toBe('API Key 不正确或已失效，请检查后重试')
    expect(translateApiError('unauthorized')).toBe('API Key 不正确或已失效，请检查后重试')
    expect(translateApiError('invalid api key')).toBe('API Key 不正确或已失效，请检查后重试')
  })

  it('403 → 无权限', () => {
    expect(translateApiError('forbidden')).toBe('没有权限访问该服务，请检查账号权限或 API Key')
  })

  it('429 限流', () => {
    expect(translateApiError('429 Too Many Requests')).toBe('请求太频繁被限流了，稍等片刻再试')
    expect(translateApiError('rate limit exceeded')).toBe('请求太频繁被限流了，稍等片刻再试')
  })

  it('模型不存在', () => {
    expect(translateApiError('model "gpt-4o" not found')).toBe('模型名称不正确，请检查模型设置')
  })

  it('未匹配的消息原样返回（不吞掉可读信息）', () => {
    expect(translateApiError('自定义错误消息')).toBe('自定义错误消息')
  })
})

describe('safeErrorMsg（[object Blob]/JSON body/普通消息 映射）', () => {
  it('[object Blob] / [object File] → 回退通用文案', () => {
    expect(safeErrorMsg({ message: '[object Blob]' }, '下载失败')).toBe('下载失败')
    expect(safeErrorMsg({ message: 'blob is [object File]' }, '下载失败')).toBe('下载失败')
  })

  it('request failed with status code → 回退通用文案', () => {
    expect(safeErrorMsg({ message: 'Request failed with status code 500' }, '下载失败')).toBe('下载失败')
    expect(safeErrorMsg({ message: 'request failed with status code 403' }, '恢复失败')).toBe('恢复失败')
  })

  it('完整 JSON body → 回退通用文案，不展示原始 body', () => {
    expect(safeErrorMsg({ message: '{"code":"bad_request","message":"oops"}' }, '下载失败')).toBe('下载失败')
    expect(safeErrorMsg({ message: '[{"a":1}]' }, '下载失败')).toBe('下载失败')
  })

  it('普通消息 → 原样返回', () => {
    expect(safeErrorMsg({ message: '磁盘空间不足' }, '下载失败')).toBe('磁盘空间不足')
  })

  it('空/缺失消息 → 回退通用文案', () => {
    expect(safeErrorMsg({}, '下载失败')).toBe('下载失败')
    expect(safeErrorMsg(undefined, '下载失败')).toBe('下载失败')
    expect(safeErrorMsg({ message: '   ' }, '下载失败')).toBe('下载失败')
  })
})
