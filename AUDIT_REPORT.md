# Memora 全盘问题审计

> **历史快照（已失效）**：本报告对应整改启动前的审计基线（`main`/`c9fe01d`），
> 其中的问题清单与行号已被 `docs/SYSTEM_REMEDIATION_PLAN.md` 台账（P0-P3）覆盖，
> 并在 Phase 0-5 中逐项整改。不再作为现状依据，仅留档。

> 审计日期：2026-08-06  
> 审计范围：Go 后端、Vue 前端、REST/SSE 契约、核心数据流及本地运行验证  
> 审计方式：静态代码审查、`go test ./...`、`go vet ./...`、前端生产构建、临时配置启动、HTTP 路由冒烟测试  
> 说明：本次仅审计并记录问题，未修改产品代码。实际启动使用独立临时配置，未初始化或修改真实工作区。
> 后续处理：报告中的 24 项问题与第 7 节契约偏差已在后续迭代中修复（代码内留有"修复 B-xx/H-xx/M-xx"注释）；2026-08-10 二次审计又发现并修复了 taskqueue 竞态/永久封禁、git 并发、N+1 查询、Embed 向量归位、FullReindex 幽灵索引、标签建议前端接线等问题。本报告保留作为问题清单与修复对照，具体现状以代码为准。

## 1. 结论

当前版本不能视为可用的首版产品。主要原因不是某一个页面的小缺陷，而是首次初始化、配置持久化、后台任务恢复、索引与自动标签等主链路存在系统性断点；与此同时，前端没有接入文件详情、内容搜索、标签建议、文件问答目标选择等核心能力。

经去重后，本审计记录 **24 个问题**：

| 严重度 | 数量 | 含义 |
|---|---:|---|
| 阻断 | 5 | 直接导致首次使用、配置或后台处理主流程无法工作 |
| 高 | 9 | 核心功能不可用、数据可能串档，或对用户行为产生严重误导 |
| 中 | 8 | 错误不可见、状态误判、部分流程或交互明显失效 |
| 低 | 2 | 边界显示或类型契约问题 |

优先修复顺序应为：**初始化与配置定位 → 队列恢复 → 路由 → 索引正确性 → watch 过滤 → 文件详情/搜索/标签/问答前端接线 → 错误状态与测试**。

## 2. 已执行验证

| 检查 | 结果 |
|---|---|
| `npm run build` | 通过，Vue 类型检查与 Vite 构建成功 |
| `go test ./...` | 命令通过，但所有包均显示 `[no test files]` |
| `go vet ./...` | 通过 |
| 临时配置启动 | 成功，服务监听 `127.0.0.1:19000`，首页返回 200 |
| `PUT /api/settings/secrets` | 实测 404 |
| `POST /api/tag-suggestions/1/accept` | 实测 404 |
| 未初始化时 `/api/stats`、`/api/timeline` | 实测 500 |
| 未带 `fileId` 的 file 模式问答 | 实测进入全局嵌入路径并返回模型未配置错误 |

静态检查通过只能说明代码可编译；目前没有任何项目测试覆盖初始化、配置迁移、路由、索引、任务队列或前后端契约。

## 3. 阻断问题

### B-01 首次初始化后，当前进程仍绑定空工作区和错误数据库

- **位置**：`backend/internal/assembler/app.go:99`、`:146`、`:155`、`:169`；`backend/internal/transport/transport.go:577`、`:633`
- **触发**：首次启动时配置没有工作区，再从设置页调用 `POST /api/workspace/init`。
- **现象与影响**：接口只更新 Config；启动时已经构造好的 Storage、Extract、Index、Timeline 和 Watch 不会重建。Storage 仍指向可执行文件旁的 `.memora`，Index/Timeline 的 workspace 仍为空，Watch 仍为 nil。随后异步 `FullReindex()` 扫描空路径，首次初始化后索引和监听无法正常工作。
- **修复建议**：把工作区初始化提升到 App 生命周期边界。初始化成功后原子地停止旧模块、按新工作区重建所有工作区相关模块及 transport 依赖，再启动 watch 和重建索引；更简单的临时方案是接口明确返回“配置已保存，请重启”，且重启后必须正确定位工作区配置。

