# Memora 系统审计与整改计划

> 审计基线：`main` / `c9fe01d`  
> 审计日期：2026-08-12  
> 审计范围：Go 后端、Vue 前端、REST/SSE 契约、SQLite/向量索引、Git、配置与密钥、任务与生命周期、构建测试、日志和发布工程  
> 文档性质：当前源码整改主计划。历史审计快照已随整改完成归档删除，不再作为现状依据。

## 实施进度（截至 2026-08-12）

| Phase | 主题 | 状态 | Commit |
|---|---|---|---|
| Phase 0 | 发布止血与工程门禁 | 完成 | `6605744` |
| Phase 1 | 生命周期与持久化一致性 | 完成 | `fc4f83e` |
| Phase 2 | 契约、错误和日志统一 | 完成 | `031c3ab` |
| Phase 3 | 收敛业务主链 | 完成 | `4645d21` |
| Phase 4 | 模块拆分与前端状态治理 | 完成 | `47c1ad7` |
| Phase 5 | 性能与可观测性 | 完成 | `991c2f4` |
| Phase 6 | 发布工程与文档 | 进行中 | — |

> 注：台账中的行号随代码演进可能偏移；本计划仍为整改总纲，实施中新增问题先归入对应 ID/Phase。

## 1. 执行结论

Memora 的产品主链已经具备，但多轮快速修改后，当前实现没有形成稳定的单一架构。主要矛盾不是代码风格，而是以下五类系统性问题：

1. **运行时一致性不足**：工作区切换、全量重建、HTTP 请求、后台任务和关闭流程没有统一生命周期；旧 SQLite 可能在使用中被关闭，多个重建也可能并发写同一索引。
2. **关键写入不原子**：配置覆盖、数据库升级、chunks/vectors 发布、问答双消息写入均可能部分成功；空文件重建还会保留旧索引。
3. **新旧业务链并存**：同步/流式问答、侧栏/独立问答、新旧版本浏览、旧 `index.Query`/新 `search` 编排、三套文件详情逻辑长期共存。
4. **边界与契约失真**：中央 `contract`、模块局部接口、设计文档和真实调用链不一致；前端大量 `any`、静默 catch 和直接 `data!` 使契约漂移延迟到运行时。
5. **缺少工程安全网**：当前前端生产构建失败；没有前端测试、lint、CI、数据库迁移、可复现发布和统一错误/日志规范。

整改不能从大规模拆文件开始。正确顺序是：**先阻断数据与密钥风险，建立可重复验证；再统一生命周期和写入契约；随后收敛重复业务链；最后做性能、可观测性和发布治理。**

## 2. 审计基线

### 2.1 技术和规模

- 后端：Go 1.22，`net/http`、go-git、fsnotify、modernc SQLite，约 12.5k 行。
- 前端：Vue 3.5、TypeScript 6、Vite 8、Pinia 3、Axios、Marked，约 12.4k 行。
- 运行形态：Windows 本地单进程，Go 内嵌 SPA，REST + SSE，工作区内 `.memora` 保存配置、数据库和文本缓存。
- 数据规模目标：500-5000 文件、数万 chunks，当前向量检索为内存线性扫描。

### 2.2 当前验证结果

| 检查 | 当前结果 | 说明 |
|---|---|---|
| `go test -count=1 ./...` | 通过 | 25 个测试；6 个包有测试，15 个包无测试 |
| `go vet ./...` | 通过 | 0 个问题 |
| 后端编译 | 通过 | 主程序可编译 |
| `gofmt -l backend` | 未达标 | `config.go`、`index.go`、`stats.go`、`transport.go` |
| Vue 类型检查/生产构建 | **失败** | `TimelinePage.vue:176` 对模板 ref 再访问 `.value` |
| Vite 内存构建 | 通过 | 131 个模块；不能替代类型检查 |
| 前端 test/lint | 不存在 | `package.json` 仅有 dev/build/preview |
| CI/CD | 不存在 | 无自动门禁和发布流水线 |
| race 检查 | 未执行 | 当前环境缺少 CGO/gcc |
| 依赖漏洞审计 | 未完成 | npm 镜像不支持 advisory API，`govulncheck` 未安装 |

## 3. 目标架构

整改后的系统保持单进程和简单优先，不引入微服务或大型 DI 框架。目标是建立清晰的模块所有权和单向依赖。

