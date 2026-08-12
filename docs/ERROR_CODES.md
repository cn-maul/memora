# Memora 错误码表

> 来源：`backend/internal/contract/errors.go`（统一错误模型 P1-13，稳定契约）与整改计划 §6。
> 权威实现：`contract.StatusForCode` 集中映射 code → HTTP 状态；未知错误经 `contract.AsAppError` 归一为 `internal`。
> 前端对齐：`frontend/src/utils/errors.ts` 的 `ERROR_CODES` 常量与后端保持一致，未知 code 一律归一为 `internal`。

## 1. 错误响应格式

所有 REST 错误响应使用统一信封（`backend/internal/transport/responses.go`）：

```json
{
  "code": "llm_error",
  "message": "模型服务调用失败，请检查模型配置后重试",
  "requestId": "xxxxxxxxxxxxxxxx"
}
```

- `code`：稳定错误码（下表），前端据此映射文案与重试建议，**不解析原始 body/Blob**。
- `message`：面向用户的中文可执行建议，不含 SQL、Go 错误文本或内部路径。
- `requestId`：本次请求 ID，也写入响应头 `X-Request-ID`；前端展示后可用于日志回溯（见 §5）。
- 500 类错误只返回 `code/message/requestId`；内部细节（`Err`、路径、SQL、远端响应）只进日志。

## 2. 实际错误码表

以下 code 为 `backend/internal/contract/errors.go` 中的真实常量（**稳定契约，勿随意改名**）：

| code | HTTP 状态 | 含义 | 用户建议操作 |
|---|---|---|---|
| `bad_request` | 400 | 请求体不合法 / 业务校验失败（最多见） | 检查输入（路径、字段、必填项）后重试；升级后出现请重启确认配置 |
| `invalid_param` | 400 | 参数非法（范围/格式错误） | 修正参数值后重试 |
| `not_configured` | 400 | 未配置（如模型未配置、Python/抽取环境缺失） | 前往「设置」完成 AI 或抽取环境配置 |
| `not_found` | 404 | 资源不存在（文件、版本、会话、建议等） | 刷新列表确认资源存在；文件可能已被移动/删除 |
| `unauthorized` | 401 | 凭据缺失或无效 | 检查设置中的 API Key / 模型凭据 |
| `forbidden` | 403 | 无权限访问 | 确认操作对象在权限范围内（本机 CORS 已拦截跨源请求） |
| `conflict` | 409 | 状态冲突（如提交冲突、恢复冲突、并发修改） | 刷新状态后重试；恢复版本遇未跟踪文件冲突时先处理冲突 |
| `rate_limited` | 429 | 限流（模型上游或本地任务限制） | 稍后重试，或降低请求频率 |
| `timeout` | 504 | 上游超时（LLM / 抽取 / 任务） | 稍后重试；持续出现请检查网络与模型端点 |
| `canceled` | 499 | 请求被取消（客户端断开/会话切换） | 属预期行为；确认网络稳定后重发 |
| `llm_error` | 502 | AI 上游错误（未配置、认证失败、限流、超时、协议错误） | 检查「设置」中 provider/model/API Key 与网络；按提示重试 |
| `extract_error` | 422 | 文档抽取失败（不支持格式或抽取器错误） | 确认文件格式受支持（pdf/docx/pptx/xlsx/txt/md）；重新索引该文件 |
| `internal` | 500 | 未分类内部错误（固定文案，不泄露细节） | 记录界面 `requestId`，按 §5 定位日志后联系维护者 |

## 3. 传输层补充错误码

除 `contract` 常量外，transport 层直接写出的错误码（同信封）：

| code | HTTP 状态 | 含义 | 用户建议操作 |
|---|---|---|---|
| `request_too_large` | 413 | 请求体超过大小上限（`decodeStrictBody` 触发） | 减少提交内容/请求大小后重试 |
| `not_ready` | 503 | 服务/任务队列未就绪 | 等待初始化完成；检查 `/ready` 的 `reasons` 定位原因 |

> SSE 流式问答的异常中断：前端按「连接中断」处理并显示恢复建议，不视为普通 HTTP 错误。

## 4. 分域对照

整改计划 §6 建议的语义分域为演进方向，与当前扁平 code 的对应关系如下（新错误码按此域命名演进，如 `validation.*`、`ai.*`）：

| 计划分域 | 覆盖的错误码 |
|---|---|
| `validation.*` | `bad_request`、`invalid_param`、`not_configured`、`request_too_large` |
| `workspace.*` | `not_ready`（未初始化/切换中/队列未就绪） |
| `index.*` | `extract_error`、`canceled`、`timeout` |
| `git.*` | `conflict`、`not_found`、`forbidden` |
| `ai.*` | `llm_error`、`rate_limited`、`timeout`、`unauthorized` |
| `storage.*` | `internal`（迁移/事务失败归入日志排查）、`not_ready` |
| `stream.*` | `canceled`、SSE 异常中断（连接中断） |
| `internal.unexpected` | `internal` |

## 5. 用 requestId 排查

- **前端**：统一从信封取 `code/message/requestId`，错误区域展示 `requestId` 与重试按钮（`frontend/src/utils/errors.ts` 的 `unwrapData` / `parseEnvelope`）。
- **后端日志**：同一请求的整条操作链贯穿同一 `operationId`（HTTP → 任务 → LLM → 索引均记录开始/结束与耗时）；日志字段含 `ts level component event requestId operationId ... durationMs outcome errorCode retryable`。
- **定位步骤**：取界面 `requestId` → 在日志中按 `requestId`（HTTP 层）与 `operationId`（任务链）过滤 → 沿 `errorCode` + `err`（仅日志）定位根因。
- 诊断摘要：`GET /diagnostics` 返回版本、generation、队列深度、storage 可用性、缓存体积、uptime，可辅助判断是环境问题还是业务问题。

## 6. 前端呈现规则

- API client 只接受 `unknown`，集中解析；下载 Blob 根据 content-type 解析 JSON 错误，聊天气泡禁止展示完整 HTTP body。
- store 标准状态 `idle | loading | ready | refreshing | error`，保留上次成功数据，不在 catch 中清空数据伪装成功。
- 「无数据」与「请求失败」是不同状态；写操作（设置、提交、恢复）只有后端确认成功后才提示成功。