### B-02 配置迁移后，下次启动仍读取旧配置，模型配置与密钥丢失

- **位置**：`backend/internal/assembler/app.go:72`；`backend/internal/config/config.go:145-184`；`backend/internal/transport/transport.go:577-624`
- **触发**：首次初始化工作区，随后保存 LLM、Embedding 与密钥，再重启。
- **现象与影响**：默认启动路径永远是“可执行文件目录/.memora/config.json”。`Relocate` 只复制旧配置后切换当前写路径，不删除或更新旧入口配置；而模型配置和密钥又是在 Relocate 之后写入工作区配置。重启时仍加载旧文件，表现为模型和密钥消失，搜索、索引、问答、摘要全部失效。
- **修复建议**：确定唯一引导配置策略。可在入口配置只保存 `workspace_path`，启动后继续加载 `<workspace>/.memora/config.json`；或真正移动配置并在旧位置写稳定指针。迁移必须原子写入且具备重启测试。

### B-03 后台任务连续失败五次后永久暂停，用户无法恢复

- **位置**：`backend/internal/taskqueue/taskqueue.go:120-150`；`backend/internal/transport/transport.go:297-343`
- **触发**：例如 Embedding 配置错误，连续五个索引任务失败。
- **现象与影响**：队列设置 `paused=true` 后等待 `Resume()`；但 transport 没有 TaskQueue，也没有暂停/恢复接口。第五个失败任务因 `continue` 跳过 pending key 清理，即使内部恢复也不能重新提交同任务。只能重启。
- **修复建议**：第五次失败前先清理当前去重键；按任务族计数而不是全局混算；增加状态、暂停、恢复和重试失败项 API，并在 UI 显示真实错误与恢复按钮。

### B-04 保存 API 密钥的路由未注册，按钮必然 404

- **位置**：`backend/internal/transport/transport.go:335-337`、`:1250-1271`；`frontend/src/api/client.ts:206-208`
- **触发**：设置页点击“保存密钥”。
- **现象与影响**：前端请求 `PUT /api/settings/secrets`；Go ServeMux 只精确注册 `/api/settings`，请求不会进入处理器内部的 `/secrets` 分支。已通过运行实测确认 404。
- **修复建议**：显式注册 `/api/settings/secrets`，并为方法、空密钥、保留已有密钥及响应体添加契约测试。

### B-05 文件问答没有文件选择，静默退化为全局问答

- **位置**：`frontend/src/views/QAPage.vue:16-22`、`:67-70`；`backend/internal/qa/qa.go:79-142`
- **触发**：用户切换到“文件问答”后提问。
- **现象与影响**：前端没有文件选择控件，也不发送 `fileId`。后端只有在 `mode == "file" && fileId > 0` 时进入单文件逻辑，否则走全局 RAG。界面声称文件问答，实际检索全库，属于严重的结果范围误导。
- **修复建议**：file 模式必须强制选择已索引文件；前后端均校验 `fileId`，缺失时返回 `bad_request`，禁止静默降级。

## 4. 高严重度问题

### H-01 标签建议接受/拒绝路由也未注册

- **位置**：`backend/internal/transport/transport.go:315-318`、`:985-1034`；`frontend/src/api/client.ts:120-126`
- **影响**：`/api/tag-suggestions/{id}/accept|reject` 实测 404；即便前端后续接线也无法使用。
- **修复建议**：注册 `/api/tag-suggestions/` 子树路由，并验证严格路径和方法。

### H-02 自动打标永远不会触发

