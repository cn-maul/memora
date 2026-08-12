// 设置页业务 composable（P2-05 抽取）：配置加载/回填、保存（含错误处理与成功提示）、密钥保存、
// 测试连接、MarkItDown/Python 探测与路径选择、工作区初始化、autoCommit 开关与 debounce 设置等
// 业务状态与操作统一收归此处；页面只负责编排视图，通过解构使用本 composable 返回的状态与函数。
import { ref } from 'vue'
import { useSettingsStore } from '@/stores/settings'
import { useWorkspaceStore } from '@/stores/workspace'
import {
  testMarkitdown,
  browsePickDir,
  detectPython,
  translateApiError,
  type PythonDetectResult,
} from '@/api/client'
import type { ProviderControllers } from '@/composables/useProviderSettings'

export function useSettings(providers: ProviderControllers) {
  const { chat, embed, rerank } = providers
  const settings = useSettingsStore()
  const ws = useWorkspaceStore()

  // ───── MarkItDown / Python ─────
  const pythonPath = ref('')
  const command = ref('')
  const markitdownCmd = ref('')
  const pythonDetected = ref<PythonDetectResult | null>(null)
  const pythonDetecting = ref(false)
  const pythonDetectError = ref('')

  // ───── 工作区 ─────
  const workspacePath = ref('')
  const scanIntervalSec = ref(8)
  const recentWindowHours = ref(24)

  // ───── autoCommit ─────
  const autoCommitEnabled = ref(true)
  const autoCommitDebounceSec = ref(90)

  // ───── 操作状态与提示 ─────
  const testing = ref('')
  const testChatResult = ref('')
  const testEmbedResult = ref('')
  const testRerankResult = ref('')
  const testMarkitdownResult = ref('')
  const saving = ref(false)
  const savedMsg = ref('')
  const saveError = ref('')
  const initing = ref(false)
  const initMsg = ref('')
  const formReady = ref(false)
  // 首次使用向导
  const showWizard = ref(false)
  function onWizardDone() {
    showWizard.value = false
    ws.fetchInfo()
    settings.fetch().then(() => {
      formReady.value = true
      loadFromSettings()
    })
  }

  // ───── 配置加载 / 回填 ─────
  // 首次挂载初始化：拉取设置 + 工作区信息，成功则回填表单。失败时 formReady=false，
  // 页面据此不建立滚动观察器（与历史 onMounted 行为一致）。
  async function initialize() {
    await settings.fetch()
    await ws.fetchInfo()
    if (settings.error) {
      formReady.value = false
      return
    }
    loadFromSettings()
    formReady.value = true
  }

  async function retryLoadSettings() {
    await settings.fetch()
    if (settings.error) {
      formReady.value = false
      return
    }
    loadFromSettings()
    formReady.value = true
  }

  function loadFromSettings() {
    const s = settings.settings
    chat.state.baseUrl = s.llm?.baseUrl || s.llmBaseUrl || ''
    chat.state.model = s.llm?.model || s.llmModel || ''
    // 用 ?? 而非 ||：temperature=0 是合法值（确定性输出），|| 会回退到默认值（修复 L-2）
    chat.state.temperature = s.llm?.temperature ?? s.llmTemperature ?? 0.7
    embed.state.baseUrl = s.embed?.baseUrl || s.embedBaseUrl || ''
    embed.state.model = s.embed?.model || s.embedModel || ''
    embed.state.dimensions = s.embed?.dimensions ?? s.embedDimensions ?? 1024
    rerank.state.baseUrl = s.rerank?.baseUrl || ''
    rerank.state.model = s.rerank?.model || 'Pro/BAAI/bge-reranker-v2-m3'
    pythonPath.value = s.markitdown?.pythonPath || s.markitdownPythonPath || ''
    command.value = s.markitdown?.command || s.markitdownCommand || ''
    markitdownCmd.value = s.markitdown?.markitdownCmd || ''
    scanIntervalSec.value = s.index?.scanIntervalSec ?? 8
    recentWindowHours.value = s.recent?.windowHours ?? 24
    autoCommitEnabled.value = s.autoCommit?.enabled ?? true
    autoCommitDebounceSec.value = s.autoCommit?.debounceSec ?? 90
    workspacePath.value = s.workspace?.path || s.workspacePath || ws.info?.workspacePath || ''
    // 按当前地址反推命中的服务商预设（未命中显示"自定义"）
    chat.detectFromUrl()
    embed.detectFromUrl()
    rerank.detectFromUrl()
  }

  // ───── 保存 ─────
  async function handleSaveSecrets() {
    saving.value = true
    saveError.value = ''
    try {
      await settings.saveSecrets(chat.state.apiKey || undefined, embed.state.apiKey || undefined, rerank.state.apiKey || undefined)
      chat.state.apiKey = ''
      embed.state.apiKey = ''
      rerank.state.apiKey = ''
      savedMsg.value = '密钥已保存'
      setTimeout(() => (savedMsg.value = ''), 3000)
    } catch (e: any) {
      saveError.value = e?.message || '保存密钥失败'
      setTimeout(() => (saveError.value = ''), 5000)
    } finally {
      saving.value = false
    }
  }

  // 需重启配置项的通俗名称（提示"重启后生效"时使用）
  function restartLabel(key: string): string {
    const map: Record<string, string> = {
      'workspace.path': '工作区路径',
      'index.chunkSize': '分块大小',
      'index.chunkOverlap': '分块重叠',
      'index.scanIntervalSec': '扫描间隔',
      'autoCommit.enabled': '自动保存开关',
      'autoCommit.debounceSec': '自动保存间隔',
      'qa.maxContextChars': '问答上下文长度',
      'qa.systemPrompt': '问答提示词',
      'git.authorName': '作者名',
      'git.authorEmail': '作者邮箱',
      'markitdown.pythonPath': 'Python 路径',
      'markitdown.command': '提取命令',
      'markitdown.markitdownCmd': 'MarkItDown 路径',
      'embed.dimensions': '向量维度',
    }
    return map[key] || key
  }

  async function handleSaveSettings() {
    saving.value = true
    saveError.value = ''
    try {
      const result = await settings.save({
        'llm.baseUrl': chat.state.baseUrl,
        'llm.model': chat.state.model,
        'llm.temperature': chat.state.temperature,
        'embed.baseUrl': embed.state.baseUrl,
        'embed.model': embed.state.model,
        'embed.dimensions': embed.state.dimensions,
        'rerank.baseUrl': rerank.state.baseUrl,
        'rerank.model': rerank.state.model,
        'markitdown.pythonPath': pythonPath.value,
        'markitdown.command': command.value,
        'markitdown.markitdownCmd': markitdownCmd.value,
        'index.scanIntervalSec': scanIntervalSec.value,
        'recent.windowHours': recentWindowHours.value,
        'autoCommit.enabled': autoCommitEnabled.value,
        'autoCommit.debounceSec': autoCommitDebounceSec.value,
        'workspace.path': workspacePath.value ?? undefined,
      })
      // 顺带保存表单里填写的密钥（非空才覆盖，保持"留空不修改"语义）。
      const hasKeys = chat.state.apiKey || embed.state.apiKey || rerank.state.apiKey
      if (hasKeys) {
        await settings.saveSecrets(
          chat.state.apiKey || undefined,
          embed.state.apiKey || undefined,
          rerank.state.apiKey || undefined,
        )
        chat.state.apiKey = ''
        embed.state.apiKey = ''
        rerank.state.apiKey = ''
      }
      // 三种提示：需重启 / 维度变更自动重建 / 纯保存成功
      if (result.reindexRequired) {
        savedMsg.value =
          '已保存。检测到向量维度变更，正在后台自动重新整理索引，完成后按内容搜索即可生效（可在「内容整理」页查看进度）。'
      } else if (result.restartRequired && result.restartRequired.length > 0) {
        savedMsg.value =
          `已保存。以下设置将在重启后生效：${result.restartRequired.map((k) => restartLabel(k)).join('、')}`
      } else {
        savedMsg.value = '已保存'
      }
      setTimeout(() => (savedMsg.value = ''), result.reindexRequired ? 12000 : 6000)
    } catch (e: any) {
      saveError.value = e?.message || '保存设置失败'
      setTimeout(() => (saveError.value = ''), 5000)
    } finally {
      saving.value = false
    }
  }

  // ───── 工作区初始化 ─────
  async function handleInitWorkspace() {
    if (!workspacePath.value) {
      initMsg.value = '请先选择要管理的文件夹'
      return
    }
    initing.value = true
    initMsg.value = ''
    try {
      await ws.init({
        workspacePath: workspacePath.value,
        markitdown: {
          pythonPath: pythonPath.value || undefined,
          command: command.value || undefined,
        },
        // 载荷仍按各区块原形状组装（chat 带 temperature、rerank 空地址省略），线格式不变
        llm: chat.buildSection({ includeTemperature: true }),
        embed: embed.buildSection(),
        rerank: rerank.buildSection({ omitEmptyBaseUrl: true }),
      })
      initMsg.value = ws.initialized ? '✓ 工作区已初始化并开始索引' : '✓ 配置已应用'
    } catch (e: any) {
      initMsg.value = `✗ ${e.message}`
    } finally {
      initing.value = false
    }
  }

  // ───── 路径选择（文件夹 / MarkItDown 可执行文件 / Python 解释器） ─────
  const pickingDir = ref(false)
  const pickMsg = ref('')
  const pickingMd = ref(false)
  const mdPickError = ref('')
  // 手动选择 Python 解释器（文本框已隐藏，保留按钮走原生选择器）
  const pickingPython = ref(false)
  async function pickPythonDir() {
    pickingPython.value = true
    pythonDetectError.value = ''
    try {
      const res = await browsePickDir(undefined)
      if (!res.cancelled && res.path) {
        const dir = res.path
        // 尝试将 dir 视为 python 解释器路径
        const exe = dir.endsWith('.exe') ? dir : `${dir}\\python.exe`
        pythonPath.value = exe
        pythonDetected.value = { path: exe, ok: true, version: '' }
      }
    } catch (e: any) {
      pythonDetectError.value = `✗ ${translateApiError(e.message)}`
    } finally {
      pickingPython.value = false
    }
  }
  async function pickWorkspaceDir() {
    pickingDir.value = true
    pickMsg.value = ''
    try {
      const res = await browsePickDir(workspacePath.value || undefined)
      if (!res.cancelled && res.path) {
        workspacePath.value = res.path
      }
    } catch (e: any) {
      pickMsg.value = `✗ ${e.message}`
      setTimeout(() => (pickMsg.value = ''), 4000)
    } finally {
      pickingDir.value = false
    }
  }

  async function pickMarkitdownExe() {
    pickingMd.value = true
    mdPickError.value = ''
    try {
      const res = await browsePickDir(undefined)
      if (!res.cancelled && res.path) {
        markitdownCmd.value = res.path
      }
    } catch (e: any) {
      mdPickError.value = `✗ ${e.message}`
    } finally {
      pickingMd.value = false
    }
  }

  // ───── Python 探测 ─────
  async function runPythonDetect() {
    pythonDetecting.value = true
    pythonDetectError.value = ''
    pythonDetected.value = null
    try {
      const res = await detectPython()
      if (res.ok) {
        pythonDetected.value = res
        if (!pythonPath.value) {
          pythonPath.value = res.path
          command.value = `python -m markitdown "{file}"`
        }
        // 后端已探测 markitdown 路径，直接填入
        if (!markitdownCmd.value && res.markitdownCmd) {
          markitdownCmd.value = res.markitdownCmd
        }
      } else {
        pythonDetectError.value = res.error || '未检测到 Python'
      }
    } catch (e: any) {
      pythonDetectError.value = `✗ ${e.message}`
    } finally {
      pythonDetecting.value = false
    }
  }

  // ───── 测试连接 ─────
  async function handleTest(type: string) {
    testing.value = type
    if (type === 'chat') testChatResult.value = ''
    else if (type === 'embed') testEmbedResult.value = ''
    else if (type === 'rerank') testRerankResult.value = ''
    else testMarkitdownResult.value = ''
    try {
      if (type === 'markitdown') {
        const res = await testMarkitdown(pythonPath.value || 'python3', command.value || 'markitdown')
        testMarkitdownResult.value = res.ok ? '✓ 测试通过' : `✗ ${translateApiError(res.message)}`
      } else if (type === 'embed') {
        testEmbedResult.value = await embed.testConnection()
      } else if (type === 'rerank') {
        testRerankResult.value = await rerank.testConnection()
      } else {
        testChatResult.value = await chat.testConnection()
      }
    } catch (e: any) {
      // 控制器内部已吞掉各自的错误；此 catch 仅兜底 markitdown 传输层异常
      const msg = `✗ ${translateApiError(e.message)}`
      if (type === 'chat') testChatResult.value = msg
      else if (type === 'embed') testEmbedResult.value = msg
      else if (type === 'rerank') testRerankResult.value = msg
      else testMarkitdownResult.value = msg
    } finally {
      testing.value = ''
    }
  }

  return {
    pythonPath,
    command,
    markitdownCmd,
    pythonDetected,
    pythonDetecting,
    pythonDetectError,
    workspacePath,
    scanIntervalSec,
    recentWindowHours,
    autoCommitEnabled,
    autoCommitDebounceSec,
    testing,
    testChatResult,
    testEmbedResult,
    testRerankResult,
    testMarkitdownResult,
    saving,
    savedMsg,
    saveError,
    initing,
    initMsg,
    formReady,
    showWizard,
    onWizardDone,
    initialize,
    retryLoadSettings,
    runPythonDetect,
    handleSaveSecrets,
    handleSaveSettings,
    handleInitWorkspace,
    pickPythonDir,
    pickWorkspaceDir,
    pickMarkitdownExe,
    pickingDir,
    pickMsg,
    pickingMd,
    mdPickError,
    pickingPython,
    handleTest,
  }
}
