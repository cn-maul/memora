# Memora 系统二次审计与后续整改计划

> 审计基线：`phase3-converge` / `da5666e`
> 审计日期：2026-08-12
> 审计范围：Go 后端、Vue 前端、REST/SSE 契约、SQLite/内存向量索引、工作区生命周期、任务队列、性能、可观测性、本地构建与发布
> 文档性质：当前源码整改的唯一总纲。旧版计划及其中已失真的完成状态已删除，本文件以二次源码审查和实测结果为准。
> 工程门禁：项目只维护本地 `verify.bat`。

## 1. 执行结论

Memora 已完成一轮较大范围的重构，生产构建、现有测试、模块拆分和主要业务链收敛均有实质进展，但系统尚未达到“整改完成”状态。

按旧计划任务条目估算，当前总体实现约为 **55%-60%**；若严格按验收标准和完成定义计算，约为 **45%-50%**。主要差距不是文件数量或代码风格，而是部分关键能力只完成了接口、测试替身或文件拆分，没有贯通生产调用链。

当前最重要的五类问题是：

1. **工作区生命周期仍不安全**：没有 runtime lease 或 HTTP 请求排水，模块引用逐字段替换，旧 SQLite 可能在请求或任务仍使用时被关闭。
2. **关键写入没有真正原子化**：生产索引未调用现有 `ReplaceFileIndex`；工作区初始化仍逐项写配置，失败不能恢复原状态。
3. **前端状态与异步竞态仍未收敛**：问答切换会话可永久卡在发送中，多页面仍共享一份文件查询结果，部分慢响应可覆盖用户的新选择。
4. **性能和可观测性只有局部实现**：heap、batch、低频 reconcile 已落地，但仍有标签查询/刷新放大；日志关联、最近错误和真实生产基准未闭环。
5. **本地发布工程不够可靠**：发布脚本不强制执行完整门禁，会丢弃本地修改，失败的校验和或 SBOM 不能阻断发布，也没有制品级自动冒烟。

因此，当前应停止继续扩大模块拆分范围，优先修复数据一致性、生命周期和用户可见卡死问题，再补齐性能与本地发布门禁。

## 2. 当前验证基线

### 2.1 已执行验证

| 检查 | 结果 | 说明 |
|---|---|---|
| `verify.bat` | 通过 | 完整执行六步本地门禁 |
| 前端类型检查 | 通过 | `vue-tsc --noEmit` |
| 前端测试 | 通过 | 4 个测试文件，59 个测试 |
| 前端生产构建 | 通过 | Vite 构建 145 个模块 |
| `go vet ./...` | 通过 | 无报告问题 |
| `go test -count=1 ./...` | 通过 | 所有现有后端测试通过 |
| 并发敏感包重复测试 | 通过 | assembler/taskqueue/qa/transport 连续 20 次 |
| `gofmt -l backend` | 通过 | 无格式漂移 |
| 前端覆盖率 | 约 62.16% lines | 页面、files store、设置、事件同步和工作区切换覆盖不足 |
| 50k vectors 算法基准 | 通过候选阈值 | 1024 维、top-K heap p95 约 61ms |
| race 检查 | 未执行 | 当前环境未启用 CGO，`-race` 无法运行 |
| 制品级 E2E/升级冒烟 | 不存在 | 当前只有人工清单 |

### 2.2 验证结果的边界

- 现有测试通过只能证明已覆盖场景，没有覆盖 runtime 切换期间的活动 HTTP、慢提取、慢 LLM、DB-after-close 和多故障点配置回滚。
- 当前性能基准复制了候选检索算法，不调用生产 storage，不包含生产索引复制、SQLite、冷缓存或完整 search/QA 链路。
- 前端测试集中在错误映射、SSE parser、QA store 部分路径和文件历史弹窗；多数页面交互和跨页面状态没有测试。
- 本地 `verify.bat` 是唯一工程门禁，后续整改必须把必要检查纳入该脚本或由独立的本地冒烟脚本调用。

## 3. 已确认完成或基本完成的内容

以下成果已有生产代码和测试支撑，应保留并在后续修改中防止回退：

### 3.1 基础正确性与契约