```text
frontend
  app-shell        仅导航、主题、全局通知和连接状态
  features         files / search / chat / timeline / stats / settings
  shared           api-client / error-map / ui / formatting
        |
        | typed REST + typed SSE
        v
transport
  server + middleware + resource handlers + DTO mapping
        |
        v
application
  runtime manager / workspace service / indexing service / chat service
  负责用例编排、事务边界、任务提交和生命周期
        |
        v
domain
  document policy / git / extract / search / tag / timeline / stats
        |
        v
infrastructure
  config store / credential store / SQLite repositories / LLM gateway /
  event stream / task queue
```

### 3.1 硬性边界

1. `transport` 只解析/校验请求、调用 application use case、映射响应，不执行工作区重建、Python 探测、进程编排或数据库事务。
2. 工作区相关模块封装为带 generation 的 `Runtime` handle；同一 generation 内依赖引用集合不可变，内部队列、watcher 等状态只能通过受控方法变化。切换时构建新实例，排水旧实例，再一次性原子交换，禁止逐字段替换。
3. 所有索引入口只调用 `IndexingService.EnqueueRebuild/EnqueueFile`；禁止任意位置直接 `go FullReindex()`。
4. storage 按聚合写入提供事务 API；上层不得靠多次独立 repository 调用模拟事务。
5. 前端全局 store 只保存真正跨页面的会话/连接状态；页面查询结果按 feature 隔离，不共享一份可被任意页面覆盖的 `files.items`。
6. REST/SSE 只保留一套 DTO 定义和错误码表；Go 接口由消费方就近定义，删除失真的全量中央接口。
7. 文档类型、忽略目录、路径约束只由 `documentpolicy` 提供，watch/browser/index/git/轮询共同复用。

## 4. 问题台账

严重度定义：P0 为发布和数据安全阻断；P1 为核心正确性/稳定性；P2 为架构、性能和体验债务；P3 为持续治理。

### 4.1 P0：必须先处理

| ID | 问题与证据 | 影响 | 整改方向 |
|---|---|---|---|
| P0-01 | 现有 Git 仓库打开后不确保忽略 `.memora/`，而配置含明文 API Key；`git.go:52-65`、`config.go:26` | 密钥、数据库、问答和缓存可能进入 Git 历史 | 所有仓库强制检查 ignore；提交 pathspec 二次排除；扫描工作树/index/历史并轮换已泄露 Key；实现统一 `CredentialStore`，Windows 版本使用 DPAPI 加密 |
| P0-02 | 工作区重建直接关闭旧 storage 并无锁替换 App/handler 字段；`assembler/app.go:149-249` | data race、跨库写入、`database is closed` | 引入 RuntimeManager；停止接流量/任务、排水、原子交换；工作区切换集成测试 |
| P0-03 | workspace init、手动重建和维度变更等入口直接 goroutine 执行 `FullReindex`，`Index.mu` 未使用；`transport.go:836,1157,2217`、`assembler/app.go:281`、`index.go:65` | 并发删除/重写 chunks/vectors，索引损坏 | 全部归一到队列；同 generation 多次触发合并为一次运行，运行中最多保留一次 follow-up；重建可取消、可观测 |
| P0-04 | 关闭仅等待 3 秒，仍先关 DB 后停 HTTP；`app.go:613-635` | 活动请求/任务访问已关闭 DB | Shutdown 顺序改为 stop HTTP -> stop watch/poll -> cancel/drain task -> close runtime/storage |
| P0-05 | chunks、vectors、状态/hash 分步发布；`storage.go:469`、`index.go:598-607` | 搜索可见半成品索引 | 先以单文件 SQLite 事务实现 `ReplaceFileIndex`，成功后一次提交；只有基准或跨文件快照需求证明必要时才评估 staging generation |
| P0-06 | 配置直接覆盖写，`Migrate()` 无调用；workspace init 逐项保存，已有目标工作区还可能被当前配置覆盖；`config.go:175-246,575`、`transport.go:718` | 截断 JSON、部分配置生效、配置串库、密钥丢失 | 强类型 schema 和原子文件写；新增 `ApplyWorkspaceConfig`，先构造/探测候选配置和 Runtime，全部成功后提交配置并交换 Runtime，失败保持原状态 |
| P0-07 | 当前前端生产构建失败；`TimelinePage.vue:176` | 无法生成正式制品 | 修复模板 `.value`，将 typecheck/build 纳入根级 verify 和 CI |

