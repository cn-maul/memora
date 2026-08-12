# Memora API 参考（第三方开发接口文档）

> 本文档为第三方开发者提供 Memora 后端的完整 HTTP / SSE 接口契约，全部信息来自源码
> （`backend/internal/transport/` 与 `backend/internal/contract/`），可在对应行号复核。
> 文档版本对应分支 `phase3-converge`（commit `c4495c8` 之后）。

## 目录

- [0. 全局约定](#0-全局约定)
- [1. 可观测性：/health /ready /diagnostics](#1-可观测性health-ready-diagnostics)
- [2. 工作区](#2-工作区)
- [3. 文件](#3-文件)
- [4. 搜索](#4-搜索)
- [5. 索引](#5-索引)
- [6. 文件浏览](#6-文件浏览资源管理器)
- [7. 标签](#7-标签)
- [8. 问答（REST + SSE 流式）](#8-问答rest--sse-流式)
- [9. 统计](#9-统计)
- [10. 提交（Git 版本控制）](#10-提交git-版本控制)
- [11. 设置](#11-设置)
- [12. Python 检测与连通测试](#12-python-检测与连通测试)
- [13. 任务队列](#13-任务队列)
- [14. SSE 全局事件流 /api/events](#14-sse-全局事件流-apievents)
- [15. 核心数据类型](#15-核心数据类型)
- [16. 错误码与 HTTP 状态映射](#16-错误码与-http-状态映射)

---

## 0. 全局约定

| 项目 | 说明 |
|---|---|
| 监听地址 | 仅 `127.0.0.1`，端口 19000–19999 自动探测（`findAvailablePort`） |
| 鉴权 | **无 token / API-Key 鉴权**。唯一防护：CORS 仅放行 `localhost` / `127.0.0.1` / `::1` Origin；外部 Origin 直接 403 空响应 |
| 请求体上限 | 默认 32MB；超限返回 `request_too_large` / 413 |
| 服务端超时 | ReadHeaderTimeout=5s，ReadTimeout=15s，IdleTimeout=90s；无 WriteTimeout（流式需要） |
| 成功响应信封 | `{"code":"ok","data":{...},"requestId":"..."}`；`data` 为 null 或缺省时省略；`requestId` 必填 |
| 错误响应信封 | `{"code":"<code>","message":"<中文>","requestId":"..."}`，可选 `data` |
| 严格解码 | `decodeStrictBody` 端点（workspace/init、/api/qa、/api/qa/stream、settings PUT、settings/secrets）：未知字段 → 400 |

例外：`/health`、`/ready`、`/diagnostics` 为**原始 JSON（无信封）**；文件下载、stats/export、commits content 为**原始字节/文本流**。

### 请求示例（通用）

```bash
# 服务地址（首次启动日志打印）
BASE=http://127.0.0.1:19000

# 成功响应信封
curl "$BASE/api/commits/head"
# => {"code":"ok","data":{"hash":"...","branch":"master","countFiles":3,"changedFiles":1,"hasCommits":true},"requestId":"..."}

# 错误响应信封
curl "$BASE/api/files/resolve?relPath=missing.md"
# => {"code":"not_found","message":"文件未索引","requestId":"..."}
```

---

## 1. 可观测性：/health /ready /diagnostics

### `GET /health` — liveness

不检查任何模块，恒 200。

```json
{"status":"ok"}
```

### `GET /ready` — readiness

检查 storage `Ping()`；若装配层注入了 `GenerationFunc` 则额外检查工作区 generation 非空（未注入时 `generationChecked=false` 且不因 generation 判否）。

- 200：
```json
{"status":"ready","generation":"w1","generationOk":true,"generationChecked":true,"storage":true}
```
- 503（未就绪）：
```json
{"status":"not_ready","generation":"","generationOk":false,"generationChecked":true,"storage":false,"reasons":["workspace_not_initialized","storage_unavailable"]}
```

### `GET /diagnostics` — 诊断摘要

```json
{
  "version": "dev",
  "generation": "w1",
  "queue": {"running": 0, "pending": 0},
  "storage": {"ok": true},
  "cache": {"files": 12, "bytes": 1048576},
  "uptimeSec": 123,
  "recentErrors": []
}
```

---

## 2. 工作区

### `GET /api/workspace/info`

- 请求：无参数
- 响应 `data`：
```json
{
  "initialized": true,
  "workspacePath": "C:/Users/me/docs",
  "dirtyCounts": {"modified": 2, "untracked": 1, "deleted": 0},
  "head": {"hash": "...", "branch": "master", "countFiles": 3, "changedFiles": 1, "hasCommits": true},
  "markitdownConfigured": true,
  "llmConfigured": true,
  "embedConfigured": true
}
```
- 未初始化时：`workspacePath:""`、`dirtyCounts:null`、`head:null`
- 非 GET → `bad_request` / 400

### `POST /api/workspace/init`

初始化工作区：保存配置（`workspace.path`、markitdown/llm/embed/rerank，apiKey 走 `UpsertSecrets`），随后原地重建 runtime 并触发全量索引。**严格解码**（未知字段 400）。

- 请求 body（所有字段可选；`llm` 可为 null）：
```json
{
  "workspacePath": "C:/Users/me/docs",
  "markitdown": {"pythonPath": "C:/Python312/python.exe", "command": "markitdown"},
  "llm": {"baseUrl": "...", "apiKey": "...", "model": "...", "temperature": 0.7},
  "embed": {"baseUrl": "...", "apiKey": "...", "model": "...", "dimensions": 1024},
  "rerank": {"baseUrl": "...", "apiKey": "...", "model": "..."}
}
```
- 校验：workspacePath 为空/不存在/非目录 → `bad_request` / 400（探测/测试失败也 400，且不留半初始化状态）
- 响应 `data`：`{"ok": true}`

---

## 3. 文件

### `GET /api/files` — 文件列表（分页 + 筛选）

| query | 类型 | 默认 | 说明 |
|---|---|---|---|
| `status` | string | 空 | `pending` / `extracting` / `indexed` / `failed` 等 |
| `tag` | string | 空 | 标签名筛选 |
| `page` | int | 0 | 页码（0 起） |
| `pageSize` | int | 50 | 钳制 [1, 500] |
| `sort` | string | `time:desc` | 格式 `field:asc` / `field:desc` |

响应 `data`：
```json
{"page": 0, "pageSize": 50, "total": 120, "items": [FileInfoWithTags]}
```
`FileInfoWithTags` = `FileInfo` + `"tags":[FileTag]`（批量查询，无 N+1）。

### `GET /api/files/{id}` — 文件详情

扁平结构 `FileInfo` + `"tags":[...]`。id 非数字 → 400 `"无效文件 ID"`；不存在 → `not_found` / 404。

### `GET /api/files/{id}/text` — 全文

响应 `data`：`{"text":"<chunks 拼接文本>"}`

### `POST /api/files/{id}/open` — 系统默认程序打开

响应 `data`：`{"ok":true}`

### `GET /api/files/{id}/history` — 版本历史

响应 `data`：`{"fileId":3, "relPath":"doc.md", "commits":[CommitInfo]}`

### `POST /api/files/{id}/tags` — 手动覆盖标签

请求 body：`{"add":["新标签"],"remove":["旧标签"]}`；响应 `data`：`{"tags":[FileTag]}`

### `POST /api/files/{id}/retry` — 失败重试

`failed` → `pending` 并重新入队 extract。响应 `data`：`{"ok":true}`

### `POST /api/files/{id}/restore` — 恢复历史版本

请求 body：`{"commitHash":"<40位sha1>"}`；恢复前自动提交当前改动（自动备份）。
响应 `data`：`{"ok":true,"modified":["<relPath>"]}`

### `GET /api/files/recent` — 最近文件

| query | 类型 | 默认 | 说明 |
|---|---|---|---|
| `window` | int | 24 | 时间窗（小时），0 = 不限 |
| `limit` | int | 50 | 非 (0,200] 时回退 50 |

工作区未初始化 → `not_configured` / 400。响应 `data`：`{"window":24,"items":[FileInfoWithTags]}`

### `GET /api/files/resolve` — 按相对路径解析文件 id

| query | 说明 |
|---|---|
| `relPath` | 必填，正斜杠相对路径 |

响应 `data`：`{"fileId":42}`；未索引 → `not_found` / 404 `"文件未索引"`

### `GET /api/files/download-history` — 下载历史版本

| query | 说明 |
|---|---|
| `relPath` | 必填 |
| `hash` | 必填，40 位 hex SHA-1 |

响应：**原始文件字节流**（非 JSON），`Content-Type: application/octet-stream`，`Content-Disposition: attachment; filename="<base名>"`。缺参/非法 hash → `bad_request` / 400。

---

## 4. 搜索

### `GET /api/search` — 混合语义搜索

| query | 类型 | 默认 | 说明 |
|---|---|---|---|
| `q` | string | 空 | 检索词 |
| `tag` | string | 空 | 单标签过滤 |
| `page` | int | 0 | 页码 |

响应 `data`：
```json
{
  "page": 0,
  "items": [
    {"fileId": 3, "relPath": "doc.md", "hitText": "...", "score": 0.82, "tags": [], "mtime": 1723000000000, "matchedChunks": 2}
  ],
  "total": 5
}
```

---

## 5. 索引

### `POST /api/index/reindex` — 全量重建索引

- 请求：无参数（fire & forget，经任务队列合并执行）
- 前置：工作区未初始化 → `not_configured` / 400
- 响应 `data`：`{"ok":true}`

---

## 6. 文件浏览（资源管理器）

### `GET /api/browse` — 列目录

| query | 说明 |
|---|---|
| `path` | 子目录（空 = 根）；`SafeJoin` 防目录穿越 |

响应 `data`：
```json
{
  "path": "sub",
  "entries": [
    {"name": "a.md", "relPath": "sub/a.md", "isDir": false, "size": 1024, "mtime": 1723000000000, "docType": "md", "indexable": true, "indexStatus": "indexed"}
  ]
}
```
`indexStatus` 仅在可索引文件上回填（indexed / pending / failed）。

### `GET /api/browse/search` — 目录内搜索

| query | 说明 |
|---|---|
| `q` | 必填，大小写不敏感模糊匹配（name 或 relPath） |
| `limit` | 默认 100 |

响应 `data`：`{"query":"x","items":[{"relPath":"...","isDir":false,"size":1,"mtime":1,"docType":"md","indexable":true}],"total":n}`

### `POST /api/browse/open` — 打开文件

请求 body：`{"relPath":"<正斜杠相对路径>"}`（最终路径 containment 校验）。响应 `data`：`{"ok":true}`

### `POST /api/browse/pickdir` — 系统目录选择器

请求 body（可选）：`{"initial":"C:/start"}`；响应 `data`：`{"path":"C:/picked","cancelled":false}`（取消时 `path:""`、`cancelled:true`）。非 Windows 平台返回错误。

---

## 7. 标签

### `GET /api/tags`

响应 `data`：`{"tags":[{"id":1,"name":"报告","source":"auto_generated","count":3,"createdAt":1723000000000}]}`
（`source`: `predefined` / `auto_generated` / `user_confirmed`）

### `GET /api/tag-suggestions`

响应 `data`：`{"suggestions":[TagSuggestion]}`（仅返回 pending 列表）。

`TagSuggestion`：`{"id":1,"name":"待定标签","reason":"...","fileId":3,"relPath":"doc.md","status":"pending","createdAt":...}`

### `POST /api/tag-suggestions/{id}/accept` / `POST /api/tag-suggestions/{id}/reject`

响应 `data`：`{"ok":true}`；不存在或已处理 → `not_found` / 404 `"建议不存在或已处理"`。

---

## 8. 问答（REST + SSE 流式）

请求 DTO `QARequest`：`{"sessionId":0,"mode":"global","fileId":0,"question":"..."}`
- `mode`: `file` | `global`（必填）
- `sessionId`: 0 = 新建会话
- `fileId`: `mode=="file"` 时必须 > 0

### `GET /api/qa/sessions`

响应 `data`：`{"sessions":[{"id":1,"createdAt":...,"mode":"global","fileId":0,"messageCount":2}]}`

### `POST /api/qa` — 非流式问答

- 请求 body：`QARequest`（严格解码）
- 校验：question 空 → 400 `"问题不能为空"`；`file` 模式无 fileId → 400 `"文件问答需要先选择文件"`
- 响应 `data`：
```json
{"answer":"...","sources":[{"relPath":"doc.md","seq":3}],"sessionId":1}
```

### `POST /api/qa/stream` — 流式问答（SSE）

- 请求 body：`QARequest`（严格解码），校验同上
- 响应：`Content-Type: text/event-stream`，帧序列：

```text
data: "增量文本chunk1"                          ← 每个 chunk 为 JSON 编码字符串
data: "增量文本chunk2"

event: done
data: {"done":true,"sessionId":1,"sources":[{"relPath":"doc.md","seq":3}]}

event: error
data: "错误消息"                                 ← JSON 编码字符串
```

- 客户端断开（`r.Context().Done()`）即取消
- 每个 chunk 写空闲超时 60s

### `GET /api/qa/sessions/{id}/messages`

响应 `data`：`{"messages":[{"id":1,"sessionId":1,"role":"user","content":"...","sources":"","createdAt":...}]}`
（`sources` 为 JSON 字符串，`role`: `user` | `assistant`）

### `DELETE /api/qa/sessions/{id}`

响应 `data`：`{"ok":true}`

---

## 9. 统计

### `GET /api/stats`

| query | 说明 |
|---|---|
| `range` | `week` / `month` / `quarter` / `custom` |
| `from` / `to` | int64 毫秒（custom 用） |

- 统计关闭 → 200 `{"code":"stats_disabled","message":"统计已关闭","data":{"enabled":false}}`
- 工作区未初始化 → `not_configured` / 400
- 响应 `data`：
```json
{
  "enabled": true,
  "metrics": {
    "commitsByDay": [{"date":"2026-08-12","count":3}],
    "fileChanges": {"added": 1, "modified": 2, "deleted": 0},
    "hotFiles": [{"relPath":"doc.md","count":5}],
    "hourBuckets": {"morning": 2, "afternoon": 3, "evening": 1, "night": 0},
    "tagDistribution": [{"tag":"报告","count":3}],
    "iterationRate": 0.7
  }
}
```

### `GET /api/stats/export`

| query | 说明 |
|---|---|
| `format` | `csv` → `text/csv`；否则 `text/markdown` |
| `range` | 同 stats |

响应：**原始文本**（非 JSON 信封），`Content-Disposition: attachment; filename=report_<unix>.<format>`。

---

## 10. 提交（Git 版本控制）

### `POST /api/commits/auto` — 自动提交

无 body。先尝试 AI 生成备注；AI 不可用则回退普通提交。
- AI 成功：`{"hash":"...","message":"...","ai":"true"}`
- 回退且无变更：`{"skipped":true}`
- 回退已提交：`{"hash":"..."}`

### `POST /api/commits/manual` — 手动提交

请求 body：`{"message":"备注"}`（空备注自动生成 `"手动保存：修改 n 个文件/新增 n 个/删除 n 个"`）。无变更 → `{"skipped":true,"hash":""}`（不报错）。成功 → `{"hash":"...","message":"..."}`

### `POST /api/commits/suggest` — AI 生成提交备注建议

响应 `data`：`{"suggestion":"<一句话中文备注>"}`；AI 失败 → `ai_unavailable` / 422 `"AI 服务暂不可用"`

### `GET /api/commits/status` — 变更状态

响应 `data`：`{"files":[{"relPath":"doc.md","code":"M"}],"count":1}`
（`code`: `M` 修改 / `D` 删除 / `A` 新增 / `??` 未跟踪）

### `GET /api/commits/head` — HEAD 概要

响应 `data`：`HeadInfo`：`{"hash":"...","branch":"master","countFiles":3,"changedFiles":1,"hasCommits":true}`

### `GET /api/commits/list` — 提交列表

| query | 说明 |
|---|---|
| `withFiles` | `"true"` 时附带每个提交的文件明细（默认不附带） |

响应 `data`：
```json
{"commits":[{"hash":"...","time":1723000000000,"message":"...","author":"...","files":[{"path":"doc.md","status":"modified"}]}]}
```
（`status`: `added` | `modified` | `deleted`）

### `GET /api/commits/{hash}/files` — 提交快照文件清单

hash 非 40 位 hex → 400 `"无效提交哈希"`。响应 `data`：`{"hash":"...","files":[{"path":"doc.md","size":1024,"docType":"md"}]}`

### `GET /api/commits/{hash}/diff` — 提交改动文件

响应 `data`：`{"hash":"...","files":[CommitFile]}`

### `GET /api/commits/{hash}/content?path=...` — 读取版本内文件内容

`path` 必填。响应：**原始文本** `Content-Type: text/plain; charset=utf-8`。版本中无此文件 → `not_found` / 404 `"该版本中不存在此文件"`。

### `POST /api/commits/{hash}/summary` — 生成 AI 提交摘要

响应 `data`：`{"summary":"..."}`；失败 → `not_found` / 404 `"该版本无法生成总结"`。

---

## 11. 设置

### `GET /api/settings`

响应 `data`（配置快照，**不含任何 apiKey**）：
```json
{
  "schemaVersion": 1,
  "workspacePath": "C:/Users/me/docs",
  "markitdown": {"pythonPath": "...", "command": "markitdown", "markitdownCmd": "..."},
  "llm": {"baseUrl": "...", "model": "...", "temperature": 0.7},
  "embed": {"baseUrl": "...", "model": "...", "dimensions": 1024},
  "git": {"authorName": "...", "authorEmail": "..."},
  "autoCommit": {"enabled": true, "debounceSec": 90},
  "index": {"chunkSize": 800, "chunkOverlap": 100, "scanIntervalSec": 60},
  "recent": {"windowHours": 24},
  "rerank": {"baseUrl": "...", "model": "..."},
  "qa": {"maxContextChars": 4000, "systemPrompt": "..."},
  "stats": {"enabled": true},
  "tray": {"enabled": true}
}
```

### `PUT /api/settings` — 更新配置

请求 body：任意扁平 `key → value` map，key 用点分路径（如 `"llm.temperature"`）。**严格解码**（未知字段 400）。

```json
{"llm.temperature": 0.5, "stats.enabled": true}
```

- 行为：`embed.dimensions` 变化且库内有存量向量时自动触发全量重建；`stats.enabled` 同步 stats 模块；markitdown 三项热更新
- 响应 `data`：`{"ok":true,"restartRequired":["tray.enabled"],"reindexRequired":true}`
- Config.Set 失败 → `bad_request` / 400 `"保存配置失败"`

### `PUT /api/settings/secrets` — 更新 API Key

请求 body：`{"llmApiKey":"...","embedApiKey":"...","rerankApiKey":"..."}`（非空即 upsert，走 `UpsertSecrets`，DPAPI 加密存储）。**严格解码**。响应 `data`：`{"ok":true}`

---

## 12. Python 检测与连通测试

### `GET /api/python/detect`

检测系统 Python（Windows 候选路径 + PATH 的 python/python3/py，跳过 WindowsApps Store 壳）并推导 markitdown.exe。

响应 `data`：
```json
{"results":[{"path":"C:/Python312/python.exe","ok":true,"version":"3.12.2","markitdownCmd":"markitdown","error":""}]}
```
未找到：`{"results":[{"path":"","ok":false,"error":"未找到可用的 Python 解释器"}]}`

### `POST /api/test/markitdown` — 探测抽取命令

请求 body：`{"pythonPath":"...","command":"markitdown"}`；响应 `data`：`{"ok":true,"message":"..."}`

### `POST /api/test/llm` — 测试模型连通 / 列出模型

请求 body：
```json
{
  "type": "chat | embed | rerank | models",
  "kind": "chat | embed | rerank",
  "baseUrl": "...", "model": "...", "apiKey": "...", "temperature": 0.7
}
```
- `type=="models"`：成功 → `{"ok":true,"models":["model-a","model-b"]}`
- 其他：成功 → `{"ok":true,"message":"测试通过"}`
- **失败一律返回 200** `{"ok":false,"message":"<错误>"}`（非错误信封）
- 其他路径 → 400 `"不支持的测试类型"`

---

## 13. 任务队列

前置：TaskQueue 未就绪 → `not_ready` / 503 `"任务队列未就绪"`。

### `GET /api/queue/status`

响应 `data`：`{"running":1,"pending":2,"paused":false}`

### `POST /api/queue/pause` / `POST /api/queue/resume`

响应 `data`：`{"ok":true}`

---

## 14. SSE 全局事件流 /api/events

- **请求**：无参数，`GET`（无方法限制）
- **响应头**：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`Connection: keep-alive`
- **帧格式**：`data: {"topic":"<topic>","data":{...}}\n\n`；心跳帧 `: ping\n\n`
- **心跳**：每 15s；帧间空闲写超时 30s 断开；channel 缓冲 16，塞满即断开该连接
- **注意**：事件**不带 `event:` 名**，topic 在 data 内（与 QA 流式接口的 `event:` 格式不同）

```json
data: {"topic":"index_progress","data":{"phase":"processing","done":42,"total":500,"current":"doc.md"}}

data: {"topic":"files_changed","data":{"added":["a.md"],"modified":["b.md"],"removed":[]}}

data: {"topic":"qa_ready","data":{"sessionId":3}}
```

### 全部 topic 与 payload

| topic | payload | 说明 |
|---|---|---|
| `index_progress` | `{"phase":"reset","total":n}` / `{"phase":"processing","done":n,"total":n,"current":"<relPath>"}` / `{"phase":"done","total":n}` / `{"done":true,"fileId":n,"relPath":"..."}`（单文件完成） | 全量/单文件索引进度 |
| `extract_failed` | `{"relPath":"...","error":"..."}` | 抽取失败 |
| `commit_done` | `{"hash":"...","files":[...]}` / `{"hash":"..."}` / `{"auto":true}`（恢复前自动备份） | 提交完成 |
| `tag_done` | `{"fileId":n,"relPath":"...","tags":["a","b"]}` | 标签完成（tags 为 `[]string`） |
| `suggestion_new` | `{"id":n,"name":"...","reason":"..."}` | 新标签建议 |
| `files_changed` | `{"added":[...],"modified":[...],"removed":[...]}` | 工作区文件变化 |
| `settings_changed` | `{"key":"<点分配置键>"}` | 配置变更 |
| `task_queue` | `{"running":n,"pending":n,"paused":bool}` / `{"running":0,"pending":0,"paused":false,"error":"...","failedType":"<type>"}` | 队列状态/类型封禁 |
| `qa_ready` | `{"sessionId":n}` | 问答完成（非流式） |
| `stats_updated` | —（已订阅，当前无 Notify 调用点，不会触发） | 统计更新 |

---

## 15. 核心数据类型

| 类型 | 字段 |
|---|---|
| `FileInfo` | `id`(int64), `relPath`, `size`(int64), `mtime`(int64 毫秒), `contentHash`?, `docType`, `indexStatus`, `lastError`?, `firstSeenAt`(int64), `lastIndexedAt`? |
| `Chunk` | `id`(int64), `fileId`(int64), `seq`(int), `tokenEst`(int), `text` |
| `FileTag` | `name`, `origin`(`auto`/`manual`) |
| `TagInfo` | `id`(int64), `name`, `source`(`predefined`/`auto_generated`/`user_confirmed`), `count`?, `createdAt`(int64) |
| `TagSuggestion` | `id`(int64), `name`, `reason`?, `fileId`(int64), `relPath`?, `status`(`pending`/`accepted`/`rejected`), `createdAt`(int64) |
| `CommitInfo` | `hash`, `time`(int64), `message`, `author` |
| `CommitFile` | `path`, `status`(`added`/`modified`/`deleted`) |
| `VersionFile` | `path`, `size`(int64), `docType`? |
| `HeadInfo` | `hash`, `branch`?, `countFiles`(int), `changedFiles`(int), `hasCommits`(bool) |
| `QASession` | `id`(int64), `createdAt`(int64), `mode`(`file`/`global`), `fileId`?, `messageCount`(int) |
| `QAMessage` | `id`(int64), `sessionId`(int64), `role`(`user`/`assistant`), `content`, `sources`?(JSON 字符串), `createdAt`(int64) |
| `QARequest` | `sessionId`?(int64), `mode`(`file`/`global`), `fileId`?, `question` |
| `QAResponse` | `answer`, `sources`:[{`relPath`,`seq`}], `sessionId`(int64), `error`? |
| `SearchResult` | `fileId`(int64), `relPath`, `hitText`, `score`(float64), `tags`:[FileTag], `mtime`(int64), `matchedChunks`(int) |
| `StatsMetrics` | `commitsByDay`:[{`date`,`count`}], `fileChanges`:{`added`,`modified`,`deleted`}, `hotFiles`:[{`relPath`,`count`}], `hourBuckets`:{`morning`,`afternoon`,`evening`,`night`}, `tagDistribution`:[{`tag`,`count`}], `iterationRate`(float64) |
| `QueueStatus` | `running`(int), `pending`(int) |

---

## 16. 错误码与 HTTP 状态映射

权威来源：`backend/internal/contract/errors.go` + transport 层（`responses.go`、各 handler）。

| code | HTTP | 场景 |
|---|---|---|
| `ok` | 200 | 成功（信封 `code` 字段） |
| `bad_request` | 400 | 参数缺失/格式错误、body 解析失败、workspace 探测失败 |
| `invalid_param` | 400 | 定义存在，当前 transport 未使用 |
| `not_configured` | 400 | 工作区未初始化 |
| `not_found` | 404 | 文件/会话/提交不存在 |
| `request_too_large` | 413 | 请求体超 32MB（transport 层非标准码） |
| `unauthorized` | 401 | 定义存在，当前未使用（无鉴权） |
| `forbidden` | 403 | CORS 拦截（外部 Origin，裸 403 无 body） |
| `not_ready` | 503 | 任务队列未就绪（transport 层非标准码） |
| `ai_unavailable` | 422 | AI 服务暂不可用（提交备注建议） |
| `stats_disabled` | 200 | 统计已关闭（业务码，HTTP 200 + `code` 字段） |
| `conflict` | 409 | 定义存在，当前未使用 |
| `rate_limited` | 429 | 定义存在，当前未使用 |
| `timeout` | 504 | 定义存在，当前未使用 |
| `canceled` | 499 | 定义存在，当前未使用 |
| `llm_error` | 502 | 定义存在，当前未使用 |
| `extract_error` | 422 | 定义存在，当前未使用 |
| `internal` | 500 | 未知错误归一（仅返回固定文案，不泄露内部细节） |

### 错误信封示例

```json
{"code":"not_found","message":"文件未索引","requestId":"MTcyMzAwMDAwMDAwMDAwMDAw"}
```

`requestId` 同时写入响应头 `X-Request-ID`；后端日志同请求贯穿同一 `operationId`（HTTP、任务、LLM、索引均记录开始/结束与耗时）。排查方式：取 `requestId` → 按 `requestId`/`operationId` 过滤日志 → 沿 `ts level module msg durationMs errorCode retryable` 字段还原操作链。

---

*本文档由源码自动核对生成；若前端 `frontend/src/api/client.ts` 与后端行为不一致，以后端 transport handler 为准。*