- 前端类型检查和生产构建已恢复。
- 配置文件使用临时文件、`Sync` 和 rename 进行单文件原子写。
- SQLite 已建立 `PRAGMA user_version` 迁移框架和迁移测试。
- `SaveExchange` 已将问答双消息写入收敛为事务。
- 请求体上限、panic recovery、HTTP 基础超时和 request ID 已加入。
- 前端 API client 已能解析统一错误信封，SSE 异常 EOF 不再永久悬挂。
- Markdown 渲染已统一到 `ChatSurface`，使用 DOMPurify 白名单。

### 3.2 业务链收敛

- `AllFilesPage.vue` 已删除。
- 旧 `/api/timeline` 已下线并有 404 characterization test。
- 旧 `index.Query` 和 `search.IIndex` 死入口已删除。
- commits 已成为主要版本浏览模型。
- 问答核心逻辑已合并到 `Execute` 管线，流式与非流式共享主体流程。
- 文件历史、下载和恢复已收敛到 `FileHistoryDialog`。
- provider/model 预设和设置状态已有共享实现。
- 前端页面路由已改为懒加载。

### 3.3 性能与运行治理

- 生产向量检索已使用 bounded top-K heap。
- search 和 QA 的候选 chunk/file 主查询已提供批量查询路径。
- 原 8 秒全盘扫描已改为 watcher 主导、60-300 秒退避的 reconciliation。
- SSE 正常时不再常驻轮询，断线后才启用 15 秒降级轮询。
- 文本缓存已有版本化 key、流式 SHA-256、容量配额和统计方法。
- `/health`、`/ready`、`/diagnostics` 端点及基础测试已存在。
- 本地构建脚本已支持 version/commit/build time 注入，发布脚本已有校验和、依赖清单和变更日志的基础框架。

## 4. 当前问题台账

严重度定义：P0 为数据一致性、生命周期或发布阻断；P1 为核心功能正确性和用户可用性；P2 为架构、性能和可观测性缺口；P3 为持续治理。

### 4.1 P0：必须优先修复

| ID | 问题与当前证据 | 影响 | 整改要求 |
|---|---|---|---|
| P0-01 | `RuntimeManager` 只保护 current 指针；`applyRuntimeModules` 逐个改写 App/APIHandler 字段；工作区切换只排水任务，不排水 HTTP，超时后仍关闭旧 storage | 混合 generation、data race、`database is closed`、跨库写入 | 实现 runtime lease/refcount；每个 HTTP/任务固定持有一代 Runtime；串行化切换；旧代引用归零后才能关闭 |
| P0-02 | `ProcessFile` 先提交 `ChunksReplaceForFile`，再逐条 `VectorsInsert`；现有 `ReplaceFileIndex` 无生产调用 | 向量写入失败时旧索引已删除，新索引只有部分向量 | 生产路径统一调用 `ReplaceFileIndex`；chunks/vectors/hash/status 在一个明确发布边界内完成；故障注入覆盖第 N 次写入 |
| P0-03 | 工作区初始化先逐项 `Config.Set/Relocate`，再构建 Runtime；失败分支不恢复 `wsPath`、配置路径和配置内容 | 初始化失败后旧 Runtime 与新路径混用，重启也可能指向失败目标 | 实现 `ApplyWorkspaceConfig`：读取目标已有配置、构造候选配置/凭据/Runtime、探测成功后一次提交；任一步失败保持原状态 |
| P0-04 | `Config.Relocate` 切换到已有工作区时，把当前内存配置保存到目标 `config.json` | A 切换到 B 时覆盖 B 的模型、索引、Git 等配置，并把 A 入口改指向 B | 已有目标配置必须加载并校验，禁止用源工作区配置覆盖；增加 A→B→A 集成测试 |
| P0-05 | `freezeQueue` 排水 5 秒超时后只告警；shutdown 忽略等待结果并关闭 storage；活动任务缺少取消 context | 慢提取、嵌入或重建仍在运行时访问已关闭 DB | 任务全链路 context；关闭时 cancel、等待明确终态；超时必须记录任务并阻止错误的正常完成语义 |

### 4.2 P1：核心正确性与用户可用性