### 4.2 P1：核心正确性与用户信任

| ID | 问题与证据 | 影响 | 整改方向 |
|---|---|---|---|
| P1-01 | 空文件重建只标记成功，不删旧 chunks/vectors/hash；`index.go:546` | 已删除内容仍被搜索和回答 | 空内容同样执行原子 Replace，保留“内容为空”的明确状态 |
| P1-02 | `Incremental` 忽略 `ProcessFile`/删除错误；`index.go:480-485` | 队列误报成功、无重试 | 返回根因并统一任务终态；错误分类为 retryable/skippable/fatal |
| P1-03 | `autoCommit.enabled=false` 运行时不生效；配置定义见 `config.go:AutoCommitConfig`，执行路径见 `assembler/app.go:323,669` | 用户关闭后仍改 Git 历史 | 按现有设计保留该能力：补回 UI 开关，入队和执行时双重校验，并加配置契约测试 |
| P1-04 | Git add 错误被忽略，提交混入预 staged 内容；`git.go:205,293-305` | 不完整或越权提交 | 明确 pathspec/独立 index；add 任一失败则中止；提交前展示/验证文件集合 |
| P1-05 | 恢复版本可能覆盖同路径未跟踪文件；`timeline.go:393`、`git.go:814` | 用户文件丢失 | 冲突检测、备份/拒绝策略、父目录创建、恢复前后校验 |
| P1-06 | QA EOF 无 done/error 时 Promise 永不结束；`client.ts:251`、`stores/qa.ts:142` | 永久“发送中” | stream 返回显式终态；异常 EOF 报 connection_interrupted；finally 收尾 |
| P1-07 | 发送中可切换/删除会话，旧请求完成后覆盖新会话；`QAPage.vue:161-171`、`qa.ts:163` | 消息串会话 | cancel + request generation/session ID 校验；会话操作有明确并发策略 |
| P1-08 | 三个文件详情异步响应无 token；`RecentFilesPage.vue:247`、`WorkspacePage.vue:294`、`AllFilesPage.vue:174` | 可能对错误文件执行恢复 | 收敛为一个 `useFileHistoryDialog`，响应按 fileId/generation 验证 |
| P1-09 | Markdown 允许原始 HTML；自制净化器虽删除 `<style>` 标签，但仍保留 inline `style`、表单和远程媒体；`QAPage.vue:208-258,423` | 模型输出可伪造 UI、触发外部请求并扩大 sanitizer 漏洞面 | DOMPurify 严格 allowlist；禁原始 HTML、inline style、表单和远程媒体；固定 tags/attributes/URL schemes，并验证恶意样例不发起外部请求 |
| P1-10 | 请求体无上限，HTTP server 无超时/恢复/request ID；`transport.go:289,599` | 本地资源耗尽、panic 影响服务 | MaxBytesReader、严格 decoder、timeouts、recovery、request ID |
| P1-11 | 流式 LLM 无响应头/空闲超时；`llm.go:41-57` | goroutine 与关闭流程永久悬挂 | Dial/TLS/Header/idle timeout；context 全链路传播 |
| P1-12 | 用户+助手消息、会话写入不成事务且忽略错误；`qa.go:138-140,313` | 空会话/单边消息/伪成功 | `SaveExchange` 事务；持久化失败不得发送成功终态 |
| P1-13 | 错误直接返回 `err.Error()`，前端静默吞错或显示 Blob/raw body | 泄露内部细节，用户无法恢复 | typed error + 稳定 code + requestId；前端统一映射与重试建议 |
| P1-14 | schema 只靠 `CREATE TABLE IF NOT EXISTS`，没有版本和迁移；`storage.go:73-159` | 当前尚无已复现升级事故，但正式发布后的 schema 演进不可控 | 以首提交 `a290cd9` 和当前 schema 生成版本化 fixture；建立 `PRAGMA user_version`、事务迁移、升级前备份及失败回滚测试 |
| P1-15 | `FullReindex` 可在单文件/cleanup 失败后仍返回完成，且未校验向量数量等于 chunks 数量；`index.go:FullReindex`、`index.go:565` | 部分失败被报告成功，文件可能以缺失向量的状态标记 indexed | 校验向量基数；聚合单文件/cleanup 错误并发布 `failed/partial` 终态；故障注入覆盖第 N 批 embed、少/多向量和第 N 次写入 |
| P1-16 | browse/open/extract/index/restore 缺少统一的最终路径 containment，词法检查不能阻止 Windows junction 越界；`browser.go:30`、`git.go:801` | 工作区内链接可能读写或打开工作区外路径 | `documentpolicy` 统一规范化和最终路径校验；默认拒绝越界 symlink/junction，覆盖绝对路径、`..`、混合分隔符和嵌套恢复测试 |