- **位置**：`backend/internal/index/index.go:417-422`；`backend/internal/assembler/app.go:374-386`
- **原因**：index 事件里的 `fileId` 是 Go `int64`，内存订阅者却只断言 `float64`。这里没有 JSON 编解码，不会发生数值类型转换。
- **影响**：所有成功索引文件都不会提交 tag 任务，自动标签和标签建议主链路完全失效。
- **修复建议**：使用强类型事件 DTO，或至少在订阅处处理 `int64/int/float64/json.Number`；增加“索引完成后产生标签任务”的集成测试。

### H-03 全量重建可能把内容写入错误文件 ID

- **位置**：`backend/internal/storage/storage.go:162-186`；`backend/internal/index/index.go:236-251`
- **原因**：`INSERT ... ON CONFLICT DO UPDATE` 命中已有行时，代码仍使用 `LastInsertId()` 作为该文件 ID。SQLite 不会把它更新成冲突行 ID。
- **影响**：再次全量重建时可能更新、清空或写入其他文件的 chunks/vectors，产生索引串档或失败。
- **修复建议**：upsert 后按 `rel_path` 查询并返回真实 ID，或使用支持的 `RETURNING id`。必须加入已有多文件数据库上的重建回归测试。

### H-04 watch 会把目录和所有不支持类型送入索引

- **位置**：`backend/internal/watch/watch.go:140-155`、`:211-222`；`backend/internal/index/index.go:267-305`
- **影响**：新建目录、修改图片/源码/压缩包等都会被记录为 modified。增量索引不检查 `detectDocType()` 结果，目录会在 `os.ReadFile` 失败，不支持文件会被送入 MarkItDown；连续失败还能触发队列永久暂停。
- **修复建议**：watch 入队前排除目录和不支持扩展名；Incremental 再做一次防御校验；`.doc` 应明确置 ignored 并给提示。

### H-05 资源管理器中的普通文件与搜索结果不可操作

- **位置**：`frontend/src/views/WorkspacePage.vue:221-225`、`:278-290`
- **影响**：目录只能展开；文件点击无行为，搜索结果也不可点击。用户无法打开外部文件、查看详情、文本、历史、索引错误或进入问答。
- **修复建议**：添加文件详情抽屉或页面，至少提供“打开、索引状态/错误、历史、问答、标签”入口；搜索结果与目录列表复用同一文件操作。

### H-06 后端“打开文件”接口实际上什么都不做

- **位置**：`backend/internal/transport/transport.go:778-786`
- **影响**：即使前端接入 `POST /api/files/{id}/open`，处理器只验证数据库记录后直接返回 `{ok:true}`，没有调用 Windows 外部打开程序。用户收到虚假成功。
- **修复建议**：基于工作区根和经过约束的 relPath 构造绝对路径，验证文件存在并使用 Windows Shell 打开；启动失败必须返回错误。

### H-07 索引文件列表、状态、标签编辑和语义搜索未接入页面

- **位置**：`frontend/src/stores/files.ts:1-30`；`frontend/src/api/client.ts:47-89`
- **影响**：`useFilesStore`、`searchFiles`、`updateFileTags`、文件详情 API 均无人调用。当前“搜索”仅扫描文件名，用户看不到 `indexStatus/lastError`，也无法使用产品核心的内容搜索和标签管理。
- **修复建议**：工作区主视图应以索引文件 API 为主数据源，文件系统浏览作为导航辅助；实现内容搜索、状态筛选、标签过滤和编辑。

### H-08 设置页未回填 MarkItDown，保存会清空已有配置

- **位置**：`frontend/src/views/SettingsPage.vue:44-53`、`:79-80`
- **影响**：进入设置页后 `pythonPath` 和 `command` 保持空值，点击保存便覆盖有效配置，后续提取失败。
- **修复建议**：从 `settings.markitdown` 回填；表单初始化完成前禁用保存；添加加载-保存不变性测试。

### H-09 配置修改不会更新已构造模块的运行参数

