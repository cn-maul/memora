# Memora 功能说明文档（基于源码）

> 文档版本：v1.0（对应源码仓库当前状态）
> 数据来源：对 `backend/` 全部 18 个 Go 文件 + `frontend/src/` 全部 Vue 源文件逐模块源码阅读。关键结论均可在源码行号级别追溯，主要引用见文末。
> 项目定位：面向个人办公场景的 Windows 桌面工具，把本地工作目录升级为可版本回溯、可自然语言检索、可智能标签、可时间复盘、可文档问答、可习惯统计的**个人知识管理中心**。
> 运行形态：单可执行文件 `memora.exe`，内嵌静态资源 + 自动打开浏览器访问 `127.0.0.1:xxxx`（端口在 19000–20000 自动探测）。无系统安装依赖（`go-git` 内置，无需 Git）。

---

## 1. 一句话总览

Memora = **本地 Git 版本库**（自动防抖 commit）+ **文档抽取**（MarkItDown 子进程，支持 pdf/docx/txt/md）+ **语义索引**（OpenAI 兼容嵌入模型 → 向量入 SQLite → 内存余弦扫描）+ **智能标签**（LLM 打标 + 手动覆盖）+ **混合搜索**（向量 top-K + 关键词 + 标签过滤）+ **文档问答**（RAG 全局/文件两种模式，SSE 流式输出）+ **时间线/提交管理** + **使用统计** + **设置管理**。

---

## 2. 系统架构（源码事实）

### 2.1 分层

源码按 `design.md` D30/D31 分四层，每层只通过接口契约对接：

```
第四层  UI (frontend，Vue 3 + TS)        只调 REST/SSE
第三层  Adapter (transport + app)        REST 路由 / SSE 广播 / 装配 & 生命周期
第二层  Orchestrator (taskqueue)         单队列 + 单顺序处理器，串行执行所有耗时任务
第一层  Domain (git watch extract index tag search timeline qa stats)
第零层  Infra (config storage llm events)
```

- 装配顺序（`backend/internal/assembler/app.go:75-131`）：`config → events → (llm, git) → 工作区模块组(storage/extract/index/tag/search/timeline/qa/stats/watch) → taskqueue → transport`。
- 启动顺序（`app.go:Run`，`app.go:265-273`）：初始化 Git 仓库 → 全量加载向量到内存 `VectorsLoadAll` → 探测 MarkItDown/LLM/Embed → 恢复上次 `pending` 任务 → 启动 watch → 订阅业务事件 → 开浏览器。
- 关闭顺序（`app.go:585-610`）：Watch 停 → TaskQueue.CancelAll → Wait 3s 排水 → Storage.Close → Transport.Stop。防止运行中的任务访问已关闭的 SQLite。

### 2.2 关键依赖与边界

- **配置**：仅单一文件 `config.json`，字段统一 `snake_case`（`backend/internal/config/config.go:15-69`）。支持从可执行文件旁探测，或迁移到工作目录 `.memora/config.json`（`Relocate`，`config.go:175-236`）。
- **存储**：SQLite（`modernc.org/sqlite`，单连接 WAL 模式），元数据落在工作目录 `.memora/meta.db`；向量同时存 BLOB 与内存 `vectorIndex`（`backend/internal/storage/storage.go:45`、`storage.go:421-560`）。
- **网络边界**：所有路由绑定 `127.0.0.1`（`backend/internal/transport/transport.go:293-304`），无认证层，CORS 全开（`transport.go:306-320`）——这是"本地自包含桌面应用"的安全边界。

---

## 3. 核心功能详解

### 3.1 工作区初始化与管理

**入口流程（首次启动）**
1. 用户通过设置页填写工作区路径、LLM 端点、Embedding 端点、Python/MarkItDown 路径。
2. 前端调用 `POST /api/workspace/init`（`transport.go:586-761`），后端探测 Python/MarkItDown 是否可用（`detect` 逻辑在 `transport.go:1895-1994`），校验 LLM/Embed 端点可达后写入 `config.json`，并调用 `RebuildWorkspace` 重建工作区模块组，全量扫描工作目录重建索引。
3. 前端 `workspace.store.fetchInfo()` 读取 `GET /api/workspace/info`（`transport.go:764-811`）返回 `WorkspaceInfo`：`initialized` / `workspacePath` / `dirtyCounts` / `head` / `markitdownConfigured` / `llmConfigured` / `embedConfigured`。