| ID | 问题与当前证据 | 影响 | 整改要求 |
|---|---|---|---|
| P1-01 | QA generation 失效时回调直接 return，不 resolve；`newSession/selectSession` 不 abort 活动请求 | 切换或新建会话后 `sending=true` 永久不复位，输入区卡死 | 会话操作先 cancel 活动请求；所有回调路径只结算一次 Promise；旧请求最终必须进入 finally；补回归测试 |
| P1-02 | LLM 流读取异常只记日志并关闭 channel，QA 将已收到内容当完整成功 | 截断回答被持久化并显示为成功 | 流必须返回显式 success/error 终态；未收到正常完成标记或发生读错误时返回 `stream.interrupted`，不得保存成功 exchange |
| P1-03 | 只在 `embed.dimensions` 改变时触发重建；相同维度的 model/base URL 变化不会使旧向量失效 | 新查询向量与旧文档向量不在同一向量空间，搜索/QA 召回错误 | 持久化 embedding fingerprint（provider/base/model/dim/预处理版本）；fingerprint 改变时强制重建，幂等判断同时校验 fingerprint |
| P1-04 | `/settings/secrets` 只更新 Config；LLM 优先读取启动时创建的 CredentialStore，切工作区也不重绑 | 界面提示密钥更新成功，但模型调用仍使用旧密钥或其他工作区密钥 | 凭据写入必须直接更新当前工作区 CredentialStore；工作区切换时候选凭据与 Runtime 一起交换；端到端测试更新后立即调用 |
| P1-05 | `qa.systemPrompt` 可保存但 QA 始终使用编译期常量 | 用户设置成功但行为完全不变 | 删除无效配置入口，或将有效 prompt 作为 QA Runtime 配置注入并测试热更新/重启语义 |
| P1-06 | `selectSession` 自身没有请求令牌或 AbortController | 快速选择 A 再选择 B 时，A 的慢响应可能覆盖 B 的消息 | 会话消息加载采用 latest-request-wins；响应写入前校验 session ID 和请求 generation |
| P1-07 | Workspace/RecentFiles/Index 等页面的提交选择、预览和搜索缺少 generation | 慢响应覆盖用户的新选择，可能对错误对象继续操作 | 所有用户驱动查询使用 AbortController 或 token；弹窗响应同时校验资源 ID |
| P1-08 | `FullReindex` 在 cleanup 和聚合错误判断前发送 `phase=done` | 失败重建仍在 UI 显示完成 | 终态改为 `done/partial/failed/canceled`；所有清理和结果汇总完成后只发布一次终态 |
| P1-09 | migration 在事务提交后单独写 `PRAGMA user_version`；备份失败只告警 | schema 已改变但版本仍旧；升级缺少可靠备份 | 在同一事务设置 user_version；备份失败默认阻断迁移；故障测试覆盖备份、Apply、commit、版本写入 |
| P1-10 | 自动提交仍可能包含用户预先 staged 的无关内容 | 提交文件集合超出本次任务预期 | 使用独立 index 或提交前精确比对 staged 集合；任务提交文件与最终 commit tree 必须一致 |
| P1-11 | `/ready` 只检查 generation 非空和 storage ping；空工作区启动也会创建 generation | 尚未初始化时错误报告 ready | readiness 明确检查有效 workspace、Runtime 状态、storage 和必要后台组件；未初始化返回 503 和原因 |

### 4.3 P2：架构、状态、性能与可观测性