- **位置**：`backend/internal/assembler/app.go:121-171`；`backend/internal/transport/transport.go:1274-1295`
- **影响**：运行时修改 MarkItDown 命令、分块大小、重叠、问答上下文、Embedding 维度、工作区和 debounce 后，Config 会变，但 Extract、Index、QA、Storage、Watch 保留启动时参数。UI 显示已保存，实际行为没有变化，部分值只能重启后生效。
- **修复建议**：明确区分热更新项与需重启项。可热更新的模块提供 ApplyConfig；其他项保存后明确提示并执行受控重建/重启，不能无提示地假成功。

## 5. 中严重度问题

### M-01 初始化接口没有执行设计契约要求的探测和模型测试

- **位置**：`backend/internal/transport/transport.go:541-640`；对照 `design.md:559-562`
- **影响**：接口先保存配置并直接触发重建，没有调用 `Extract.Probe`、`TestEmbed` 或可选 `TestChat`。无效配置直到批量任务失败才暴露，继而可能暂停队列。
- **修复建议**：先验证目录、提取、嵌入，再提交配置和模块切换；失败不得留下半初始化状态。

### M-02 初始化写配置时大量错误被忽略，可能返回伪成功

- **位置**：`backend/internal/transport/transport.go:589-624`
- **影响**：除 workspace 和 Relocate 外，多个 `Config.Set`/`UpsertSecrets` 返回值未检查。磁盘写入失败、类型错误等情况下接口仍可能继续并返回成功。
- **修复建议**：统一事务式应用配置；任一写入失败则回滚并返回错误。

### M-03 问答失败会留下用户消息和空会话

- **位置**：`backend/internal/qa/qa.go:63-75`、`:173-177`
- **影响**：注释声称“仅在最终成功时才保留”，实际先创建会话、写入用户消息；LLM 失败后没有删除消息或会话。会话历史中出现永久无回答问题。
- **修复建议**：模型成功后再事务写入双方消息，或失败时显式回滚新会话和用户消息。

### M-04 关键页面普遍没有可见错误状态

- **位置**：`frontend/src/views/TimelinePage.vue:14-20`、`StatsPage.vue:16-24`、`QAPage.vue:16-24`、`SettingsPage.vue:55-87`
- **影响**：网络、后端和配置错误表现为空数据或未处理 Promise rejection；保存设置/密钥失败没有错误提示，用户容易误认为成功。
- **修复建议**：每个 store 维护 `loading/error/lastSuccess`，页面区分“无数据”和“加载失败”；写操作只在确认成功后显示成功状态。

### M-05 工作区状态请求失败会伪装成“未初始化”

- **位置**：`frontend/src/stores/workspace.ts:13-23`
- **影响**：后端临时失败时界面引导用户重新初始化已有工作区，可能进一步覆盖配置。
- **修复建议**：保留 `info=null` 并单独设置 error；只有后端明确返回 `initialized:false` 才显示未初始化。

### M-06 设置加载失败会变成空配置，保存可能覆盖真实值

- **位置**：`frontend/src/stores/settings.ts:9-15`；`frontend/src/views/SettingsPage.vue:38-53`
- **影响**：加载失败后显示空表单与前端默认值，用户保存会覆盖后端真实配置；fetch 也从未设置 loading=true。
- **修复建议**：加载失败阻止编辑与保存，显示重试；保留上一次成功快照。

### M-07 目录树只渲染两级且根折叠失真

- **位置**：`frontend/src/views/WorkspacePage.vue:236-257`
- **影响**：第二级节点可加载更深 children，但模板不递归渲染；点击根节点箭头变为折叠，子节点仍显示。
- **修复建议**：使用递归 TreeNode 组件；根 children 受 `root.expanded` 控制。

### M-08 关闭时可能让运行任务继续访问已关闭 SQLite

- **位置**：`backend/internal/assembler/app.go:406-421`；`backend/internal/taskqueue/taskqueue.go:218-228`
- **影响**：CancelAll 只清空未开始任务，不中断或等待正在执行的 handler；随后立即关闭数据库，可能留下失败或半完成状态。
- **修复建议**：队列增加 stop、WaitGroup 和 drain/timeout 语义；停止接收新任务，等待当前任务完成或取消后再关闭 Storage。