**工作区重建 `RebuildWorkspace`**（`app.go:217-273`）：原地替换全部工作区模块（storage/extract/index/tag/search/timeline/qa/stats/watch），并异步启动全量重建。

---

### 3.2 文件观察与抽取（watch + extract）

**文件观察 `watch`**（`backend/internal/watch/watch.go`）
- 通过 `fsnotify` 监听工作目录变更，过滤只处理支持的文档类型 `isSupportedDoc`（`watch.go:123-131`，不含 `.doc`）。
- 变更事件产出 `*FileChange` 通道（`IWatch.Changes()`，`contract.go:317-323`），被装配器消费。
- **自动 commit 防抖在 watch 侧**：`dirtyTimer` + `time.Timer.Reset(debounceSec)`，默认 90s 无改动即触发 `auto_commit` 任务（`watch.go:153-160`、`watch.go:183-199`，`config.go:48-50` 默认 90s）。

**文档抽取 `extract`**（`backend/internal/extract/extract.go`）
- 通过 MarkItDown 子进程把文档抽成纯文本（`extract.go:96-185`），子进程超时 60s。
- SHA256 缓存：同一文件同一 hash 复用上次结果，避免重复调用外部进程。
- 支持类型：`pdf / docx / txt / md`。
- `.doc` 双重拒绝：`index.detectDocType` 返回 `"ignored"`（`backend/internal/index/index.go:529-530`，附 D39 注释），`watch.isSupportedDoc` 也不含 `.doc`（`watch.go:123-131`）——UI 侧直接提示用户"另存为 docx"。

---

### 3.3 Git 版本管理（git 模块）

**自动提交**（`backend/internal/git/git.go:103-188`，`CommitAuto`）
1. 校验仓库已初始化；
2. 取工作区 status；
3. **幂等**：若全部状态为空格/0 则 `("", true, nil)` 跳过；
4. stage 指定文件或全部变更；
5. 统计 `A/?`/`M`/`D`，生成消息正文；
6. 用 `GetGitAuthor()` 返回的作者（默认 `Memora <memora@local>`）`Commit`，返回 hash；
7. 异步生成版本摘要并缓存（`contract.go:256-257` 的 `SummariesUpsert`）。

**手动提交**（`CommitManual`，`git.go:192-230`）；支持 AI 备注手动提交（`/api/commits/auto`，`transport.go:1555-1588`）以及 AI 提交信息建议（`/api/commits/suggest`，`transport.go:1733-1745`）。

**历史与差异**：`Log` / `DiffStats` / `FileHistory` / `ShowFileAt` / `DiffContents` / `Head` / `CommitFiles` / `RestoreFile`（`git.go:230-577`）。`DiffContents` 单文件最多 500 字符（`git.go:399`）；二进制探测窗口 8000 字节（`git.go:462-464`）。

---

### 3.4 语义索引（index + storage）

**切块流程**（`backend/internal/index/index.go:150-212`）五步：
1. 段落切分；
2. 超长段（>4000 rune）按中文标点 `。！？；` 分句（`index.go:133-147`）；
3. 贪心合并，上限 `chunkSize + 10%`；
4. 相邻块 overlap `chunkOverlap` rune（默认 256）；
5. 尾部 <200 rune 合并到上一块。

默认 `chunk_size=2000` rune、`chunk_overlap=256`（`config.go:53-55`）。

**向量化**：`ProcessFile`（`index.go:367-424`）分批（每批 16）调 `llm.Embed`，返回 `[][]float32`。

**向量存储**（`backend/internal/storage/storage.go:398-417`）
- `vecToBlob`：`math.Float32bits(v)` + `binary.LittleEndian.PutUint32` → **小端 + float32 位模式**（`storage.go:400-403`）；
- `blobToVec` 逆操作（`storage.go:407-417`）；
- `VectorsInsert`（`storage.go:420-449`）同一事务写入 `chunk_vectors(chunk_id, vec, dim)` 并更新内存索引；
- `VectorsLoadAll`（`storage.go:470-500`）启动时从库全量加载到内存。