| ID | 问题与当前证据 | 整改要求 |
|---|---|---|
| P2-01 | transport 虽已拆文件，但仍负责编排配置迁移、Runtime 重建、Python 探测和任务触发 | 增加 application service；handler 只校验 DTO、调用用例、映射响应；删除直接 goroutine/reindex fallback |
| P2-02 | `files` store 仍是一份全局查询状态，QA/Index/SSE 互相取消和覆盖 | 按 feature 建独立 query state；实体缓存与查询结果分离；SSE 只做失效标记或定向刷新 |
| P2-03 | `App.vue` 仍包含侧栏聊天模式决策和完整提交 dialog 业务 | App 只保留导航、主题、连接状态和全局通知；聊天入口与提交 dialog 下沉到 feature 组件/composable |
| P2-04 | 中央 `contract` 的 IStorage/ILLM/IGit/IWatch 等大接口仍存在且无生产消费者 | 保留 DTO/错误/事件类型；删除失真中央接口；消费方继续定义最小接口 |
| P2-05 | watch/reconciliation/git 仍复制文档扩展名和忽略目录规则 | 全部改用 `documentpolicy`；增加覆盖所有调用模块的表驱动契约测试 |
| P2-06 | 同步 `/api/qa` 和流式 `/api/qa/stream` 仍是两个公开入口，取消语义不同 | 明确唯一公开入口；若保留同步接口，必须接收请求 context 并共享完全一致的终态/持久化规则 |
| P2-07 | MarkItDown `ApplyConfig` 无锁写字段且不能清空旧值 | 配置快照原子替换或加锁；空值必须有明确清空语义；并发运行时用不可变配置快照 |
| P2-08 | search 最终结果仍逐个查询 tags | 增加 FilesTagsByFileIDs 批量接口；一次查询返回当前页所有标签，查询数保持 O(1) |
| P2-09 | 每个 `index_progress` 都立即刷新 tags | 对同 topic 合并/节流；全量重建期间标签刷新不随文件数线性增长 |
| P2-10 | reconcile 虽默认低频，但设置允许降到 2 秒 | 后端设置合理最小值；前端约束与后端一致；旧 8 秒 fallback 默认值全部删除 |
| P2-11 | 缓存 `CleanupExpired` 只有单元测试，无生产调用；命中不更新 mtime | 配置 TTL 并在启动/低频维护任务中执行；提供清理入口；需要 LRU 时在命中后受控 touch |
| P2-12 | `/diagnostics.recentErrors` 永远为空，且不包含 commit/build time | 接 logx 内存环形缓冲；返回最近脱敏错误、version/commit/buildTime、活动任务和最后失败 operation |
| P2-13 | requestId 未贯穿日志，operationId 基本未实现；缺少请求耗时、日志轮转和等级阈值 | HTTP/任务/LLM/索引统一 operation context；记录 start/end/duration/outcome；实现等级过滤、轮转、保留期和脱敏 |
| P2-14 | ChatSurface 每次响应式渲染都重新 parse/sanitize 已完成消息 | 完成消息缓存安全 HTML，只对当前流式消息增量更新；测试缓存失效和 XSS 样例 |
| P2-15 | FileHistoryDialog 等弹窗缺少 Escape、焦点管理和窄窗口行为 | 增加 dialog 语义、focus trap/restore、Escape、移动端布局和组件测试 |

### 4.4 P3：本地工程与发布治理

- `verify.bat` 在缺少依赖时必须使用 `npm ci`，不得退化为会修改 lockfile 的 `npm install`。
- 前端测试不可选；没有 test script 应视为门禁配置错误。
- 固定并验证 Go、Node 和 npm 版本；版本不匹配时本地门禁明确失败。
- 新增本地 `smoke-release.bat` 或等价脚本，测试打包后的 exe，而不是只测试源码模块。
- 发布脚本必须在仓库根执行所有 Git 命令，拒绝脏工作树，不得 `checkout --force` 或改变用户当前分支。
- 发布前强制运行 `verify.bat`；任何校验和、依赖清单、构建或冒烟失败都必须返回非零退出码。
- 只接受解析到 tag object 的版本参数，拒绝分支名和任意 commit。
- 依赖清单改为标准 SPDX 或 CycloneDX 格式，并增加本地解析验证。
- BuildTime 使用真实构建时间；诊断端点同时暴露 version、commit 和 build time。
- 建立真实旧版本 DB/config fixture，覆盖升级成功、失败回滚和恢复备份。
- 文档只描述真实实现，不再声明尚未存在的 operationId、TTL、最近错误或自动冒烟能力。

## 5. 修复路线

### Phase A：数据一致性与 Runtime 生命周期

目标：首先消除跨库写、DB-after-close 和半成品索引。

实施项：

1. 为 RuntimeManager 增加 `Acquire/Release` lease，Runtime 包含 closing 状态和引用计数。
2. HTTP middleware 在路由前获取 runtime lease，并通过 request context 传递；任务提交时固定 generation，执行时获取对应 lease。
3. 串行化工作区切换；停止旧代接收新工作，取消后台操作，等待引用排空后再关闭。
4. 实现 `ApplyWorkspaceConfig` 候选流程，支持已有工作区配置加载和所有失败点回滚。
5. 生产索引改用 `ReplaceFileIndex`，将文件索引发布变为单事务。
6. 修正 migration 的备份和 user_version 事务边界。
7. 让关闭流程的任务、提取、嵌入、LLM 和索引全部响应 context 取消。

验收：

- 活动文件查询、QA 流、慢提取和慢嵌入期间切换工作区，不出现 data race、跨库写或 DB-after-close。
- A→已有 B→A 切换后，两边配置、凭据、数据库和索引均保持各自内容。
- 任一索引写入故障后，搜索只能看见完整旧版本或完整新版本。
- 工作区应用每个故障点失败时，当前 workspace、配置入口、目标配置和 Runtime 均不变化。
- 关闭超时有明确 canceled/failed 终态，不再以正常完成继续运行。