### 4.3 P2：架构收敛与性能

| ID | 问题与证据 | 整改方向 |
|---|---|---|
| P2-01 | `transport.go` 2435 行，混合 server、SSE、所有路由、配置应用、Python 探测和进程执行 | 按资源拆 `handlers/*.go`；通用 middleware/response；编排移到 application |
| P2-02 | `storage.go` 1094 行，表所有权和事务边界不清 | 按 files/index/chat/stats/migrations 拆 repository 文件，共享同一 DB owner |
| P2-03 | `llm.go` 930 行，`ChatStream` 约 256 行 | 拆 client/config/retry/stream decoder/embed/rerank；共用请求和错误分类 |
| P2-04 | `App.vue` 1718 行，负责壳、侧栏聊天、SSE、轮询、提交和 store 刷新 | App 只保留 shell；抽 `useEventSync`、commit dialog、connection status；收敛聊天入口 |
| P2-05 | `SettingsPage.vue`、文件页面和 QA 页面含大量业务状态 | feature composable + 专用组件；页面只编排视图 |
| P2-06 | 同步 `Ask` 与 `AskStream` 重复完整问答流程；`qa.go:90,158` | 单一 `Execute(ctx, request, sink)` 管线，stream 只是输出 sink |
| P2-07 | App 侧栏和 QAPage 两套聊天/渲染；`App.vue:197`、`QAPage.vue:125` | 明确保留独立页为主入口；需要侧栏时复用同一 ChatSurface/store/renderer |
| P2-08 | `search` 仍注入未使用 IIndex，旧 `index.Query` 保留；`search.go:14-39,87`、`index.go:641` | 搜索编排归 search/application；删除死注入和旧入口 |
| P2-09 | 新 commits 浏览外仍保留无人调用 `/api/timeline` 聚合链 | 确认无外部调用后，从 client/route/contract/implementation 整体下线 |
| P2-10 | `AllFilesPage.vue` 1043 行不可达；三页复制详情、历史、下载、恢复 | 删除死页或重新接线；只保留一个文件详情实现和共享操作组件 |
| P2-11 | provider/model 逻辑在向导和设置页语义不一致 | 提取 provider 配置状态机，统一预设/远端模型/错误策略 |
| P2-12 | 最近文件页前端拼接语义搜索+名称搜索，`Promise.all` 全成全败 | 后端统一 Search API，或前端 `allSettled` 明确部分成功语义 |
| P2-13 | 中央 `contract` 与真实实现签名不一致，且模块再声明局部接口 | 以消费方最小接口为准；DTO/事件类型独立共享；删除失真全量接口 |
| P2-14 | 文档类型/忽略目录规则散落 browser/index/watch/app | 建 `documentpolicy` 单一规则包并加表驱动测试 |
| P2-15 | 向量检索复制全量、计算全量、排序全量；search/QA 再做 SQLite N+1 | 第一阶段 batch JOIN + bounded top-K heap；基准不达标再评估 HNSW/vec 扩展 |
| P2-16 | 8 秒递归扫描工作区并逐文件查 DB，与 fsnotify 重复 | watcher 主导；低频 reconciliation；批量载入 path/mtime/size 后比较 |
| P2-17 | App 每个索引事件刷新标签、重复刷新队列，另有无 UI 的 5 秒轮询 | 事件按 topic 合并/节流；SSE 正常时禁轮询，断线时降级并重同步 |
| P2-18 | QA 每次流式更新反复 Markdown 解析整段历史；路由全同步导入 | 路由懒加载；完成消息缓存安全 HTML；仅更新当前消息和预解析 sources |
| P2-19 | files store 被 QA、列表、筛选和 SSE 共同覆盖 | 视图查询隔离；latest-request-wins/AbortController；实体缓存和 query cache 分开 |