**检索**（`VectorsSearch`，`storage.go:503-560`）：内存 `vectorIndex` 上逐条 `cosineSimilarity` 线性扫描，取 top-K。不依赖 SQL 侧向量运算——这是 D10 千级规模够用即可的取舍。

**重建触发三条路径**：启动全量（`app.go:265-270`）、任务队列 `reindex`（`app.go:300-301`）、watch 增量 + pending 兜底轮询（`app.go:276-283`、`app.go:438-439`）。

---

### 3.5 智能标签（tag 模块）

**预定义库**（`backend/internal/tag/tag.go:80-84`）：20 个种子标签：合同 / 报告 / 会议纪要 / 数据 / 图纸 / 简历 / 发票 / 方案 / 清单 / 学习笔记 / 通知 / 制度 / 流程 / 分析 / 审批 / 日程 / 通讯录 / 表单 / 模板 / 其他。`New()` 时自动 `seedPredefined`（`tag.go:61-77`）。

**自动打标 `ProcessFile`**（`tag.go:87-181`）
1. 读取该文件分块，拼接前 8000 Unicode 字符作样本；
2. 样本为空直接返回；
3. 构造系统提示词（`tag.go:116-121`，含标签库 + 禁用词 + JSON 输出格式约束），调 `llm.ChatJSON`；
4. LLM 失败仅打日志不阻塞（`tag.go:132-136`）；
5. 命中标签写 `FileTagsReplace` 且 `Origin="auto"`；
6. 建议的新标签写 `suggestions` 表，广播 `suggestion_new` 事件（`tag.go:155-171`）；
7. 广播 `tag_done`（`tag.go:173-179`）。

**手动覆盖 `ManualOverride`**（`tag.go:184-251`）：支持 `add[]` / `remove[]`，新增标签长度 >20 拒收，`Origin="manual"` 覆盖同名的 `auto` 标签。

**建议确认闭环**：
- `AcceptSuggestion`（`tag.go:268-321`）：幂等建标签 + 应用到来源文件 + 状态 `accepted`；
- `RejectSuggestion`（`tag.go:325-351`）：记录 `rejectCount`，累计 ≥3 次加入 `forbidden` 黑名单——自我纠偏。

---

### 3.6 混合搜索（search 模块）

**流程 `Query`**（`backend/internal/search/search.go:78-204`）
1. 查询向量化；
2. `storage.VectorsSearch` 取 top-20 分块；
3. 文件去重，保组内最高分并计匹配块数；
4. 标签过滤（可选 `tagFilter`）；
5. 若结果携带目标标签则 `score += 0.5`；
6. 按 (score, matchedChunks) 双键排序；
7. 分页返回 `[]SearchResult`。

> 观察：模块声明 `IIndex` 但实际未使用（`search.go:13-16` vs `search.go:30, 88`），所有向量调用走 `storage.VectorsSearch`——契约与实际存在轻微偏差。

---

### 3.7 文档问答（QA 模块，RAG）

**两种模式**（`backend/internal/qa/qa.go:212-317`）
- **文件问答**（`mode=file`，需 `fileID > 0`）：从该文件分块做向量检索，阈值 0.3，取前 8 块；分块总数 ≤ 阈值直发的走全文直发路径。
- **全局问答**（`mode=global`）：跨库检索，阈值 0.01，取前 8 块。

**问句回答流程**
1. 校验 mode + 文件选择；
2. 检索相关分块并拼接上下文（全局 RAG，D21/D22），受 `qa.max_context_chars=30000` 限制（`config.go:58-60`）；
3. 拼提示词调用 `llm.Chat`（流式）；
4. 会话/消息在 LLM 成功后**一次性写入**；
5. 新建会话若 LLM 失败，`QASessionsDelete` 回滚（`qa.go:80-84`、`qa.go:94-100`、`qa.go:148-154`），避免孤立空会话。

> 观察：`NewSesion` / `ClearSesion` 两处方法名拼写错误（`qa.go:327-334`），应修正或标记 deprecated。

---