### Phase B：问答、配置和前端竞态修复

目标：消除用户可见卡死、截断成功和跨页面状态污染。

实施项：

1. 重写 QA store 的一次性 settle/cancel 逻辑；会话切换、新建和删除采用明确并发策略。
2. LLM stream 返回显式终态，异常 EOF/读错误不保存成功 exchange。
3. 增加 embedding fingerprint，并统一模型配置变化后的重建策略。
4. 修复 CredentialStore 更新和工作区重绑定。
5. 决定 `qa.systemPrompt` 是真正生效还是删除，禁止保留假设置。
6. 分离 files 实体缓存与 Index/QA 查询状态。
7. 为会话、搜索、commit、preview 和文件详情增加 latest-request-wins。
8. 将重建终态改为 done/partial/failed/canceled。

验收：

- 任意会话操作后旧发送 Promise 都能结束，`sending` 必定恢复。
- 流式连接中断不会产生成功消息或成功 SSE done。
- 相同维度模型切换后旧向量不会继续使用。
- QA 文件下拉、索引页列表和 SSE 刷新互不覆盖查询结果。
- 慢响应不能覆盖新会话、新查询、新 commit 或新预览。

### Phase C：边界、性能和可观测性闭环

目标：让已拆分模块形成真实所有权边界，并用生产链路证明性能和诊断能力。

实施项：

1. 新建 application use case 层，迁出 transport 中的工作区、设置和进程编排。
2. 删除中央死接口和重复 document policy。
3. 批量加载结果标签，节流 index_progress 触发的刷新。
4. 将 benchmark 改为调用生产 storage/search/QA 检索路径，记录冷热缓存和 SQL 查询数。
5. 接入 operation context、请求/任务耗时、错误环形缓冲和日志等级/轮转。
6. 将缓存 TTL 接到低频维护流程，并补清理入口。
7. 缓存已完成聊天消息的安全 HTML，完善弹窗可访问性和窄窗口行为。

验收：

- transport handler 不直接创建 goroutine、执行进程探测、切换 Runtime 或编排多模块事务。
- search/QA 候选及标签元数据 SQL 查询数为 O(1)。
- 5000 文件/50000 chunks 的生产候选检索 p95 在固化阈值内，并保留真实结果记录。
- 全量索引期间同类前端刷新按时间窗口合并，不随文件数量线性增长。
- 从 UI requestId 能定位 HTTP→任务→LLM/索引的完整 operation 链。
- diagnostics 返回真实最近错误、活动任务、版本、commit 和 build time。

### Phase D：本地发布工程

目标：使本地发布可重复、可验证、不会破坏开发工作区。

实施项：

1. 固定本地工具链版本并纳入 `verify.bat` 检查。
2. `verify.bat` 全程使用 lockfile 严格安装模式，固定执行 typecheck/test/build/vet/test/format。
3. 重写 `release.bat`：拒绝脏树、不切换分支、不丢弃修改、强制验证 tag、先运行门禁。
4. 校验和、标准 SBOM、版本信息和变更日志任一步失败都阻断发布。
5. 增加制品级本地冒烟：首次启动、旧库升级、初始化、索引、搜索、QA、提交、恢复和重启。
6. 建立版本化 DB/config fixture 及升级/回滚测试。
7. 更新发布、备份和故障排查文档，使其与脚本真实行为一致。

验收：

- 干净 checkout 在固定工具链下，一条本地命令完成验证和制品构建。
- 发布命令不会改变当前分支、HEAD 或工作树内容。
- 非 tag、脏工作树、门禁失败、校验和失败、SBOM 失败和冒烟失败均返回非零退出码且不宣告发布成功。
- 制品中的 version/commit/build time 可追溯到源码和真实构建时间。
- 真实旧版本 fixture 可升级；故障后可恢复备份并由旧版重新打开。

## 6. 测试矩阵