## 6. 低严重度问题

### L-01 标签建议字段名错配

- **位置**：`frontend/src/types/index.ts:41-49`；`backend/internal/contract/contract.go:45-53`
- **影响**：前端声明 `suggestedByFile`，后端 JSON 为 `fileId`；未来展示来源文件时得到 undefined。
- **修复建议**：按通信契约统一为 `fileId`。

### L-02 统计柱图在全零数据时产生 `NaN%`

- **位置**：`frontend/src/views/StatsPage.vue:92-94`
- **影响**：有日期项但所有 count 为 0 时分母为 0，柱高计算为 `0/0`。
- **修复建议**：最大值为 0 时统一使用 0 高度或安全分母 1。

## 7. 其他契约偏差与产品缺口

以下问题暂未单独提高计数，但应在对应主问题修复时一并处理：

- `GET /api/workspace/info` 把 `markitdownConfigured` 固定为 false，见 `backend/internal/transport/transport.go:675-681`。
- 未初始化时 `/api/stats` 与 `/api/timeline` 实测返回 500；页面应在工作区未就绪时阻止请求或后端返回明确的 `not_configured`。
- `handleBrowse` 返回错误码 `not_init`，不在 `design.md` 的错误码表中，契约定义为 `not_configured`。
- `index_progress` 同一字段 `done` 在全量进度中是整数，在单文件完成事件中是布尔值，见 `backend/internal/index/index.go:255-260` 与 `:417-422`；这破坏 SSE 固定负载契约并导致消费者脆弱。
- 全量扫描创建 FileInfo 时没有填 size/mtime，见 `backend/internal/index/index.go:236-240`，因此首次文件元数据可能显示为 0。
- `frontend/README.md` 仍是 Vite 模板，缺少实际安装、MarkItDown、配置、构建、运行和故障排查说明。
- 设计包含 `/onboard` 首次向导，但前端路由只有工作区、时间线、问答、统计、设置；目前把初始化塞进设置页，且未按向导流程逐步验证。
- 设计要求托盘常驻与退出流程，当前纯网页入口没有托盘实现，`ShowWindow` 为空。

## 8. 测试缺口

后端所有包均无测试，前端也没有测试脚本。至少需要以下自动化层次：

1. **配置测试**：首次配置、Relocate、重启定位、密钥保留、写失败回滚。
2. **路由契约测试**：枚举设计 §5 每条 API 的方法、路径、请求和响应字段；特别覆盖子路径。
3. **索引测试**：新文件、已有文件重建、空文本、维度不匹配、删除、非支持类型、目录事件。
4. **队列测试**：去重、第五次失败、恢复、暂停/恢复、关闭等待。
5. **事件测试**：`fileId` 数值类型及每个 topic 的固定 DTO。
6. **前端 store/view 测试**：错误状态、设置回填、文件模式必须选文件、文件操作接线。
7. **端到端测试**：临时工作区初始化 → 建 Git → 索引一个 txt/md → 搜索 → 文件问答 → 自动标签 → 提交 → 时间线 → 重启后仍可用。

## 9. 建议修复里程碑

### P0：恢复基础可用性

修复 B-01 至 B-04、H-02 至 H-04、H-09；建立临时工作区端到端测试。完成标准是“首次初始化后无需手工补救，重启后配置仍在，索引队列可恢复”。

### P1：接通核心产品能力

修复 B-05、H-01、H-05 至 H-08，并完成文件详情、打开、语义搜索、状态/错误、标签和文件问答 UI。完成标准是设计中的文件、搜索、标签、问答主流程能从界面闭环使用。

### P2：可靠性与可维护性

修复全部 M/L 问题，统一 REST/SSE DTO，补齐测试、README、首次向导和关闭流程。完成标准是关键失败均可见、可恢复，且契约测试能阻止前后端再次漂移。