### 3.8 时间线与提交管理（timeline + transport 提交路由）

`timeline` 模块（`backend/internal/timeline/timeline.go:176-190`）按 `granularity` 聚合：
- **day**：`YYYY-MM-DD`（Go 默认 UTC）；
- **week**：`YYYY-Www`，用 `time.ISOWeek()`，**周一**为周起（`timeline.go:181-183`）；
- **month**：`YYYY-MM`。

> 潜在隐患：day/month 未显式指定本地时区（`timeline.go:176`），跨时区用户看到的日历可能与本地不符。

**提交管理 API**（`backend/internal/transport/transport.go`）
- `POST /commits/auto`（`transport.go:1555-1588`）：AI 备注手动提交，失败回退 `CommitAuto(nil)`；
- `POST /commits/manual`（`transport.go:1688-1731`）：用户自定义 message；
- `POST /commits/suggest`（`transport.go:1733-1745`）：AI 生成提交建议；
- `GET /commits/status`（`transport.go:1663-1686`）：未提交变动列表 `[{relPath, code}] + count`；
- `GET /commits/head`（`transport.go:1590-1602`）：HEAD 概要；
- `GET /commits/list`（`transport.go:1604-1640`）：提交列表，每项含 files 明细；
- `POST /commits/{hash}/summary`（`transport.go:1642-1661`）：为该提交生成摘要。

**前端呈现**：`TimelinePage.vue` 页面 `<h2>` 显示"提交记录"（`TimelinePage.vue:74`），数据源为 `getCommitList`（`TimelinePage.vue:19`）——页面命名（timeline）与实际语义（commits log）不一致。

---

### 3.9 使用统计（stats 模块）

**指标维度**（`backend/internal/stats/stats.go:68-208`，每次 `Summary` 实时重算）
- `commitsByDay`：按天提交数；
- `fileChanges`：`added / modified / deleted` 汇总；
- `hourBuckets`：`morning / afternoon / evening / night` 时段分布；
- `hotFiles`：`fileChangeCount` 排序 Top 10；
- `tagDistribution`：标签使用分布；
- `iterationRate`：`activeFiles / totalDays`，无活动日为 0。

**导出**：`/api/stats/export?format=&range=`（`transport.go:1478-1501`）支持 `csv` / `markdown`，`Content-Disposition: attachment`。

**清零**：`Purge()`（`stats.go:273-277`）仅重置内存 `enabled=true`——**无持久化缓存**，所有指标实时重算；所谓"清零"即切换统计范围。开关可通过 `stats.enabled` 关闭，关闭时 API 返回 `stats_disabled` code（`transport.go:1454`，仍 200）。

---

### 3.10 任务队列（taskqueue）

**机制**（`backend/internal/taskqueue/taskqueue.go:54-163`）
- 单 `tasks chan *Task` + 单 `processLoop` 顺序处理器；
- `running` 原子 CAS 保证只启动一个 `processLoop`；
- **同 `relPath` 去重**：连续提交同一文件的重索引任务会被合并；
- **连续 5 次失败自动暂停**，防止风暴；
- 支持 `Pause` / `Resume` / `CancelAll` / `Status()`；
- 前端通过 `/api/queue/{status,pause,resume}`（`transport.go:1503-1553`）+ `task_queue` SSE 事件感知队列状态。

**任务类型**（在 `app.go` 侧分发，`app.go:300-332`）：`extract`（抽取）/ `reindex`（全量重建）/ `auto_commit`（自动提交）/ `tag`（自动打标）。

---

### 3.11 设置管理

**配置字段结构**（`backend/internal/config/config.go:15-69`，全部 `snake_case`）