| 层级 | 必测内容 |
|---|---|
| Runtime | lease 获取/释放、切换串行化、活动 HTTP、慢任务、旧代排空、超时取消、关闭顺序 |
| Workspace | 新工作区、已有工作区、A→B→A、配置/凭据隔离、每个候选构建和提交故障点回滚 |
| Repository | ReplaceFileIndex 原子性、空内容、SaveExchange、rows.Err、migration 备份/Apply/commit/version 故障 |
| Index | 第 N 批 embed、第 N 次 vector 写入、少/多向量、cleanup 失败、embedding fingerprint 变化 |
| QA/LLM | 正常 done、异常 EOF、读错误、取消、空流重试、持久化失败、会话切换/新建/删除 |
| 前端 Store | query 隔离、latest-request-wins、AbortController、loading/error/empty/refreshing、旧响应丢弃 |
| 前端组件 | 文件恢复 token、聊天 settle、弹窗 Escape/焦点、窄窗口、Markdown 缓存和恶意样例 |
| 性能 | 500/5000 文件、10k/50k chunks；生产 storage/search/QA；冷热缓存；SQL 查询数；刷新请求数 |
| 可观测性 | requestId/operationId 关联、duration/outcome、脱敏、日志轮转、最近错误和 diagnostics |
| 本地发布 | 固定工具链、严格 lockfile、脏树拒绝、tag 验证、制品元数据、校验和、SBOM、升级和回滚冒烟 |

## 7. 本地门禁

根目录 `verify.bat` 是唯一持续工程门禁，目标步骤固定为：

1. 校验 Go、Node 和 npm 版本。
2. 使用 `npm ci` 安装或校验前端依赖。
3. 前端类型检查。
4. 前端测试，测试脚本缺失视为失败。
5. 前端生产构建。
6. `go vet ./...`。
7. `go test -count=1 ./...`。
8. `gofmt -l backend` 漂移检查。
9. 可用环境下执行 race 检查；不可用时输出明确的未执行原因，不伪装为通过。

制品发布必须额外执行本地制品冒烟，不以源码测试代替打包后的 exe 验证。

## 8. 实施规则

1. 修复顺序固定为 Phase A→B→C→D；P0 未完成前不继续做大范围 UI 改版或抽象重构。
2. 每个行为修复先增加可复现失败测试，再修改生产调用链；只测试未被调用的 helper 不算完成。
3. 工作区、索引、问答和迁移必须做故障注入，不能只验证 happy path。
4. 模块拆文件不作为低耦合验收；以依赖方向、Runtime 所有权、事务边界和替换测试为准。
5. 新配置项必须有真实读取者、应用时机和测试；无法生效的设置直接删除。
6. 新兼容入口必须有真实消费者、移除条件和契约测试；无消费者的旧接口不默认保留。
7. 所有异步前端操作都要定义取消、迟到响应和组件卸载语义。
8. 性能整改必须测生产路径并记录环境、seed、冷热缓存、次数、p50/p95 和源码 commit。
9. 文档只能声明已实现并验证的能力；计划目标必须明确标为目标，不能写成当前事实。
10. 发布脚本不得执行破坏性 Git 操作，不得丢弃、覆盖或隐藏用户未提交修改。

## 9. 完成定义

只有同时满足以下条件，系统才达到本轮“重新凝聚”标准：

- 每个 HTTP 请求和后台任务固定持有一个 Runtime generation，工作区切换和关闭测试无法制造跨库写或 DB-after-close。
- 工作区配置、凭据、Runtime 交换和已有目标配置具有原子提交/失败回滚语义。
- 文件 chunks、vectors、hash 和状态以明确事务边界发布，故障后查询只看到完整旧版本或完整新版本。
- QA 任意终态都可结束；会话操作不会卡住发送状态；异常流不会保存截断成功回答。
- embedding 模型身份被持久化验证，相同维度模型切换也不会复用不兼容向量。
- 各 feature 查询状态隔离，慢响应无法覆盖用户的新会话、新查询、新提交或新文件。
- transport 只负责协议适配，工作区/设置/任务编排由 application use case 负责。
- 文档类型和忽略规则由 documentpolicy 单一提供，中央死接口和无消费者入口已清理。
- 生产检索链达到固化性能阈值，SQL 和前端刷新次数不随候选/事件数线性放大。
- 日志具有真实 requestId/operationId、耗时、结果、等级、轮转和脱敏；diagnostics 返回真实最近错误和构建信息。
- `verify.bat` 在干净 checkout 可重复通过；race 不可用时有明确记录。
- 本地发布不修改工作树或分支，门禁、校验和、标准 SBOM 和制品冒烟任一步失败都会阻断发布。
- 真实旧版本 DB/config 可升级、备份和回滚，发布文档与脚本行为一致。

---

本文件替代此前所有完成状态和旧整改路线。后续发现的新问题必须先归入对应 ID/Phase，补充触发条件、影响、生产证据和可复现验收标准，再进入实现。
