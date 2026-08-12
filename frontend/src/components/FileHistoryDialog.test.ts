// FileHistoryDialog 测试：版本列表 / 空历史（isNotFound）/ 加载失败 / 恢复成功与失败
// 注：组件不依赖 vue-router（打开当前版本走 navigate-file 事件），无需 router mock。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import FileHistoryDialog from '@/components/FileHistoryDialog.vue'

// 仅 mock 网络函数，safeErrorMsg/isNotFound 走真实实现
const mocks = vi.hoisted(() => ({
  getFileHistory: vi.fn(),
  downloadHistoryVersion: vi.fn(),
  resolveFileId: vi.fn(),
  restoreFile: vi.fn(),
}))

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return { ...actual, ...mocks }
})

function mountDialog() {
  return mount(FileHistoryDialog, {
    props: { file: { relPath: 'docs/note.md', name: 'note.md', docType: 'md' }, open: false },
  })
}

async function openDialog(wrapper: ReturnType<typeof mountDialog>) {
  await wrapper.setProps({ open: true })
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.downloadHistoryVersion.mockResolvedValue(new Blob(['x']))
  mocks.restoreFile.mockResolvedValue(undefined)
})

describe('FileHistoryDialog 版本历史加载', () => {
  it('打开弹窗 → resolveFileId + getFileHistory → 展示版本列表', async () => {
    mocks.resolveFileId.mockResolvedValue(5)
    mocks.getFileHistory.mockResolvedValue({
      commits: [
        { hash: 'abc123def456', time: 1700000000000, message: '更新了说明文档', author: 'tester' },
        { hash: 'def456abc789', time: 1690000000000, message: '初始提交', author: 'tester' },
      ],
    })
    const wrapper = mountDialog()
    await openDialog(wrapper)

    expect(mocks.resolveFileId).toHaveBeenCalledWith('docs/note.md')
    expect(mocks.getFileHistory).toHaveBeenCalledWith(5)
    const items = wrapper.findAll('.version-item')
    expect(items.length).toBe(2)
    expect(wrapper.text()).toContain('更新了说明文档')
    expect(wrapper.text()).toContain('初始提交')
    expect(wrapper.find('.detail-empty').exists()).toBe(false)
    expect(wrapper.find('.alert--error').exists()).toBe(false)
  })

  it('文件未索引（not_found）→ 显示"暂无版本历史"空态而非错误', async () => {
    mocks.resolveFileId.mockRejectedValue({ code: 'not_found', message: '文件不存在' })
    const wrapper = mountDialog()
    await openDialog(wrapper)

    expect(wrapper.find('.detail-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('暂无版本历史')
    expect(wrapper.find('.alert--error').exists()).toBe(false)
  })

  it('加载失败（非 not_found）→ 明确展示错误，不伪装成空数据', async () => {
    mocks.resolveFileId.mockRejectedValue(new Error('服务器开小差了'))
    const wrapper = mountDialog()
    await openDialog(wrapper)

    const err = wrapper.find('.alert--error')
    expect(err.exists()).toBe(true)
    expect(err.text()).toContain('服务器开小差了')
    expect(wrapper.find('.detail-empty').exists()).toBe(false)
  })

  it('getFileHistory 阶段失败同样显示错误', async () => {
    mocks.resolveFileId.mockResolvedValue(5)
    mocks.getFileHistory.mockRejectedValue({ code: 'internal', message: '历史记录拉取失败' })
    const wrapper = mountDialog()
    await openDialog(wrapper)

    const err = wrapper.find('.alert--error')
    expect(err.exists()).toBe(true)
    expect(err.text()).toContain('历史记录拉取失败')
  })
})

describe('FileHistoryDialog 一键恢复', () => {
  beforeEach(() => {
    mocks.resolveFileId.mockResolvedValue(5)
    mocks.getFileHistory.mockResolvedValue({
      commits: [{ hash: 'abc123', time: 1, message: '版本一', author: 'a' }],
    })
  })

  it('恢复成功 → 调用 restore API 并显示成功提示', async () => {
    const wrapper = mountDialog()
    await openDialog(wrapper)

    // 点「恢复此版本」→ 行内确认条出现
    await wrapper.find('.version-item .btn-primary').trigger('click')
    expect(wrapper.find('.restore-confirm').exists()).toBe(true)

    // 点「确认恢复」→ 调用 restore API
    await wrapper.find('.restore-confirm .btn-primary').trigger('click')
    await flushPromises()

    expect(mocks.restoreFile).toHaveBeenCalledWith(5, 'abc123')
    const notice = wrapper.find('.alert--success')
    expect(notice.exists()).toBe(true)
    expect(notice.text()).toContain('已恢复')
    expect(wrapper.find('.alert--error').exists()).toBe(false)
  })

  it('恢复失败 → 显示安全错误文案，不出现成功提示', async () => {
    mocks.restoreFile.mockRejectedValue({ message: '[object Blob]' })
    const wrapper = mountDialog()
    await openDialog(wrapper)

    await wrapper.find('.version-item .btn-primary').trigger('click')
    await wrapper.find('.restore-confirm .btn-primary').trigger('click')
    await flushPromises()

    const err = wrapper.find('.alert--error')
    expect(err.exists()).toBe(true)
    expect(err.text()).toBe('恢复失败') // [object Blob] → 回退通用文案
    expect(wrapper.find('.alert--success').exists()).toBe(false)
  })

  it('取消确认 → 不调用 restore API', async () => {
    const wrapper = mountDialog()
    await openDialog(wrapper)

    await wrapper.find('.version-item .btn-primary').trigger('click')
    await wrapper.find('.restore-confirm .btn-ghost').trigger('click')
    expect(wrapper.find('.restore-confirm').exists()).toBe(false)
    expect(mocks.restoreFile).not.toHaveBeenCalled()
  })
})

describe('FileHistoryDialog 交互', () => {
  it('关闭按钮发出 close 事件', async () => {
    mocks.resolveFileId.mockResolvedValue(5)
    mocks.getFileHistory.mockResolvedValue({ commits: [] })
    const wrapper = mountDialog()
    await openDialog(wrapper)

    await wrapper.find('.modal-actions .btn-ghost').trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
  })

  it('「打开当前版本」发出 navigate-file 事件并关闭', async () => {
    mocks.resolveFileId.mockResolvedValue(5)
    mocks.getFileHistory.mockResolvedValue({ commits: [] })
    const wrapper = mountDialog()
    await openDialog(wrapper)

    await wrapper.find('.modal-actions .btn-primary').trigger('click')
    expect(wrapper.emitted('navigate-file')).toBeTruthy()
    expect(wrapper.emitted('navigate-file')?.[0]).toEqual(['docs/note.md'])
    expect(wrapper.emitted('close')).toBeTruthy()
  })
})