| 字段 | 默认值 | 说明 |
|---|---|---|
| `schema_version` | 1 | 配置版本 |
| `workspace_path` | `""` | 工作目录绝对路径 |
| `markitdown.python_path` | `""` | Python 解释器路径 |
| `markitdown.command` | `python -m markitdown "{file}"` | 抽取命令模板 |
| `llm.base_url` / `api_key` / `model` | `""/""/"gpt-4o"` | 问答端点 |
| `llm.temperature` | 0.2 | |
| `embed.base_url` / `api_key` / `model` / `dimensions` | `""/""/"text-embedding-3-large"/1024` | 嵌入端点 |
| `git.author_name` / `author_email` | `Memora` / `memora@local` | |
| `auto_commit.enabled` / `debounce_sec` | `true` / 90 | |
| `index.chunk_size` / `chunk_overlap` / `scan_interval_sec` | 2000 / 256 / 8 | |
| `qa.max_context_chars` / `system_prompt` | 30000 / `""` | |
| `stats.enabled` | `true` | |
| `tray.enabled` | `true` | 系统托盘（v1 未完整实现） |

**保存**（`config.go:297-321`）：`RLock` 读旧值判等 → `Lock` 写内存 → 落盘 → 失败回滚 → 广播 `settings_changed` 事件。

**密钥单独通道**：`PUT /api/settings/secrets`（`transport.go:1752-1769`，`UpsertSecrets`，`config.go:507-519`），快照不回显密钥（`Snapshot`，`config.go:558-505`）。

**热更新**：`PUT /api/settings`（`transport.go:1771-1838`）批量 Set，其中 `markitdown.pythonPath` / `markitdown.command` / `markitdown.markitdownCmd` 直接热更新 `Extract`；`stats.enabled` 同步 `Stats.SetEnabled`；其余需要 `restartRequired` 才生效。

---

### 3.12 LLM 网关（llm 模块）

**统一封装**（`backend/internal/llm/llm.go`）
- `Chat`：调用 `/chat/completions`，支持流式（SSE 逐块 `data: {...}`，`error` 前缀 `"__ERROR__:"` 回传）。
- `ChatJSON`：调 `Chat` + 解析 JSON（用于 tag / commit-summary / commit-suggest）。
- `Embed`：调用 `/embeddings`，支持批量。
- `TestChat` / `TestEmbed`：供设置页一键测试端点。

**限频/重试/超时**（`llm.go:36-44, 104-132, 222-335`）
- `httpClient.Timeout = 60s`；
- 每请求限频 50ms（≈ 20 rps）；
- 最多 3 次重试，1s/2s 指数退避；
- 4xx 视为致命不重试，5xx/429 可重试。

---

### 3.13 事件总线（events）

`backend/internal/events/events.go:28-69`
- 极简 `topic → []Handler` 同步发布订阅，无队列/持久化；
- `Notify` 先复制切片再逐个执行；
- `defer recover` 隔离订阅者 panic；
- `Subscribe` 返回的取消函数通过 `fmt.Sprintf("%p", h)` 函数指针字符串比较移除——理论上极小概率哈希碰撞。

**10 个被 transport 桥接到 SSE 的主题**（`transport.go:227-258`）：`index_progress` / `extract_failed` / `commit_done` / `tag_done` / `suggestion_new` / `files_changed` / `stats_updated` / `settings_changed` / `task_queue` / `qa_ready`。

---

## 4. 通信契约（REST + SSE）