### 4.4 P3：工程与运行治理

- 建立前端 Vitest + Vue Test Utils，覆盖 API unwrap、SSE 分帧/EOF、stores 竞态、聊天会话、文件恢复 dialog。
- 建立关键后端测试：config、migrations、storage transaction、index consistency、taskqueue、watch、runtime switch、shutdown、Git ignore/restore。
- 建立 Windows CI：`npm ci`、typecheck、lint、test、build、gofmt check、vet、test、可行环境下 race、干净目录打包冒烟。
- 固定 Node/npm/Go toolchain；构建只使用 lockfile 严格模式；注入 version/commit/build time。
- 发布生成 SHA-256、SBOM 和变更日志；加入升级前备份和回滚演练。
- 增加 `/health`、`/ready` 和诊断摘要：版本、DB、runtime generation、队列深度、活动任务、缓存体积、最近错误。
- API Key 通过统一 `CredentialStore` 管理，Windows 实现使用 DPAPI；文本缓存增加配额、TTL、清理入口和隐私说明。
- 修正文档：根 README、开发验证、备份恢复、发布回滚、错误码、日志字段、当前架构；历史设计书已标注为 ADR 后随整改完成归档删除，架构以源码与 `docs/PROJECT_GUIDE.md` 为准。

## 5. 重复与删除清单

以下清理必须先用引用搜索和契约测试确认，不采用“先保留兼容层”的默认策略。

| 候选 | 当前状态 | 处理决策 |
|---|---|---|
| `AllFilesPage.vue` | 路由不可达，复制文件详情/历史逻辑 | 迁移仍需能力后删除 |
| `getTimeline` + `/api/timeline` + timeline 聚合 | 当前页面只用 commits list/diff | 确认无外部客户端后整链删除 |
| `index.Query` 与 `search.IIndex` 注入 | 实际搜索直接用 LLM + storage | 删除死入口和装配依赖 |
| `NewSesion/ClearSesion` | 拼写错误、未使用接口 | 删除；不要再加兼容别名 |
| `getCommitList(withFiles?)` 的未使用参数 | 无调用者传 `true` | 固化按需 diff 契约后删除参数 |
| SSE “旧版明文”解析 | 仓库历史无法证明有旧客户端 | 用当前 JSON SSE 协议测试固定后删除 |
| `autoCommit.enabled` | 配置存在、UI 已无入口、运行时忽略 | 按设计决策 D5 保留并恢复完整能力：UI、入队、执行和测试闭环 |
| 侧栏 QA | 与独立页双轨且渲染策略不同 | 默认收敛到独立页；若保留必须复用共享实现 |
| 每 5 秒 queue 轮询 | 结果未渲染且与 SSE 重复 | 删除或仅作为 SSE 断线 fallback |

## 6. 错误体系整改

### 6.1 后端错误模型

```go
type AppError struct {
    Code       ErrorCode
    PublicMsg  string
    Kind       ErrorKind // validation, conflict, unavailable, internal
    Retryable  bool
    Cause      error
    Fields     map[string]string
}
```

规则：

- `transport` 集中映射 `ErrorCode -> HTTP status + publicMessage`，不得散落字符串 code。
- 500 只返回 `code/message/requestId`；`Cause`、路径、SQL、远端响应只写日志。
- 禁止靠 `strings.Contains(err.Error(), "401")` 判断重试；LLM gateway 返回 typed upstream error。
- 保存/恢复/重建等操作返回可执行建议，例如“检查模型配置后重试”，而不是原始技术文本。
- “无数据”和“请求失败”是不同状态；前端不得在 catch 中清空数据伪装成功。

建议初始错误码域：

- `validation.*`：非法参数、缺少文件、配置范围错误。
- `workspace.*`：未初始化、切换中、冲突、不可访问。
- `index.*`：提取失败、嵌入失败、维度不匹配、重建中。
- `git.*`：仓库错误、提交冲突、恢复冲突。
- `ai.*`：未配置、认证失败、限流、超时、协议错误。
- `storage.*`：迁移失败、不可用、事务失败。
- `stream.*`：连接中断、取消、协议错误。
- `internal.unexpected`：未分类错误。

