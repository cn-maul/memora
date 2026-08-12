# Memora 项目指南（详细文档）

> 本文档是 Memora 项目的**综合使用与开发指南**，承接根目录 `README.md` 的项目简介，
> 覆盖：功能、快速开始、架构、目录结构、开发与验证、发布、数据与隐私、故障排查、备份与恢复、文档索引。
>
> 阅读路径建议：
> - 想快速了解怎么构建和运行 → [§2 快速开始](#2-快速开始)
> - 想了解系统怎么设计 → [§3 架构概览](#3-架构概览) 与 [§4 目录结构](#4-目录结构)
> - 想参与开发 → [§5 开发与验证](#5-开发与验证)
> - 要发布/升级/回滚 → [§6 发布](#6-发布) 与 [`RELEASE_RUNBOOK.md`](RELEASE_RUNBOOK.md)
> - 遇到问题 → [§8 故障排查](#8-故障排查)

---

## 1. 项目简介

Memora 是一个面向 Windows 的**本地单进程文档知识库**：在用户选定的工作区目录内建立索引，
提供文档语义搜索、全局/单文件 RAG 问答、Git 版本控制、统计与设置。

- **运行形态**：单可执行文件 `memora.exe`（Go 内嵌 Vue 构建产物），自动打开浏览器访问 `127.0.0.1:19000-20000`（端口自动探测）。
- **数据本地性**：数据（配置、数据库、缓存）均保存在工作区的 `.memora/` 目录中，不上传业务文档；问答内容仅流向用户自行配置的模型端点。
- **技术栈**：Go 后端（net/http、go-git、fsnotify、modernc SQLite）+ Vue 3 / TypeScript / Pinia 前端。

## 2. 快速开始

```bat
REM 构建（构建前端并内嵌到后端，输出 bin\memora.exe）
build.bat

REM 全量验证（前端类型检查+测试+构建、go vet、go test、gofmt 漂移检查）
verify.bat

REM 运行
bin\memora.exe
```

开发模式（前后端分离，热更新）：

```bat
REM 终端 1：前端 Vite 开发服务器（默认 5173 端口，/api 代理到后端）
cd frontend
npm install
npm run dev

REM 终端 2：后端
cd backend
go run ./cmd/memora
```

### 2.1 功能清单

| 功能 | 说明 |
|---|---|
| 文档索引 | 监听工作区文件变化自动建立索引，支持 pdf / docx / pptx / xlsx / txt / md；可手动全量重建 |
| 语义搜索 | 向量检索 + 文件名搜索混合，按相关性返回文件与命中片段 |
| 问答（RAG） | 全局或单文件模式，基于检索片段生成回答，支持流式输出（SSE）与引用来源 |
| 版本控制 | 内置 go-git：自动/手动提交、版本记录浏览、版本差异、单文件恢复 |
| 统计 | 提交趋势、文件变化、热门文件、标签分布、活跃时段 |
| 设置 | 工作区、AI provider / 模型、自动保存、隐私开关 |

## 3. 架构概览

系统保持**单进程、简单优先**，不引入微服务或大型 DI 框架。目标分层见
[`SYSTEM_REMEDIATION_PLAN.md`](SYSTEM_REMEDIATION_PLAN.md) §3「目标架构」的分层图，概要如下：

- **frontend**：Vue 3 单页应用，`app-shell`（导航/主题/连接状态）+ `features`（文件/搜索/问答/版本记录/统计/设置）+ `shared`（api-client / error-map / ui）。
- **transport**：HTTP server + middleware + 资源 handler + DTO 映射，只解析/校验请求并调用 application use case，不执行业务。
- **application**：用例编排、事务边界、任务提交与生命周期（runtime / workspace / indexing / chat 服务）。
- **domain**：文档策略、git、抽取、检索、标签、时间线、统计等核心规则。
- **infrastructure**：配置存储、凭据存储、SQLite 仓储、LLM 网关、事件流、任务队列。

依赖方向单向：`frontend → transport → application → domain → infrastructure`。

## 4. 目录结构

```
backend/
  cmd/memora/           主程序入口
  cmd/review_scan/      密钥泄露扫描工具（工作树 / index / 历史）
  internal/
    assembler/          装配与运行时生命周期（RuntimeManager、工作区原子切换）
    browser/            目录浏览
    config/             配置加载 / 迁移 / 原子写
    contract/           契约与共享类型（DTO、AppError 错误模型、接口）
    credstore/          凭据存储（Windows DPAPI 加密 API Key）
    documentpolicy/     文档类型 / 忽略目录 / 路径约束统一规则
    events/             轻量事件总线（发布/订阅）
    extract/            文本抽取（pdf/docx 等）与 text_cache 配额管理
    git/                go-git 版本管理（提交 / 恢复 / 泄露扫描）
    index/              语义索引（分块 / 向量写入）
    llm/                LLM 网关（chat / embed / rerank，统一重试与错误分类）
    logx/               结构化日志（JSON / console，敏感字段脱敏）
    pkg/                通用工具包
    qa/                 问答编排（单一执行管线）
    search/             混合搜索编排
    stats/              使用统计
    storage/            SQLite 存储与版本化迁移（PRAGMA user_version + 迁移前备份）
    tag/                智能标签
    taskqueue/          单队列任务处理器
    timeline/           版本摘要与历史回退
    transport/          REST / SSE / health 处理器与中间件
    watch/              文件监视（fsnotify）
    web/                go:embed 内嵌前端静态资源

frontend/
  src/
    api/client.ts       REST / SSE 封装与响应信封校验
    components/         通用组件（ChatSurface、FileHistoryDialog、OnboardingWizard 等）
    composables/        组合函数（useEventSync、useSettings 等）
    data/               provider / 模型预设
    router/             路由定义
    stores/             Pinia 状态（workspace / files / qa / settings / tags）
    types/              前后端契约类型
    utils/              错误映射（errors.ts）、状态文案、文件历史等
    views/              页面（Index / QA / RecentFiles / Stats / Timeline / Workspace / Settings）
```

## 5. 开发与验证

根目录 `verify.bat` 是本地发布门禁，依次执行 6 步（任一失败即非零退出）：

1. **前端类型检查**：`vue-tsc --noEmit`（严格类型检查，即 lint 门禁）。
2. **前端单元测试**：`npm run test`（Vitest，若 `package.json` 存在 `test` 脚本则执行）。
3. **前端生产构建**：`npm run build`（`vue-tsc -b && vite build`，在 `frontend/` 下）。
4. **后端静态检查**：`go vet ./...`。
5. **后端测试**：`go test -count=1 ./...`。
6. **格式漂移检查**：`gofmt -l backend` 输出必须为空。

单独执行的后端检查：

```bash
cd backend
go test -count=1 ./...   # 单元测试
go vet ./...             # 静态检查
gofmt -l .               # 格式检查（应无输出）
```

单独执行的前端检查（在 `frontend/` 下）：

```bash
npm run test      # Vitest 单元测试
npm run lint      # vue-tsc 严格类型检查
npm run build     # 类型检查 + 生产构建
```

## 6. 发布

```bat
REM 打标签并产出发布制品（tag 驱动，产物含校验和 / SBOM / 变更日志）
release.bat vX.Y.Z
```

发布流程、产物清单、升级与回滚步骤详见 [`RELEASE_RUNBOOK.md`](RELEASE_RUNBOOK.md)，要点：

- **产物清单**：`memora.exe`、`*.sha256`、`*sbom*`、`CHANGELOG`、`VERSION`。
- **升级前必须备份用户工作区 `.memora/`**（config.json、meta.db、text_cache）。
- 升级：停旧进程 → 覆盖 exe → 启动 → 打开 `/health`、`/ready` 确认就绪。
- 回滚：停进程 → 恢复备份的 `.memora/` → 换回旧 exe → 启动验证。
- 签名策略：当前制品**未签名**，可选使用 signtool（策略见 RELEASE_RUNBOOK §签名策略）。

## 7. 数据与隐私

- 每个工作区的 `.memora/` 目录存放运行时数据：`config.json`（配置）、`meta.db`（SQLite 数据库）、`text_cache`（抽取文本缓存）。`.memora/**` 被强制写入工作区 `.gitignore` 并在提交时二次排除（P0-01）。
- **API Key** 通过统一 `CredentialStore` 管理：Windows 实现使用 **DPAPI** 加密存储，旧明文配置启动时自动迁移；配置、日志与诊断包中均不出现明文 Key。
- **文本缓存**默认配额 512MB，超出后按最旧优先清理（LRU 式），并带版本化 key 与 TTL。
- 日志自动脱敏：字段名含 `apikey`、`token`、`password`、`secret` 或等于 `authorization` 的值一律输出为 `***`。
- HTTP 服务仅监听本机（127.0.0.1），CORS 只回显本机来源，请求体有大小上限，未知字段请求被拒绝。

## 8. 故障排查

### 8.1 错误码

完整错误码表见 [`ERROR_CODES.md`](ERROR_CODES.md)。错误响应统一为
`{ "code", "message", "requestId" }` 信封，code 前缀大致分域：

| 前缀 | 含义 |
|---|---|
| `bad_request` / `invalid_param` / `not_configured` / `request_too_large` | 请求/配置问题（validation） |
| `not_found` / `not_ready` | 资源缺失 / 服务未就绪（workspace / storage） |
| `unauthorized` / `forbidden` | 凭据或权限问题 |
| `conflict` / `rate_limited` / `timeout` / `canceled` | 冲突 / 限流 / 超时 / 取消 |
| `llm_error` | AI 上游错误（未配置、认证失败、限流、超时、协议错误） |
| `extract_error` | 文档抽取失败 |
| `internal` | 未分类内部错误（500，仅返回固定文案，不泄露内部细节） |

> 整改计划 §6 建议的 `validation.*` / `workspace.*` / `index.*` / `git.*` / `ai.*` / `storage.*` / `stream.*` / `internal.unexpected` 分域为演进方向，当前实现以 `backend/internal/contract/errors.go` 中的扁平 code 常量为准。

### 8.2 用 requestId 排查

- 每个错误/成功响应都带 `requestId`，同时写入响应头 `X-Request-ID`；前端统一从信封解析并展示在错误提示中。
- 后端日志中同一请求的整条操作链贯穿同一 `operationId`（HTTP、任务、LLM、索引都记录开始/结束与耗时）。
- 排查方式：取界面上显示的 `requestId` → 在日志中按 `requestId`/`operationId` 过滤 → 沿 `ts level module msg durationMs errorCode retryable` 字段还原操作链。日志字段规范见整改计划 §7。

### 8.3 诊断端点

| 端点 | 用途 | 说明 |
|---|---|---|
| `GET /health` | liveness | 进程活着即 200 `{"status":"ok"}`，不依赖任何模块 |
| `GET /ready` | readiness | 200 `{"status":"ready",...}`；未就绪 503 `{"status":"not_ready","reasons":[...]}`（含 storage 可用性、工作区是否初始化） |
| `GET /diagnostics` | 诊断摘要 | 版本、generation、队列深度（running/pending）、storage 可用性、缓存体积、uptime、最近错误 |

三个端点均为原始 JSON 输出（不带标准 `{code,data}` 包裹），便于编排/监控工具直接消费。

## 9. 备份与恢复

- **工作区级备份**：复制整个 `.memora/` 目录即可备份配置 + 数据库 + 缓存（建议在停止服务后复制以保证一致性）。
- **数据库迁移自动备份**：storage 在 schema 迁移前自动执行 WAL checkpoint 并把 `meta.db` 复制为 `meta.db.bak-<时间戳>`（同目录），迁移失败自动回滚且保留备份库（见 `backend/internal/storage/migrations.go`）。
- **文件恢复**：通过版本记录页将任意文件恢复到历史提交版本（内置未跟踪文件冲突检测）。
- **升级/回滚演练**：见 [`RELEASE_RUNBOOK.md`](RELEASE_RUNBOOK.md)。

## 10. 文档索引

| 文档 | 角色 |
|---|---|
| [`API_REFERENCE.md`](API_REFERENCE.md) | **第三方开发接口参考**：全部 REST/SSE 端点、参数、响应 DTO、错误码 |
| [`design.md`](../design.md) | 目标设计与决策记录（ADR，D1-D40）——设计意图参考，**不是源码事实** |
| [`SYSTEM_REMEDIATION_PLAN.md`](SYSTEM_REMEDIATION_PLAN.md) | 整改主计划（当前总纲，含问题台账 P0-P3 与 Phase 0-6 状态） |
| [`MEMORA_FUNCTION_SPEC.md`](MEMORA_FUNCTION_SPEC.md) | 基于源码的功能说明（数据模型、路由、模块职责） |
| [`FRONTEND_SIMPLIFY_PLAN.md`](FRONTEND_SIMPLIFY_PLAN.md) | 前端「小白化」简化改版计划 |
| [`SECRET_SCAN_REPORT.md`](SECRET_SCAN_REPORT.md) | 密钥与敏感文件泄露扫描记录 |
| [`RELEASE_RUNBOOK.md`](RELEASE_RUNBOOK.md) | 发布手册（tag 驱动发布、升级、回滚、冒烟清单） |
| [`ERROR_CODES.md`](ERROR_CODES.md) | 错误码表（code / HTTP 状态 / 含义 / 用户建议） |
| [`AUDIT_REPORT.md`](../AUDIT_REPORT.md) | 历史审计快照（已失效，仅留档） |
| [`benchmarks/README.md`](../benchmarks/README.md) | 性能基准说明与阈值记录 |