### 4.1 路由表

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/api/workspace/info` | 工作区初始化状态 |
| POST | `/api/workspace/init` | 初始化工作区（含探测） |
| GET | `/api/files` | 分页文件列表 |
| GET | `/api/files/{id}` | 文件详情（含 tags） |
| GET | `/api/files/{id}/text` | 文件全文 |
| GET | `/api/files/{id}/history` | Git 提交历史 |
| POST | `/api/files/{id}/tags` | 手动标签 add/remove |
| POST | `/api/files/{id}/retry` | 失败文件重入队 |
| POST | `/api/files/{id}/restore` | 按 hash 恢复文件 |
| POST | `/api/files/{id}/open` | 系统默认打开 |
| GET | `/api/search` | 语义搜索 |
| POST | `/api/index/reindex` | 全量重建索引 |
| GET | `/api/browse` | 目录浏览 |
| GET | `/api/browse/search` | 文件名实时扫描 |
| POST | `/api/browse/open` | 打开文件 |
| POST | `/api/browse/pickdir` | 弹目录选择框 |
| GET | `/api/tags` | 标签库 |
| GET | `/api/tag-suggestions` | 待处理标签建议 |
| POST | `/api/tag-suggestions/{id}/accept|reject` | 采纳/拒绝建议 |
| GET | `/api/timeline` | 时间线节点 |
| GET | `/api/qa/sessions` | 问答会话列表 |
| POST | `/api/qa` | 一次性问答 |
| POST | `/api/qa/stream` | 流式问答（SSE） |
| GET | `/api/qa/sessions/{id}/messages` | 会话消息 |
| DELETE | `/api/qa/sessions/{id}` | 删除会话 |
| GET | `/api/stats` | 统计指标 |
| GET | `/api/stats/export` | 导出 csv/markdown |
| POST | `/api/commits/auto` | AI 备注提交 |
| POST | `/api/commits/manual` | 手动提交 |
| POST | `/api/commits/suggest` | AI 提交建议 |
| GET | `/api/commits/status` | 未提交变动 |
| GET | `/api/commits/head` | HEAD 概要 |
| GET | `/api/commits/list` | 提交列表 |
| POST | `/api/commits/{hash}/summary` | 提交摘要 |
| GET | `/api/settings` | 设置快照 |
| PUT | `/api/settings` | 批量设置（含 restartRequired） |
| PUT | `/api/settings/secrets` | 密钥更新（不回显） |
| GET | `/api/python/detect` | Python 探测 |
| POST | `/api/test/markitdown` | 测试抽取 |
| POST | `/api/test/llm` | 测试 LLM/Embed |
| GET | `/api/queue/status` | 队列状态 |
| POST | `/api/queue/pause|resume` | 队列控制 |
| GET | `/api/events` | 打开 SSE 长连接 |
| `*` | `/` | SPA 回退到 index.html |

（来源：`backend/internal/transport/transport.go:323-388`）

### 4.2 统一响应

`Response{code, data, message}`（`transport.go:211-216`），错误码表：

| code | HTTP | 语义 |
|---|---|---|
| `ok` | 200 | 成功 |
| `bad_request` | 400 | 参数/方法错误 |
| `internal` | 500 | 服务端错误 |
| `not_found` | 404 | 资源不存在 |
| `workspace_dirty` | 409 | 恢复时工作区有改动 |
| `ai_unavailable` | 422 | AI 端点不可用 |
| `not_init` | 400 | 工作区未初始化 |
| `not_ready` | 503 | 队列未就绪 |
| `stats_disabled` | 200 | 统计已关闭 |

### 4.3 命名约定

- REST 请求/响应 DTO 一律 **camelCase**（`transport.go` 各 handler 内匿名 struct，`InitRequest` 见 `frontend/src/types/index.ts:187-205`）；
- `config.json` 一律 **snake_case**（`config.go:15-69`）；
- 映射只发生在 `config` 与 `transport` 两处（D40）。

### 4.4 SSE

- **广播式**：`GET /api/events`，头 `text/event-stream` / `Cache-Control: no-cache` / `Connection: keep-alive`（`transport.go:482-484`），帧格式 `data: {topic,data}\n\n`（`transport.go:451-459`），15s 心跳 `: ping\n\n`（`transport.go:501-515`）。
- **QA 流式**：`POST /api/qa/stream`，同一套 SSE 头，但使用 `event: error` / `event: done` 私有事件（`transport.go:1330-1403`），前端用 `fetch + ReadableStream` 手动解析实现打字机效果（`frontend/src/api/client.ts:176-252`）。
- **全局 SSE 订阅**：前端 `EventSource('/api/events')` + 3s 自动重连（`client.ts:394-430`）。

---

## 5. 前端功能视图

### 5.1 路由表

前端使用 `createWebHashHistory()`（`frontend/src/router/index.ts:20`），共 6 条真实路由，根路径重定向 `/files`：

| 路径 | 组件 | 说明 |
|---|---|---|
| `/` | → `/files` | 首页跳转 |
| `/files` | `AllFilesPage` | 全部文件列表 + 浏览 |
| `/index` | `IndexPage` | 语义搜索 + 索引管理 |
| `/timeline` | `TimelinePage` | 提交记录（命名与实际语义不一致） |
| `/qa` | `QAPage` | 文档问答 |
| `/stats` | `StatsPage` | 使用统计 |
| `/settings` | `SettingsPage` | 设置 |

> **未接入页面**：`WorkspacePage`（`WorkspacePage.vue` 已实现左目录树 `TreeBranch` 组件）未在路由表中注册（`router/index.ts:9-17`）。

### 5.2 视图功能明细

**`App.vue`（外壳）**：左导航 6 项 + 队列轮询 `refreshQueue` + 自动提交 `handleAutoCommit` + 未提交变更统计 `gitDirtySum` + 手动提交对话框。

**`AllFilesPage.vue`**：工作区未初始化横幅、文件名搜索（调 `browseSearch`）、目录浏览 + 面包屑、文件列表 6 列（名称/类型/大小/修改时间/索引状态/操作）、按扩展名匹配图标与主题色、`?highlight` 与 `window.__highlightFile` 高亮、索引状态标签 `statusLabel/statusClass`。

**`IndexPage.vue`**：语义搜索 + 相似度百分比卡片 + 命中片段 + 标签 + "打开/问答"按钮、标签过滤墙、文件表格（含标签列/索引时间/重试按钮）、重建索引 `handleReindex`、重试失败文件、标签编辑弹窗（diff 后 `updateFileTags`）。

**`QAPage.vue`**：左会话列表（新建/选择/删除）、右对话面板（模式切换"全局/文件问答" + 文件选择下拉）、Markdown 渲染前 HTML 转义再 `marked.parse`、消息气泡（user/assistant 分侧 + sources 卡片）、发送前校验"文件问答必须选文件"、welcome 空态、textarea 支持 Enter/Shift+Enter。

**`TimelinePage.vue`**：刷新按钮、提交列表卡片（message + 时间 + 短 hash + 可展开文件明细，>12 时显示"共 N 个文件改动"）、文件状态 +/-/~ 色签。数据源 `getCommitList`。

**`StatsPage.vue`**：导出 CSV/Markdown、范围切换"本周/本月/本季度"、三张概览卡（文件变更 +/-~ / 迭代速率 / 时段分布）、提交趋势柱状图（`maxCount` 防 NaN）、热点文件、标签分布。

**`SettingsPage.vue`**：LLM 表单（baseUrl/model/temperature/apiKey）、Embedding 表单、Python/MarkItDown 表单、工作区路径与扫描间隔、侧边栏分"工作区/模型" 5 项导航 + 搜索过滤、`IntersectionObserver` 高亮当前可见 section、`loadFromSettings` 双轨兼容 `s.llm.baseUrl` 与扁平 `s.llmBaseUrl`、`retryLoadSettings` 加载兜底。

### 5.3 Store 与状态

| Store | 关键状态 | 职责 |
|---|---|---|
| `files` | `items, total, page=0, pageSize=50, statusFilter, tagFilter, loading` | 分页文件列表 |
| `qa` | `sessions, currentSessionId, messages, sending, abortCtrl, sendSeq` | 会话 + 流式问答（`send` 用 `askQuestionStream` 逐 chunk 追加，完成后重拉含 sources 的完整消息） |
| `settings` | `settings, loading, error` | 设置 CRUD，失败仅记错不清空 |
| `tags` | `tags, suggestions` | 标签库 + 建议 |
| `workspace` | `info, loading, error, initialized, path` | 工作区初始化状态，失败保留上次快照 |

### 5.4 国际化现状

`zh.ts`（`frontend/src/locales/zh.ts`）覆盖完整（`nav/index/timeline/qa/stats/settings/workspace/common` 8 个分组），但**页面模板 100% 硬编码中文**，未见 `$t(...)` 调用——当前国际化资源**未接入**使用。

### 5.5 UI 组件

- **`Icon.vue`**：37 种内联 SVG 图标（`Icon.vue:5-42` 定义 `IconName` + `paths`，`Icon.vue:91-105` 用 `v-html` 渲染）。
- **`TreeBranch.vue`**：递归目录树，`TreeNode` 含 `name/relPath/isDir/docType/expanded/loading/hasLoaded/children`，`toggle` 对目录触发 load、对文件触发 open-file + navigate；按扩展名映射图标颜色、支持搜索高亮、当前路径高亮、缩进；目前仅被 `WorkspacePage` 使用（未挂载路由）。

---

## 6. 数据模型（SQLite 表）

`backend/internal/storage/storage.go` 定义 10 张表：

| 表名 | 用途 |
|---|---|
| `files` | 文件元数据（relPath、size、mtime、contentHash、docType、indexStatus、lastError、firstSeenAt、lastIndexedAt） |
| `chunks` | 分块文本 |
| `chunk_vectors` | 向量 BLOB + dim |
| `tags` | 标签库（含 `source`：predefined/auto/manual） |
| `file_tags` | 文件-标签关联（含 `origin`：auto/manual） |
| `tag_overrides` | 手动覆盖记录 |
| `tag_suggestions` | 建议标签（含 `rejectCount`） |
| `commit_summaries` | 提交摘要 |
| `qa_sessions` | 问答会话 |
| `qa_messages` | 问答消息（含 `sources`） |

---

## 7. 已知的源码事实偏差 / 待改进点

1. **`qa.go` 拼写错误**：`NewSesion` / `ClearSesion` 两处公开方法（`qa.go:327-334`），应为 `NewSession` / `ClearSession`。
2. **`search.go` 声明但未使用的 `IIndex`**：`search.go:13-16` 声明了 `IIndex`，但 `Query` 直接走 `storage.VectorsSearch`（`search.go:88`）。
3. **`TimelinePage` 命名不一致**：路径/路由名 `timeline`，实际展示"提交记录"，数据源 `getCommitList`。
4. **`WorkspacePage` 未接入路由**：源码已实现但 `router/index.ts:9-17` 未注册。
5. **`zh.ts` 国际化未接入**：`$t(...)` 未使用。
6. **时间线时区未显式指定**：`timeline.go:176` day/month 用 Go 默认 UTC。
7. **`top-K` O(n·k) 选择**：`VectorsSearch` 是选择式（`storage.go:531-557`），向量规模增长后性能受限。
8. **`events.Subscribe` 指针字符串比较**：`fmt.Sprintf("%p", h)` 有理论哈希碰撞风险（`events.go:63`）。
9. **`stats.Purge()` 语义与用户预期不符**：仅重置内存 `enabled=true`，无持久缓存可清（`stats.go:273-277`）。
10. **`main.go` 的 `context.WithCancel` 未接入 signal**：当前只驱动 Shutdown，不驱动 Cancel（`main.go:20-25`）。

---

## 附录 A. 关键源码引用索引

- 装配 & 生命周期：`backend/internal/assembler/app.go:75-131, 217-273, 265-332, 438-439, 585-610`
- 入口：`backend/cmd/memora/main.go:16-43`
- 配置：`backend/internal/config/config.go:15-69, 117-156, 175-236, 297-321, 507-519`
- 存储：`backend/internal/storage/storage.go:398-560`
- LLM：`backend/internal/llm/llm.go:36-44, 104-132, 222-335`
- 事件：`backend/internal/events/events.go:28-69`
- Git：`backend/internal/git/git.go:103-251, 230-577`
- Watch：`backend/internal/watch/watch.go:123-199`
- Extract：`backend/internal/extract/extract.go:96-185`
- Index：`backend/internal/index/index.go:133-212, 367-424, 529-530`
- Tag：`backend/internal/tag/tag.go:61-84, 87-181, 184-251, 268-351`
- Search：`backend/internal/search/search.go:78-204`
- Timeline：`backend/internal/timeline/timeline.go:176-190`
- QA：`backend/internal/qa/qa.go:80-154, 212-317, 327-334`
- Stats：`backend/internal/stats/stats.go:68-208, 273-277`
- Transport：`backend/internal/transport/transport.go:211-304, 323-388, 451-515, 586-761, 1330-1403, 1503-1553, 1555-1745, 1748-1994`
- Contract：`backend/internal/contract/contract.go:8-478`
- TaskQueue：`backend/internal/taskqueue/taskqueue.go:54-163`
- 前端路由：`frontend/src/router/index.ts:9-22`
- 前端 API：`frontend/src/api/client.ts:23-430`
- 前端类型：`frontend/src/types/index.ts:3-248`
- 前端视图：`frontend/src/views/*.vue`（详见 §5.2）