### 6.2 前端呈现

- API client 只接受 `unknown`，集中 `unwrapResponse` 和错误解析；删除广泛 `any`/`data!`。
- store 标准状态：`idle | loading | ready | refreshing | error`，保留上次成功数据。
- 全局 toast 只承载操作结果；页面级加载失败在内容区显示错误、request ID 和重试。
- 下载 Blob 根据 content-type 解析 JSON 错误；聊天气泡禁止展示完整 HTTP body。
- 设置、恢复、提交等写操作只有后端确认成功后才展示成功提示。

## 7. 日志与控制台整改

当前 Go 日志已是 JSON，前端没有 `console.log/error/debugger` 污染；问题是字段不统一、缺少等级过滤/关联/脱敏，部分 catch 又完全无诊断。

### 7.1 后端日志字段

必备字段：

```text
ts level component event requestId operationId workspaceGeneration
fileId taskId sessionId durationMs outcome errorCode retryable
```

约束：

- 控制台开发模式使用紧凑、对齐、带颜色的人类可读格式；文件/诊断导出使用 JSON Lines。
- 默认 INFO；DEBUG 由配置显式开启；不在 INFO 输出完整问题、文档正文、模型响应或 API Key。
- 路径默认记录相对路径或 hash；远端响应只记录 status、provider、request ID 和截断后的脱敏摘要。
- HTTP、任务、LLM、索引都记录开始/结束和耗时；同一操作贯穿一个 `operationId`。
- 加入大小/日期轮转和保留期；诊断包生成前再次脱敏。

### 7.2 前端诊断

- 建立小型 `logger`，生产默认只记录 warn/error 到内存环形缓冲，不直接污染 DevTools。
- 捕获未处理 Promise、Vue error handler、SSE parse/reconnect 状态，统一附 requestId/topic。
- 对预期取消和网络离线降级日志等级，避免错误风暴。
- 提供“复制诊断信息”入口，包含版本、连接状态和 request ID，不包含用户问题/密钥。

## 8. 分阶段实施路线

### Phase 0：冻结基线与发布止血（1-2 天）

范围：只做小改动和门禁，不做模块搬迁。

- 修复 `TimelinePage.vue` 构建阻断和四个 gofmt 漂移。
- 增加根级 `verify` 脚本；建立最小 Windows CI。
- 给当前 REST/SSE、工作区初始化、重建和文件恢复补 characterization tests。
- 强制 `.memora/` ignore 和提交排除；检查工作树、Git index 和可用历史是否已含敏感文件；发现泄露时停止使用并轮换对应 Key，历史清理作为单独、需确认的操作。
- 修复空文件旧索引、增量吞错、FullReindex 伪成功/向量基数校验、autoCommit 完整开关和 Git add 错误。

验收：

- clean checkout 一条命令完成 typecheck/build/test/vet/format check。
- `.memora/**` 无法被 Memora 提交；工作树/index/历史检查有记录；发现的 Key 已轮换。
- 空文件不会被搜到旧内容；少/多向量、单文件失败和 cleanup 失败均不会被报告为完整成功。

### Phase 1：生命周期与持久化一致性（4-7 天）

- 实现 Runtime/RuntimeManager 和 generation；HTTP/任务持有 runtime lease；watcher、reconciliation poller 和队列都归 Runtime 生命周期所有。
- 所有 FullReindex 归一到队列；同 generation 的触发合并，运行中最多保留一次 follow-up；支持取消、状态和排水。
- 将 context 贯穿 HTTP、任务、提取和 LLM；实现 `http.Server.Shutdown`、流式响应头/空闲超时及 poller 停止机制，再加入慢提取、慢 LLM、活动 HTTP 的故障测试。
- 建立数据库迁移框架和配置原子存储/迁移；实现 `ApplyWorkspaceConfig` 的候选验证、一次提交和失败回滚。
- 实现 `ReplaceFileIndex`、`SaveExchange` 等事务 API。
- 增加 `CredentialStore` 抽象，Windows 使用 DPAPI；启动时迁移旧明文 Key，迁移失败可回滚且不得清空原凭据。
- 恢复历史加入未跟踪文件冲突保护；为 browse/open/extract/index/restore 统一最终路径 containment。

验收：

- 同 generation 的 N 次并发触发最大并发数为 1、实际完整执行最多 2 次（当前运行 + 一次 follow-up）；不同 generation 不互相合并；工作区切换期间请求不会跨 generation 写入。
- 任意索引步骤失败后，查询只能看到完整旧版本或完整新版本。
- shutdown 故障测试覆盖活动 HTTP、poller、提取和流式 LLM；到期后日志列出 operationId/taskId，且测试无 DB-after-close。
- 首提交和当前 schema/config fixture 均可升级；每个迁移故障点可回滚，损坏文件可从备份恢复。
- 明文 Key 迁移后重启仍可用，`config.json`、日志和诊断包均不含 Key；迁移失败保留原可用状态。
- `ApplyWorkspaceConfig` 在每个配置写入、探测、Runtime 构建和交换故障点失败时，当前工作区、入口指针和目标已有配置均不变化。

### Phase 2：契约、错误和日志统一（3-5 天）

- 定义 typed AppError、错误码常量和集中 HTTP 映射。
- API response/SSE payload 全类型化；前端以 `unknown` 校验和 unwrap。
- 增加 requestId/operationId；落实日志字段、级别、脱敏和人类可读控制台格式。
- 修复前端静默 catch、Blob/raw body 错误、加载失败伪装空数据。
- HTTP 请求体限制和严格 decoder 在本阶段补齐；连接、响应头、空闲超时及 context 传播已作为生命周期依赖前移到 Phase 1。

验收：

- 契约测试覆盖所有公开路由、错误响应和 SSE topic。
- UI 不出现 SQL、Go error、完整 HTTP body 或 `[object Blob]`。
- 从任一 UI 错误 requestId 能定位一条完整后端操作链。

### Phase 3：收敛业务主链（5-8 天）

- 合并 Ask/AskStream 为单一执行管线。
- 收敛侧栏/独立问答，统一 ChatSurface、Markdown sanitizer 和会话并发控制。
- 收敛文件详情/历史/下载/恢复；删除不可达 `AllFilesPage`。
- 明确 commits 为唯一版本浏览模型，下线旧 timeline API/实现。
- 明确 search 为唯一查询编排层，删除旧 `index.Query`/IIndex 死依赖。
- 统一 provider/model 状态机、文档策略和搜索聚合契约。

验收：

- 每项核心业务只有一个 application entry point。
- 删除清单完成后无死路由、死参数、假兼容注释和拼写错误接口。
- 同一回答在所有入口使用同一渲染、安全和会话规则。

### Phase 4：模块拆分与前端状态治理（5-8 天）

- 按资源拆 transport；按 repository/transaction 拆 storage；按协议职责拆 llm。
- `App.vue` 只保留壳；SSE 同步逻辑进入 `useEventSync`。
- store 区分实体缓存和查询缓存；引入 AbortController/latest-generation。
- 页面业务抽成 feature composable 和共享组件；补键盘/modal/窄窗口行为。
- 路由懒加载，安全 HTML 缓存，移除重复格式化工具。

验收：

- transport handler 不直接创建 goroutine、不重建 runtime、不执行数据库事务。
- App 不包含聊天业务和轮询细节；文件恢复仅一份实现。
- 故障注入下慢请求不能覆盖新查询或新会话。

### Phase 5：性能和可观测性（3-6 天）

- 建立 500/5000 文件、10k/50k chunks 基准数据和 p50/p95 记录。
- 候选元数据批量 JOIN，消除 search/QA N+1。
- top-K 使用 bounded heap，避免全量排序；用 benchmark 决定是否引入 ANN。
- 8 秒全盘扫描改低频 reconciliation + 批量比较。
- SSE 刷新合并/节流；仅断线时轮询并在重连后全量对账。
- 文本缓存加入版本化 key、配额、TTL 和 cleanup；大文件流式 hash。
- 增加 health/ready/诊断摘要。

初始性能候选目标如下。Phase 5 开始时先把基准机 CPU/内存/磁盘、Go/Node 版本、向量维度、数据生成 seed、预热次数、正式运行次数（不少于 30 次）和冷热缓存条件写入 `benchmarks/README.md`；基线测量后再在同一 PR 固化阈值：

- 5k 文件/50k chunks 本地向量候选检索候选阈值 p95 < 300ms，不含远端 rerank。
- search/QA 每次候选元数据 SQL 查询数为 O(1)，不随 topK 线性增长。
- 空闲状态不进行 8 秒递归全盘扫描；SSE 正常时无 5 秒 queue 轮询。
- 全量索引期间前端同类刷新请求按秒级合并，不随 progress 事件数线性增长。

### Phase 6：发布工程与文档（2-4 天）

- 固定工具链，`npm ci`，版本/commit/build time 注入。
- tag 驱动 Windows 制品，生成 SHA-256、SBOM、变更日志和签名策略。
- 制品冒烟：首次启动、升级旧 DB/config、初始化、索引、搜索、问答、提交、恢复、重启。
- 更新 README、架构、错误码、日志、备份恢复和发布回滚文档。
- 将旧审计报告保留为历史记录，并标注对应 commit 和已失效结论。

## 9. 测试矩阵

| 层级 | 必测内容 |
|---|---|
| 单元 | 配置校验/原子写、迁移、错误映射、SSE parser、top-K、文档规则、Markdown sanitizer |
| repository | chunks/vectors 原子替换、空内容、QA exchange、rows.Err、迁移升级/回滚 |
| 并发 | 重建去重、runtime switch、event subscribe/cancel、shutdown drain、请求竞态 |
| 契约 | 每条 REST 方法/路径/DTO/error；每个 SSE topic payload 和异常 EOF |
| 前端组件 | loading/error/empty 区分、会话切换、恢复 modal token、设置回填/保存 |
| E2E | 临时工作区初始化 -> 索引 -> 搜索 -> QA -> commit -> diff -> restore -> restart |
| 安全 | `.memora` 排除、Markdown 恶意输入、请求体限制、路径/junction、日志脱敏 |
| 性能 | 500/5000 文件、10k/50k chunks；索引、搜索、QA context、timeline diff |
| 发布 | clean checkout build、旧版本升级、制品版本信息、校验和、回滚 |

## 10. 实施规则

1. 每个 Phase 单独分支/PR，禁止把生命周期重构、UI 改版和性能优化混在一个提交。
2. 先写 characterization test，再删除旧链；未证明无调用的公开 API 不直接删除。
3. 每项整改必须记录：问题 ID、行为变化、数据迁移、回滚方法，以及可复现的 `命令 / fixture / 故障点 / 观测值 / 阈值`；涉及热点时再附性能前后对比。
4. 不以“拆成更多文件”作为低耦合验收；以依赖方向、数据所有权、事务边界和替换测试为准。
5. 不先引入 HNSW、消息总线、微服务或通用 DI；现有规模先通过 batch、heap、事务和清晰边界解决。
6. 新兼容层必须有真实消费者、移除日期和测试；无法证明的“旧版兼容”直接清理。
7. 文档引用以 commit 固定；模块移动后同步更新架构和 ADR，不继续维护过期行号说明书。

## 11. 完成定义

系统达到“重新凝聚”的标准，不是大文件全部消失，而是满足以下可验证条件：

- 工作区、索引、问答、版本四条主链各只有一个明确 application entry point。
- runtime 切换、重建和关闭具有统一生命周期，测试无法制造跨库写和 DB-after-close。
- 数据库、配置、索引和问答写入具有明确原子性；失败后状态可恢复、可解释。
- API Key 不再以明文出现在配置、日志或诊断包中；旧配置迁移和泄露轮换有验证记录。
- transport、application、domain、infrastructure 依赖单向，死 contract 和双轨实现被删除。
- 前端每类查询有独立状态与竞态控制；错误、空数据、加载和刷新状态明确区分。
- `verify` 和 CI 全绿；生产构建、迁移、E2E、关键并发和安全测试成为发布门禁。
- 日志可读、可关联、可脱敏；用户错误信息稳定且包含可执行建议，不泄露内部细节。
- 在 5000 文件/50000 chunks 基准下达到固化的 p95 指标，空闲状态无无效高频扫描/轮询。
- 发布制品可从版本追溯到源码和工具链，升级前有备份，失败有回滚路径。

---

本计划是当前整改的唯一总纲。实施过程中若发现新问题，应先归入对应 ID/Phase 并补证据和验收标准，而不是继续以页面或函数为单位追加孤立补丁。
