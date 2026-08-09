# 智能文档库（暂名：Memora）详细设计书

> 版本：v1.4
> 本轮变更：新增 §5「通信契约」——REST 接口字段级定义、SSE 事件负载、错误码表、config.json JSON 结构与 snake_case 命名，消除前后端字段错配风险；据此细化端到端流程与前端接线。
> 上一版（v1.3）：弱模型实现风险审计与处置——go-git 落地对照表、`.doc` 直接提示另存、分块改 rune 数、任务池改单顺序处理器、LLM 模板与实现细节（附录 B/C）。
> 范围：首版（Windows 单平台，Go 后端 + Vue 3 前端，前期纯网页形态）。

---

## 0. 决策记录

| # | 议题 | 结论 |
|---|------|------|
| D1 | 目标平台 | 仅 Windows（Win10/11） |
| D2 | 设计书粒度 | 完整技术设计 + 行为契约级接口 |
| D3 | 文档规模 | 千级（500~5000 文件），块级索引数万条 |
| D4 | 隐私敏感度 | 可接受云端处理；问答数据仅流向用户配置的端点 |
| D5 | 自动提交策略 | 防抖自动提交，默认 90s 无改动即 commit（可配置） |
| D6 | 桌面壳 | 前期纯网页（Go 内嵌静态资源+打开浏览器）；后期 Wails v3 |
| D7 | 前端 | Vue 3 + TypeScript |
| D8 | 聊天模型 | 任意 OpenAI 兼容端点 |
| D9 | 嵌入模型 | 任意 OpenAI 兼容端点 |
| D10 | 向量检索 | 内存线性扫描（向量存 SQLite BLOB，启动全量载入） |
| D11 | Git 引擎 | go-git（用户无需安装 Git）；实现参考 §4.5 落地对照表 |
| D12 | 工作区 | 单工作区（架构预留多库接口） |
| D13 | 元数据存储 | 工作目录内 `.memora/`（写入 .gitignore） |
| D14 | 支持文件类型 | pdf、docx、txt、md（doc 不支持：置 ignored 并提示另存 docx） |
| D15 | 分类体系/虚拟分类树 | 推迟 v2 |
| D16 | 标签个性化学习 | 仅保存手动覆盖记录，不反哺自动打标 |
| D17 | 版本摘要生成时机 | 提交时异步生成并缓存 |
| D18 | 项目脉络模式 | 推迟 v2 |
| D19 | 时间线聚合 | 默认按天，可切换周/月 |
| D20 | 问答助手集成 | 首版仅通用 OpenAI 兼容端点；CLI 助手推迟 v2 |
| D21 | 超长上下文 | 向量检索裁剪（top-K 相关块拼接） |
| D22 | 全局 RAG 模式 | 首版包含 |
| D23 | 工作简报 | 仅手动导出；定时自动生成推迟 v2 |
| D24 | 统计隐私开关 | 默认开启，可一键关闭并清除数据 |
| D25 | 文档查看方式 | 全部在外部程序中打开，不内置预览/编辑器 |
| D26 | 界面语言 | 仅中文 |
| D27 | 图表库 | ECharts（vue-echarts 封装） |
| D28 | 后台运行形态 | 关闭主窗口后托盘常驻（可关） |
| D29 | 前后端通信 | REST（请求/响应）+ SSE 单向推送 |
| D30 | 模块化策略 | 模块低耦合、仅通过接口契约连接；文档级版本标注 |
| D31 | 接口契约粒度 | 行为契约级（自然语言+逻辑方法名，不写实现代码） |
| D32 | 模型调用 | 独立 LLM 网关模块统一封装限频/重试/密钥 |
| D33 | 命名 | 模块目录/逻辑名用英文，文档与注释用中文 |
| D34 | 最高原则 | 简单优先（见 §1.2） |
| D35 | 版本管理 | 仅文档级标注 + §13 变更记录 |
| D36 | 配置存储 | 仅 config.json 单一文件 |
| D37 | 分块单位 | 按字符(rune)数切块，token 估算用 rune 数近似（v1.3） |
| D38 | 任务池 | 单队列 + 单顺序处理器（v1.3） |
| D39 | `.doc` 处理 | 不做 Word COM 转换，直接提示另存 docx（v1.3） |
| D40 | 字段命名 | REST DTO 一律 camelCase；config.json 一律 snake_case；映射仅发生在 config 与 transport 两处（v1.4） |

---

## 1. 定位与总原则

### 1.1 核心定位

面向个人办公场景的 Windows 桌面工具。以本地 Git 仓库为版本管理核心，以云端嵌入模型为语义搜索引擎，将工作文件目录升级为**可版本回溯、可自然语言检索、可智能标签、可时间复盘、可文档问答、可习惯统计**的个人知识管理中心。

### 1.2 简单优先原则（最高指导原则，D34）

| # | 规则 | 含义 |
|---|------|------|
| R1 | 能不用库就不用库 | 标准库能解决的（HTTP、JSON、SSE、文件、进程）不引第三方库 |
| R2 | 能用内存就不用文件/索引 | 数据规模撑得住就用内存 + 简单结构 |
| R3 | 能做同步就不做异步 | 只有耗时 >1s 或会阻塞 UI 才异步；异步必经任务池 + 事件通知 |
| R4 | 单一数据源 | 同一数据只存一份、只有一个模块能写 |
| R5 | 不为未来做设计 | 只解决首版已验证需求；"以后可能"一律推迟 |
| R6 | 错误只分三类 | 可跳过 / 可重试 / 致命；超纲一律简化为"可跳过并提示" |
| R7 | 模块小而直白 | 一个模块只做一件事；实现是"顺序步骤清单"，不引入状态机/DI/泛型抽象 |
| R8 | 性能够用即可 | 满足千级规模即可，不追求尖端 |

### 1.3 弱模型实现风险与处置（v1.3 审计）

| 风险点 | 为什么弱模型易错 | 处置 |
|--------|------------------|------|
| go-git API 冷门 | 训练语料少、签名易猜错 | 保留 go-git，但 §4.5 给"落地对照表"（参考类型/方法 + 步骤 + 防坑），并要求实现前按官方 v5 文档核对签名 |
| `.doc` Word COM | PowerShell COM 语法细节极多 | 直接不支持（D39），删掉 COM 方案 |
| token 估算分块 | 自写 token 计算器易错 | 按 rune 数切块（D37），token_est = rune 数近似 |
| 任务池并发编排 | goroutine/channel 竞态死锁 | 单队列 + 单顺序处理器（D38） |
| LLM 提示词设计 | 自由度高，弱模型乱写 | 附录 B 给固定模板，实现只做字符串拼装 |
| 时间线分桶 | 时区/ISO 周易错 | 附录 C 精确定义天/周/月规则 |
| 向量 BLOB 编解码 | 字节序/类型易错 | 附录 C 明确 float32 小端格式 |
| SSE 长连接 | 响应头/帧格式易漏 | 附录 C 给帧格式与心跳 |

---

## 2. 总体架构与模块化设计

### 2.1 分层与模块图

```
┌──────────────────────────────────────────────────────────────┐
│ 第四层  UI（frontend，独立工程）：只调用 transport，不碰领域模块   │
└───────────────▲──────────────────────────────────────────────┘
                │ REST(请求/响应) + SSE(服务端推送)，唯一通信边界
┌───────────────┴──────────────────────────────────────────────┐
│ 第三层  Adapter：transport（REST 路由/SSE 推送）  app（装配/生命周期/托盘）│
└───────────────▲──────────────────────────────────────────────┘
                │ 只调用 taskqueue 与领域模块接口
┌───────────────┴──────────────────────────────────────────────┐
│ 第二层  Orchestrator：taskqueue（单队列单顺序处理器）             │
└───────────────▲──────────────────────────────────────────────┘
                │ 只调用领域模块接口
┌───────────────┴──────────────────────────────────────────────┐
│ 第一层  领域层：git  watch  extract  index  tag  search         │
│              timeline  qa  stats（彼此只认接口不认实现）         │
└───────────────▲──────────────────────────────────────────────┘
                │ 依赖基础层接口
┌───────────────┴──────────────────────────────────────────────┐
│ 第零层  基础层：config  storage  llm  events（不依赖任何人）      │
└──────────────────────────────────────────────────────────────┘
```

**模块清单：**

| 模块 | 英文名 | 契约版本 | 一句话职责 |
|------|--------|---------|-----------|
| 配置 | config | v1.0 | 配置 JSON 读写、密钥保管、版本字段 |
| 事件 | events | v1.0 | 极简发布/订阅，桥接 SSE |
| 存储 | storage | v1.0 | SQLite 与内存向量索引的唯一访问者 |
| 模型网关 | llm | v1.0 | 聊天/嵌入的 OpenAI 兼容调用：限频/重试/密钥 |
| 版本管理 | git | v1.0 | go-git 封装：提交/历史/diff/还原/初始化 |
| 文件监视 | watch | v1.0 | fsnotify 递归监视 + 防抖汇总 |
| 文本提取 | extract | v1.1 | MarkItDown 子进程提取 + 文本缓存 |
| 索引 | index | v1.1 | 分块/嵌入/向量写入/增量与全量 |
| 标签 | tag | v1.0 | 自动打标、标签库、建议确认、手动覆盖 |
| 搜索 | search | v1.0 | 语义检索 + 标签过滤 + 结果组装 |
| 时间脉络 | timeline | v1.0 | 时间轴聚合、版本摘要、回退 |
| 问答 | qa | v1.0 | 单文件/全局 RAG、上下文裁剪、会话 |
| 统计 | stats | v1.0 | 指标聚合、导出、隐私开关 |
| 任务池 | taskqueue | v1.1 | 单队列单顺序处理器：去重/暂停/恢复/断点 |
| 传输适配 | transport | v1.0 | REST 路由、SSE 推送、参数校验、DTO 转换 |
| 应用装配 | app | v1.0 | 装配根：生命周期、托盘、打开浏览器 |

### 2.2 依赖规则与分层约束（硬性）

1. 单向依赖：上层可依赖下层接口；同层模块**禁止互相 import**，协作经 taskqueue 或 events。
2. 接口即边界：领域模块只暴露 `I<模块名>` 契约（§4），实现私有。
3. 数据归属唯一：任何表/文件只能一个模块写。
4. 禁止环依赖：出现"A 要 B、B 要 A"时，公共部分下沉基础层或合并模块。
5. 路径/时区统一：工作目录根由 config 持有；rel_path；毫秒；本地时区；不得绕过 config 拼路径。

### 2.3 模块版本管理（文档级，D35）

- `版本 = MAJOR.MINOR`；破坏性变更升 MAJOR，新增能力升 MINOR。
- 只做文档纪律：变更在 §13 登记一行；**无运行时校验**。
- 实现方：文件名/方法名与 §4 一一对应；改接口先登记再动代码。

### 2.4 仓库目录结构（规划）

```
Memora/
├─ design.md
├─ backend/
│  ├─ go.mod
│  ├─ cmd/memora/main.go          # 进程入口：读配置→装配→拉起生命周期
│  ├─ internal/
│  │  ├─ contract/                # 只放接口契约（无实现）
│  │  │  ├─ config.go events.go storage.go llm.go
│  │  │  ├─ git.go watch.go extract.go index.go
│  │  │  ├─ tag.go search.go timeline.go qa.go stats.go
│  │  │  ├─ taskqueue.go transport.go app.go
│  │  ├─ <模块实现>/              # 每模块一目录
│  │  └─ assembler/               # 装配根：按顺序 new 并接线
│  └─ pkg/                        # 可复用小件
└─ frontend/
   ├─ src/api/                    # 唯一调用后端处（未来换 Wails 只动这里）
   ├─ src/views/ src/state/ src/components/
   └─ src/locales/zh.ts
```

> 目录与逻辑名英文；注释、错误文案、文档中文（D33）。

### 2.5 模块间通信三种形态

1. 同步调用：请求-响应（search→index）。
2. 异步编排：领域模块不互调；watch 发事件 → taskqueue 排队执行 → 完成发事件。
3. 数据共享：只经 storage 接口。

### 2.6 面向弱模型的编写约束（总要求）

1. "内部流程"是**必须照做**的步骤清单，不跳过、不自行发明。
2. §4 接口契约是**法律**。
3. 拿不准选默认值并注释"（默认值，待确认）"。
4. 错误按三类处理；不吞错，不把可重试当致命。
5. 日志中文 `[模块] 描述 字段`；不打印密钥。
6. go-git 能力实现前，先看 §4.5 对照表并按官方 v5 文档核对签名，**不臆造**。
7. 涉及 LLM 的调用，提示词一律用附录 B 模板，只做参数替换。
8. 实现完对照附录 A 自检。

---

## 3. 数据与存储

### 3.1 工作区目录结构

```
<工作目录>/
├─ <用户文件>…                      # 用户文件，由 Git 版本管理
├─ .git/                           # go-git 创建的仓库
└─ .memora/                        # 软件数据目录（写入 .gitignore）
   ├─ config.json                  # 唯一配置文件（含密钥）
   ├─ meta.db                      # SQLite（含向量 BLOB）
   ├─ text_cache/<sha256>.md       # 提取后 Markdown 缓存
   └─ exports/<时间戳>_report.md   # 手动导出的工作简报
```

### 3.2 SQLite 表（storage 模块独占）

```sql
-- 文件元数据
CREATE TABLE files (
  id            INTEGER PRIMARY KEY,
  rel_path      TEXT NOT NULL UNIQUE,
  size          INTEGER NOT NULL DEFAULT 0,
  mtime         INTEGER NOT NULL,
  content_hash  TEXT,
  doc_type      TEXT NOT NULL,            -- pdf/docx/txt/md/doc(ignored)
  index_status  TEXT NOT NULL DEFAULT 'pending',
                -- pending|extracting|embedding|indexed|failed|ignored
  last_error    TEXT,
  first_seen_at INTEGER NOT NULL,
  last_indexed_at INTEGER
);

-- 文本分块
CREATE TABLE chunks (
  id         INTEGER PRIMARY KEY,
  file_id    INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  seq        INTEGER NOT NULL,
  token_est  INTEGER NOT NULL,            -- = 块内 rune 数（D37）
  text       TEXT NOT NULL,
  UNIQUE (file_id, seq)
);

-- 向量（BLOB 本体，内存索引据此全量加载）
CREATE TABLE chunk_vectors (
  chunk_id INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
  vec      BLOB NOT NULL,                -- float32 小端连续字节（附录 C）
  dim      INTEGER NOT NULL
);

-- 标签库
CREATE TABLE tags (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  source      TEXT NOT NULL,              -- predefined|auto_generated|user_confirmed
  created_at  INTEGER NOT NULL
);

-- 文件-标签关联
CREATE TABLE file_tags (
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  origin  TEXT NOT NULL,                  -- auto|manual
  PRIMARY KEY (file_id, tag_id)
);

-- 手动修正留痕（仅记录）
CREATE TABLE tag_overrides (
  file_id    INTEGER NOT NULL,
  tag_name   TEXT NOT NULL,
  action     TEXT NOT NULL,               -- add|remove
  created_at INTEGER NOT NULL
);

-- 模型建议的新标签（需确认）
CREATE TABLE tag_suggestions (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL,
  reason      TEXT,
  suggested_by_file INTEGER NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending',  -- pending|accepted|rejected
  created_at  INTEGER NOT NULL
);

-- 提交摘要缓存
CREATE TABLE commit_summaries (
  commit_hash TEXT PRIMARY KEY,
  summary     TEXT NOT NULL,
  generated_at INTEGER NOT NULL
);

-- 问答会话
CREATE TABLE qa_sessions (
  id         INTEGER PRIMARY KEY,
  created_at INTEGER NOT NULL,
  mode       TEXT NOT NULL,               -- file|global
  file_id    INTEGER
);
CREATE TABLE qa_messages (
  id          INTEGER PRIMARY KEY,
  session_id  INTEGER NOT NULL REFERENCES qa_sessions(id) ON DELETE CASCADE,
  role        TEXT NOT NULL,              -- user|assistant
  content     TEXT NOT NULL,
  sources     TEXT,                       -- JSON：引用的 file/段落
  created_at  INTEGER NOT NULL
);
```

### 3.3 配置项（config 模块，仅 config.json）

| 键 | 默认 | 说明 |
|----|------|------|
| schema_version | 1 | 配置结构版本 |
| workspace.path | （必填） | 工作目录 |
| markitdown.command | `python -m markitdown "{file}"` | 提取命令模板，`{file}` 占位 |
| markitdown.pythonPath | 向导探测 | Python 解释器路径 |
| llm.baseUrl / apiKey / model | 空 | 聊天端点 |
| llm.temperature | 0.2 | 标签/摘要用低温度 |
| embed.baseUrl / apiKey / model | 空 | 嵌入端点 |
| embed.dimensions | 1024 | 向量维度 |
| git.authorName / authorEmail | Memora / memora@local | 自动提交作者 |
| autoCommit.enabled | true | 自动提交开关 |
| autoCommit.debounceSec | 90 | 防抖窗口 |
| index.chunkSize | 2000 | 目标块字符(rune)数（D37） |
| index.chunkOverlap | 256 | 块间重叠字符(rune)数 |
| qa.maxContextChars | 30000 | 单文件直发上限 |
| qa.systemPrompt | 附录 B 模板 | 可自定义 |
| stats.enabled | true | 统计开关 |
| tray.enabled | true | 托盘常驻 |

**config.json 结构与键映射（D40）：** §3.3 点分键 ↔ config.json snake_case 嵌套键，映射只发生在 config 模块（落盘/读盘）与 transport 模块（DTO 边界）：

| §3.3 点分键 | config.json 键 |
|-------------|----------------|
| workspace.path | workspace_path |
| markitdown.pythonPath | markitdown.python_path |
| markitdown.command | markitdown.command |
| llm.baseUrl / apiKey / model / temperature | llm.base_url / api_key / model / temperature |
| embed.baseUrl / apiKey / model / dimensions | embed.base_url / api_key / model / dimensions |
| git.authorName / authorEmail | git.author_name / author_email |
| autoCommit.enabled / debounceSec | auto_commit.enabled / debounce_sec |
| index.chunkSize / chunkOverlap | index.chunk_size / chunk_overlap |
| qa.maxContextChars / systemPrompt | qa.max_context_chars / system_prompt |
| stats.enabled | stats.enabled |
| tray.enabled | tray.enabled |

config.json 示例（snake_case）：
```json
{
  "schema_version": 1,
  "workspace_path": "D:/docs",
  "markitdown": { "python_path": "", "command": "python -m markitdown \"{file}\"" },
  "llm": { "base_url": "", "api_key": "", "model": "", "temperature": 0.2 },
  "embed": { "base_url": "", "api_key": "", "model": "", "dimensions": 1024 },
  "git": { "author_name": "Memora", "author_email": "memora@local" },
  "auto_commit": { "enabled": true, "debounce_sec": 90 },
  "index": { "chunk_size": 2000, "chunk_overlap": 256 },
  "qa": { "max_context_chars": 30000, "system_prompt": "" },
  "stats": { "enabled": true },
  "tray": { "enabled": true }
}
```

### 3.4 内存向量索引（storage 提供）

- 结构：`[]struct{ chunkID int64; vec []float32 }`，启动时从 `chunk_vectors` 全量读入内存。
- 查询：余弦相似度线性扫描取 top-K；数万块 × 1024 维毫秒级。
- 新增/删除：写库 + 更新内存（单写者）。
- 维度校验：与 `embed.dimensions` 不符 → 提示"配置已变更，请重建索引"（`index.FullReindex`）。

---

## 4. 接口契约（行为契约级）

> 约定：`模块.能力名` 为逻辑方法名；每能力给 **输入→输出 / 副作用 / 错误规则**；错误三类：**可跳过**（记录后继续）、**可重试**（退避≤3 次）、**致命**（终止任务并上报）。时间戳毫秒；路径 rel_path。

### 4.1 config（v1.0）

- 自有数据：`config.json`（唯一写者）。
- 能力：`config.Get(key)`、`config.Set(key, value)`（写 JSON+落盘，广播 `settings_changed`）、`config.Snapshot()`（剔除 apiKey）、`config.UpsertSecrets(密钥)`、`config.Workspace()`、`config.Migrate()`（按 schema_version 增量迁移）。
- 内部流程：启动一次性加载进内存；写先改内存再落盘；落盘失败回滚报"可重试"。
- 边界：不持有业务数据。

### 4.2 events（v1.0）

- 能力：`events.Notify(topic, data)`（同步广播 + 桥接 SSE）；`events.Subscribe/Unsubscribe`。
- 主题：`index_progress`、`extract_failed`、`commit_done`、`tag_done`、`suggestion_new`、`files_changed`、`stats_updated`、`settings_changed`、`task_queue`、`qa_ready`。
- 错误规则：某订阅者出错不影响其他，记日志。
- 实现要求（R7）：内部即 `topic → 一组函数` 的简单 map，无队列、无持久化。

### 4.3 storage（v1.0）

- 自有数据：`meta.db`、`text_cache/`、内存向量索引（独占写）。
- 能力：
  - `files.Upsert / FindByRelPath / Get / List(过滤:状态/标签/分页)`。
  - `files.MarkStatus(id, status, error)` → 状态机见附录 C.5。
  - `chunks.ReplaceForFile(fileId, chunks)` → 事务内删旧块+旧向量，再插新。
  - `chunks.ByFile / Get`。
  - `vectors.Insert(chunkId, vec) / Delete(chunkId) / LoadAll() / Search(vec, topK)`。
  - `tags.List（含计数）/ GetByName / Create(n, source)`。
  - `fileTags.Replace(fileId, 合并集) / ListByTag(tag)`。
  - `overrides.Append`。
  - `suggestions.Add / ListPending / SetStatus`。
  - `summaries.Upsert / Get`。
  - `qa.SessionsCRUD / AppendMessage / Sources`。
  - `RecoverPending()` → 启动时非终态重置为 pending。
- 内部流程：多步写入一事务；向量写库同时更新内存索引。
- 错误规则：锁冲突"可重试"；磁盘满"致命"。

### 4.4 llm（模型网关，v1.0）

- 能力：`llm.Chat(system, user, opts)`（temperature/maxTokens/是否 JSON）；`llm.ChatJSON(system, user, 结构说明)`；`llm.Embed(texts[])`；`llm.TestChat() / TestEmbed()`。
- 统一行为：限频 20 rps（可配）、退避重试≤3、超时 60s、429/5xx 重试、4xx 报错不重试；请求/响应结构见附录 C.3。
- 错误规则：网络/5xx/429 可重试；4xx/密钥错误致命并提示"检查 API 配置"。
- 边界：不缓存；不管业务语义。

### 4.5 git（v1.0）

- 自有数据：工作目录 `.git`（独占）。
- 能力：`git.EnsureRepo(path)`、`git.Status()`（modified/untracked/deleted）、`git.CommitAuto(filesList)`（无变化不提交）、`git.CommitManual(message)`、`git.Log()`、`git.DiffStats(hash)`（added/modified/deleted 数量）、`git.FileHistory(relPath)`、`git.ShowFileAt(relPath, hash)`、`git.RestoreFile(relPath, hash)`。
- 内部流程（自动提交）：Status → 空则不提交 → add 全部 → commit；标题 `自动提交：修改 N、新增 M、删除 K`，正文逐行列文件；作者用 `git.authorName`。
- 错误规则：Status 失败可重试；提交锁/冲突可跳过该批保留变更；RestoreFile 前由 timeline 校验工作区干净。
- **go-git 落地对照表**（实现前必读；签名为 go-git v5 官方符号，不臆造，拿不准按官方文档核对）：

| 能力 | 参考类型/方法 | 关键步骤与防坑 |
|------|---------------|----------------|
| EnsureRepo | `git.PlainInit(path, false)` | 先 `git.PlainOpen` 探测存在性，存在则跳过；返回的 `*git.Repository` 全局复用 |
| Status | `repo.Worktree().Status()` | 返回 `git.Status`（map）；key=相对路径；code 含 `??`=未跟踪、`M`=修改、`D`=删除、`A`=新增 |
| CommitAuto | `worktree.Add(path)` → `worktree.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name,Email,When}})` | 逐个 add 后一次 commit；Author 用 `object.Signature`，When 用当前本地时间 |
| Log | `repo.Log(&git.LogOptions{})` → 迭代 `*object.Commit` | 迭代到 iterator 结束；取 `Hash().String()`、`Author().When`、`Message()`；每步判断 `Err` |
| DiffStats | 对 commit 取父：`commit.Parent(0)`（无父则整树为新增）→ `commit.Tree()` 与父 `Tree()` 的 `Diff` → `object.Changes` | 每条 Change：From 为空=新增，To 为空=删除，否则=修改；统计三类数量即可，不做 patch 内容 |
| FileHistory | `repo.Log` 迭代 + 每条 commit 的 Changes 含该 rel_path | 匹配 rel_path 即记入 |
| ShowFileAt | `commit.Tree()` → `tree.File(rel_path)` → `file.Contents()` | 返回字符串内容；文件不存在则该 commit 无此文件（用于历史版本预览） |
| RestoreFile | 同上取内容 → 直接写回工作区文件 | 不经 git 写盘；写盘前确认工作区干净（timeline 校验） |

> 通用防坑：不要在多个 goroutine 同时用一个 `*git.Repository` 做写操作；本设计 git 调用全部在 taskqueue 单处理器内串行执行，天然安全。提交空 diff 会报错，故自动提交前必查 Status。

### 4.6 watch（v1.0）

- 能力：`watch.Start/Stop/Pause/Resume`。
- 忽略规则：`.git`、`.memora`、隐藏文件、`.tmp/.~/.lock` 后缀、`~` 开头。
- 内部流程：fsnotify 递归监听 → 90s 防抖合并 → 到期发一次 `files_changed`（added/modified/removed）→ taskqueue 派发；删除另发 `file_removed`。
- 错误规则：目录不可读可跳过并通知；监听失败致命。
- 边界：只感知汇总，不做提交。

### 4.7 extract（v1.1）

- 自有数据：`text_cache/<sha256>.md`（独占写）。
- 能力：`extract.Probe(pythonPath, command)` → `{ok, message}`（向导建临时 txt 实测）；`extract.ExtractFile(file)` → `{text, cacheKey}`；`extract.Cleanup()`。
- 内部流程：模板替换 `{file}` → 经 `pythonPath` 起子进程（stdout=Markdown）→ 写缓存 → 返回。
- 格式细则：docx/pdf/txt/md 直接提取；**doc 置 `ignored`**，提示"请另存为 docx"（D39，不做 COM）；pdf 提取为空 → `failed` 提示"疑似扫描件，OCR 不在首版"。
- 错误规则：命令不存在/超时(60s) 可跳过记 `last_error`；模板错误致命（向导强制验证）。
- 边界：不解析语义；不触发模型；非交互启动子进程。

### 4.8 index（v1.1）

- 依赖：extract、storage、llm.Embed、events。
- 能力：`index.FullReindex()`、`index.Incremental(changed, removed)`、`index.ProcessFile(file)`、`index.DeleteFile(relPath)`、`index.Query(vec, topK, tagFilter)`。
- 内部流程（ProcessFile）：
  1. 置 `extracting`；`extract.ExtractFile`。
  2. 成功 → 算 `content_hash`；与库中相同则置 `indexed` 结束（幂等）。
  3. 分块（**按 rune 数**，算法见附录 C.4）；得块序列与 token_est。
  4. 置 `embedding`；`llm.Embed` 分批（每批 ≤16 段）向量化。
  5. 事务内 `chunks.ReplaceForFile` + `vectors.Insert` + 置 `indexed`。
  6. 广播 `index_progress`。
- 错误规则：提取失败走 extract 分类；嵌入失败可重试，3 次后置 `failed` 不阻塞其他。
- 边界：不调聊天模型；不生成标签。

### 4.9 tag（v1.0）

- 依赖：storage、llm.Chat、events。
- 能力：`tag.ProcessFile(file)`、`tag.ManualOverride(fileId, add[], remove[])`、`tag.ListLibrary() / ListSuggestions() / Accept(id) / Reject(id)`。
- 内部流程（ProcessFile）：
  1. 取文本前 8000 字符为样本。
  2. `llm.ChatJSON`，**提示词用附录 B.1 模板**；要求返回 `{tags:[1~3], new_tags:[≤2, 含理由]}`。
  3. 命中写 `file_tags(auto)`；新标签写 `tag_suggestions(pending)`。
  4. 广播 `tag_done`。
- 决策规则：被拒 3 次的候选名进模板"禁用词"；模型不可用 → 保留未打标，不阻塞。
- 边界：不改标签库结构；不写正文。

### 4.10 search（v1.0）

- 依赖：llm.Embed、storage、index.Query。
- 能力：`search.Query(q, tagFilter, page)` → 分页结果（rel_path、命中块、高亮句、分数、标签、mtime）。
- 内部流程：解析 `tag:xxx` → Embed(query) → `index.Query` top-K=20 → 按文件分组去重 → 组内最高分块代表 → 排序（命中块数优先、其次最高分；标签命中 +0.5）→ 组装高亮句。
- 错误规则：嵌入失败可重试；无结果返回空列表。
- 边界：不做答案生成。

### 4.11 timeline（v1.0）

- 依赖：git、storage、llm.Chat、events。
- 能力：`timeline.Get(granularity, tagFilter, from, to)`、`timeline.NodeDetail(node)`、`timeline.GenerateSummary(commitHash)`、`timeline.Restore(relPath, hash)`。
- 内部流程（Get）：`git.Log` 全量 → 未跟踪文件按 mtime 补入桶 → 分桶（规则见附录 C.6）→ 标签过滤。
- 摘要（提交时 taskqueue 异步派发）：`llm.Chat`，**提示词用附录 B.2 模板**，输入 diff 统计+文件名清单 → 1~2 句中文 → 写缓存。
- 错误规则：摘要失败不阻塞展示；Restore 不干净拒绝并给清单。
- 边界：不做 diff 渲染；不回退 `.memora`。

### 4.12 qa（v1.0）

- 依赖：storage、llm.Chat、llm.Embed、index.Query。
- 能力：`qa.Ask(sessionId, mode, fileId?, question)`、`qa.Sessions/NewSession/Clear/Delete`。
- 内部流程：
  1. file 模式：全文 ≤`qa.maxContextChars` 直发；超限 → Embed(question)+Query 该文件内 top-K 拼接。
  2. global 模式：Embed(question)+Query 全库 top-K（8 块）拼接。
  3. `llm.Chat`，**提示词用附录 B.3 模板**，上下文块带 `[文件=rel_path, 段落=seq]` 标注。
  4. 存消息与 sources；广播 `qa_ready`。
- 决策规则：来源标注映射回真实文件；模型未标注则不臆造。
- 错误规则：端点失败可重试，失败不回写历史；空问题拒绝。

### 4.13 stats（v1.0）

- 依赖：git、storage。
- 能力：`stats.Enabled/SetEnabled`、`stats.Summary(range)`、`stats.Export(format, range)`（CSV/MD；PNG 由前端出）、`stats.Purge()`。
- 指标：活跃度（commit 次数、增/改/删）、热度榜 Top N、时段分布、标签分布、单文件迭代速率。
- 错误规则：关闭时查询返回"已关闭"，前端引导开启。
- 边界：不读文件正文。

### 4.14 taskqueue（v1.1）

- 自有数据：内存队列；落库仅 `files.index_status` 作断点。
- 能力：`taskqueue.Submit(task)`、`Pause()/Resume()`（全局）、`Status()`、`CancelAll()`。
- 任务结构：`{type: extract|tag|summarize|reindex|delete_index, payload}`；执行即调对应领域模块。
- 内部流程（**单顺序处理器**，D38）：
  1. Submit：若同 rel_path 任务已在队列或处理中，丢弃重复。
  2. 队列 FIFO；一个 processor goroutine 循环取队头执行 → 完成发事件 → 下一项。
  3. Pause：processor 在取任务前等待信号量；Resume 恢复。
  4. CancelAll：清空未处理队列（处理中的不打断）。
  5. 崩溃恢复：启动调 `storage.RecoverPending()` 把非终态文件重置 pending 再入队。
- 错误规则：任务失败按模块错误分类入队或放弃；同类连续失败 5 次暂停该族并通知 UI。
- 边界：不执行业务逻辑；不做持久化队列。

### 4.15 transport（v1.0）

- 职责：REST 路由、SSE 推送、参数校验、DTO 转换；仅监听 127.0.0.1。
- 能力：`transport.Handle(路由表)`；`transport.SSE()`（订阅 events 推送主题，帧格式见附录 C.2）。
- 内部流程：请求 → 校验 → 调模块接口 → 序列化；SSE 连接管理（连接集合、心跳、断线清理）。
- 错误规则：非法参数 `{code:"bad_request"}`；模块错误映射 `{code, message}`；不暴露内部栈。
- 边界：不写业务表；不启子进程。

### 4.16 app（v1.0）

- 职责：装配根 + 生命周期 + 托盘。
- 能力：`app.Run()`（读配置 → new 全部模块接线 → 起 transport → 起 watch → 恢复任务 → 打开浏览器；待 Wails 接管）、`app.Shutdown()`（停 taskqueue → 停 watch → storage 落盘 → 停 transport）、`app.Quit()/ShowWindow()`。
- 托盘菜单：打开主界面 / 立即提交 / 暂停自动任务 / 退出。
- 边界：不做业务。

---

## 5. 通信契约（REST API、SSE 事件负载与 DTO）

> 本节是前端 `src/api/` 与后端 transport 的**唯一事实来源**：字段名、错误码、事件负载都以本节为准，两端实现时逐字对齐，不得自行改名。

### 5.1 通用约定

- 前缀 `/api`；端口随机绑定 127.0.0.1，仅本机可访问。
- 成功响应：`{ "code": "ok", "data": <负载> }`；失败：`{ "code": "<错误码>", "message": "<中文>", "data": <可选> }`。
- **字段命名（D40）**：REST DTO 一律 camelCase；config.json 一律 snake_case。映射只发生在 config 模块与 transport 模块，其他地方不得出现另一套命名。
- 时间戳毫秒整数；路径一律 rel_path；分页参数 `page`/`pageSize`，`page` 从 0 起。
- 错误码见 §5.9。

### 5.2 工作区与向导

- **POST /api/workspace/init**
  - 请求：`{ workspacePath, markitdown:{pythonPath, command}, llm?:{baseUrl, apiKey, model, temperature}, embed:{baseUrl, apiKey, model, dimensions} }`
  - 流程：`git.EnsureRepo` → config 写入（含密钥）→ `extract.Probe` 验证 → `llm.TestEmbed` 验证（失败中止）→ `llm.TestChat`（可选，可跳过）→ `index.FullReindex` 入队。
  - 响应：`{ ok: true }`；错误码：`markitdown_probe_failed`、`llm_test_failed`（message 带回失败原因）。
- **GET /api/workspace/info**
  - 响应 data：`{ initialized, workspacePath, dirtyCounts:{modified, untracked, deleted}, markitdownConfigured, llmConfigured, embedConfigured }`

### 5.3 文件

- **GET /api/files?status=&tag=&page=&pageSize=**
  - 外层：`{ page, pageSize, total, items }`；item：`{ id, relPath, size, mtime, docType, indexStatus, lastError, lastIndexedAt, tags:[{name, origin}] }`。
- **GET /api/files/{id}** → data 为单条同上。
- **GET /api/files/{id}/text** → data：`{ text }`（提取的 Markdown）。
- **POST /api/files/{id}/open** → 外部打开，data：`{ ok: true }`。
- **GET /api/files/{id}/history** → data：`{ commits:[{hash, time, message, author}] }`。

### 5.4 搜索与标签

- **GET /api/search?q=&tag=&page=**
  - item：`{ fileId, relPath, hitText, score, tags, mtime, matchedChunks }`；外层分页。
- **GET /api/tags** → data：`{ tags:[{id, name, source, count}] }`。
- **POST /api/files/{id}/tags**
  - 请求：`{ add:[], remove:[] }`；校验：add/remove 不重名、trim 后非空、长度 ≤20。
  - 响应 data：`{ tags:[{name, origin}] }`。
- **GET /api/tag-suggestions** → data：`{ suggestions:[{id, name, reason, fileId, relPath, createdAt}] }`。
- **POST /api/tag-suggestions/{id}/accept**；**POST /api/tag-suggestions/{id}/reject** → data：`{ ok: true }`；不存在 → `not_found`。

### 5.5 时间线

- **GET /api/timeline?granularity=day|week|month&tag=&from=&to=**
  - data：`{ nodes:[{ bucket, label, count, added, modified, deleted, summary?, files:[{relPath, mtime, commitHash?}] }] }`。
- **POST /api/commits/{hash}/summary** → 手动生成摘要，data：`{ summary }`。
- **POST /api/files/{id}/restore**
  - 请求：`{ commitHash }`；响应 data：`{ ok: true }`；工作区不干净 → code：`workspace_dirty`，data：`{ modified:[...] }`，前端列出清单让用户处理后重试。

### 5.6 问答

- **POST /api/qa**
  - 请求：`{ sessionId?, mode: file|global, fileId?, question }`
  - 响应 data：`{ answer, sources:[{relPath, seq}], sessionId }`。
- **GET /api/qa/sessions** → data：`{ sessions:[{id, mode, fileId, createdAt, messageCount}] }`。
- **GET /api/qa/sessions/{id}/messages** → data：`{ messages:[{role, content, sources, createdAt}] }`。
- **DELETE /api/qa/sessions/{id}** → data：`{ ok: true }`。

### 5.7 统计

- **GET /api/stats?range=week|month|quarter|custom&from=&to=**
  - data：`{ enabled, metrics:{ commitsByDay:[{date, count}], fileChanges:{added, modified, deleted}, hotFiles:[{relPath, count}], hourBuckets:{morning, afternoon, evening, night}, tagDistribution:[{tag, count}], iterationRate } }`。
  - `enabled=false` → code：`stats_disabled`，前端引导开启。
- **GET /api/stats/export?format=csv|md&range=**
  - 直接返回文件内容：`Content-Type: text/csv` 或 `text/markdown`；`Content-Disposition: attachment; filename=report_<时间戳>.csv|md`。

### 5.8 提交、设置与测试

- **POST /api/commits/auto** → data：`{ hash }`；无变化 → `{ skipped: true }`。
- **GET /api/settings** → data：config.Snapshot()（**不含 apiKey**）。
- **PUT /api/settings**
  - 请求：需修改的点分键子集（非密钥键）；响应 data：`{ ok: true }`。
- **PUT /api/settings/secrets**
  - 请求：`{ llmApiKey?, embedApiKey? }`；走 config.UpsertSecrets，不回显；响应 `{ ok: true }`。
- **POST /api/test/markitdown**
  - 请求：`{ pythonPath, command, file? }`（file 省略则用临时 txt）；响应 data：`{ ok, message }`。
- **POST /api/test/llm**
  - 请求：`{ type: chat|embed }`；响应 data：`{ ok, message }`。

### 5.9 错误码表

| code | 含义 | 前端动作 |
|------|------|----------|
| bad_request | 参数非法 | 表单标红提示 |
| not_found | 资源不存在 | 提示后返回 |
| not_configured | 缺配置 | 跳转设置页 |
| markitdown_probe_failed | 提取测试失败 | 向导停留该步 |
| llm_test_failed | 模型测试失败 | 向导停留该步 |
| llm_unavailable / embed_failed | 模型调用失败 | 提示后重试 |
| extract_failed | 提取失败 | 记入文件错误状态 |
| workspace_dirty | 回退前工作区不干净 | 列出清单 |
| stats_disabled | 统计已关闭 | 引导开启 |
| internal | 未预期错误 | 显示 message |

### 5.10 SSE 事件负载（与 4.2 主题一一对应）

| 主题 | 负载字段 |
|------|----------|
| index_progress | `{ done, total, current }` |
| extract_failed | `{ relPath, error }` |
| commit_done | `{ hash, added, modified, deleted }` |
| tag_done | `{ fileId, relPath, tags:[names] }` |
| suggestion_new | `{ id, name, reason }` |
| files_changed | `{ added:[], modified:[], removed:[] }` |
| stats_updated | `{}` |
| settings_changed | `{ key }` |
| task_queue | `{ running, pending }` |
| qa_ready | `{ sessionId }` |

前端 `src/api/sse.ts` 用一个 `EventSource('/api/events')` 按 topic 分发到 Pinia；事件字段名与上表逐字一致。

---

## 6. 端到端流程（模块协作）

1. **首次向导**：`POST /api/workspace/init` → `git.EnsureRepo` → config 写入 → `extract.Probe` 验证（不过则停）→ `llm.TestEmbed` / 可选 `llm.TestChat` → `index.FullReindex` 入队 → 前端看 `index_progress`。
2. **自动提交+增量索引**：watch 捕获 → 90s 防抖 → `files_changed` → taskqueue → `git.CommitAuto`（`commit_done`）→ 变化文件 extract→embed→tag；删除 `delete_index`；全程事件推送。
3. **语义搜索**：`search.Query` 同步链路。
4. **时间线**：`timeline.Get` → 节点详情/摘要重试 → 回退前二次确认 → `timeline.Restore` → watch 重建索引。
5. **问答**：`qa.Ask` 内联完成，会话与来源落库。
6. **统计**：`stats.Summary/Export`；关闭状态引导开启。

---

## 7. 前端设计

### 7.1 页面与路由

| 路由 | 页面 | 关键内容 |
|------|------|----------|
| `/` | 工作区/文件列表 | 搜索框、标签墙、文件列表、未提交变更红点、立即提交 |
| `/timeline` | 时间脉络 | 天/周/月切换、纵向时间轴、节点详情、回退、标签过滤 |
| `/qa` | 问答 | 会话列表 + 对话面板 + 单文件/全局切换 + 引用溯源 |
| `/stats` | 统计 | ECharts：热力/柱状/饼图、范围切换、导出 |
| `/settings` | 设置 | MarkItDown、模型端点、自动提交、隐私、托盘、测试 |
| `/onboard` | 首次向导 | 选目录→配提取→配嵌入→（可选）配聊天→建索引 |

### 7.2 组件与工程要点

- 标签组件：彩色 chip，hover 显示来源，右键删除。
- 时间线组件：ECharts 自定义系列或自绘，支持展开/收起。
- 搜索结果：命中句高亮；每项展示标签与相关度。
- **API 层抽象**：`src/api/` 是前端唯一发请求处；未来 Wails 化只替换此目录。
- 状态：Pinia 管理 工作区信息、任务队列、标签库、待确认标签、未提交变更数。
- SSE：一个 `EventSource('/api/events')` 按 topic 分发到 Pinia；断线浏览器自动重连。
- 文案集中 `src/locales/zh.ts`。

---

## 8. 后端工程与并发

- 单进程；端口随机（127.0.0.1），冲突 +1 重试。
- taskqueue：单队列 + 单顺序处理器 + rel_path 去重 + 全局暂停 + 断点恢复（D38）。
- 嵌入限频 20 rps（llm 模块内）；LLM 调用 1~2 并发；提取子进程串行。
- 幂等键：文件=`content_hash`，摘要=`commit_hash`。
- git 调用全部在单处理器内串行，规避 go-git 并发写问题（§4.5）。
- 日志中文 `[模块] 描述 字段`；不含密钥。

---

## 9. 错误处理与容错

| 场景 | 分类 | 处理 |
|------|------|------|
| MarkItDown 未配置/模板错 | 致命 | 向导强制通过；运行期红点 |
| 单文件提取失败 | 可跳过 | 记 `last_error`，不阻塞批次 |
| 嵌入限流/超时 | 可重试 | 退避≤3 次，仍失败置 `failed` |
| LLM 不可用 | 可跳过 | 标签/摘要/问答各自降级 |
| SQLite 锁冲突 | 可重试 | 退避重试 |
| Git 提交冲突/锁定 | 可跳过 | 保留变更下轮再试 |
| 端口冲突 | 可重试 | 自动递增 |
| 工作区不干净回退 | 拒绝 | 返回清单，处理后再试 |

---

## 10. 安全与隐私

- 密钥仅存 `.memora/config.json`，文件 ACL 当前用户；前端不持久化密钥。
- 模型调用默认 HTTPS；非 localhost 端点 UI 明示"数据离开本机"。
- 统计仅元数据；关闭开关即停采集，`Purge` 清除统计缓存。
- 回退前二次确认。
- `.memora` 不进 Git；导出报告默认不含正文。

---

## 11. 首版范围与 v2 展望

### 首版（v1）
D1~D40 全部决策对应能力；四大功能齐备。

### 推迟到 v2
分类体系/虚拟分类树（D15）、项目脉络（D18）、CLI 助手（D20）、定时简报（D23）、标签个性化学习（D16）、多工作区（D12）、Wails v3 桌面化（D6）、PDF OCR、`.doc` 原生提取、HNSW 升级（仅当线性扫描实测不满足时）。

---

## 12. 迭代规划

| 里程碑 | 内容 | 验收要点 |
|--------|------|----------|
| M1 底座 | config/events/storage/app 装配、向导、extract+Probe、watch+防抖、git 自动提交 | 配置持久化；改文件 90s 自动 commit；提取测试通过 |
| M2 索引与搜索 | 分块、llm.Embed、内存向量、增量/全量、search.Query | 千级文件断点续跑；自然语言查询返回相关段落 |
| M3 标签 | tag 管线、标签墙、手动覆盖、建议确认 | 新文件自动打标；修正即时生效可追溯 |
| M4 时间线 | 聚合、摘要缓存、历史与回退 | 按时间复盘；回退安全 |
| M5 问答 | qa 单文件/全局、裁剪、会话引用 | 两类问答可用，引用可溯源 |
| M6 统计 | 指标、图表、导出、隐私开关 | 报表与 git log 一致；关闭后无采集 |
| M7 收尾 | 托盘、优雅退出、全量测试、打包 | 常驻/退出无残留；可分发单文件包 |

---

## 13. 模块版本变更记录

| 版本 | 模块 | 变更 | 说明 |
|------|------|------|------|
| v1.0 | 全部 16 模块 | 初始定稿 | 随 v1.2 定版 |
| v1.2 | storage/index | D10 | 向量改内存线性扫描 |
| v1.2 | transport | D29 | WebSocket → SSE |
| v1.2 | config/storage | D36 | 删除 settings 表 |
| v1.2 | taskqueue | 简化 | 去优先级/多类型 worker |
| v1.2 | app | D35 | 移除运行时版本校验 |
| v1.3 | git | D11 细化 | 新增 go-git 落地对照表（接口不变） |
| v1.3 | extract | D39 | `.doc` 不再 COM 转换，置 ignored |
| v1.3 | index | D37 | 分块改 rune 数；token_est=rune 数 |
| v1.3 | taskqueue | D38 | worker 池 → 单顺序处理器 |
| v1.4 | transport | D40 | 通信契约细化：REST 字段级/错误码/SSE 负载（接口不变） |
| v1.4 | config | D40 | 明确 config.json JSON 结构与 snake_case（接口不变） |

## 开放项
1. 嵌入限频默认 20 rps 需按厂商调整（可配置）。
2. 内存向量索引加载耗时需千级实测（预期 <1s）。
3. "回退到该版本"语义：恢复文件快照而非 `git reset`，交互细节 M4 细化。
4. go-git 与特定文件类型的 .gitattributes 过滤规则冲突（如 LFS）不在首版考虑。

---

## 附录 A：实现方检查清单（实现后逐条自检）

1. 契约对齐：每个实现文件只对应 §4 契约能力，未列的不加。
2. 依赖纪律：不 import 其他模块实现目录；跨模块只走契约。
3. 流程照做：§4"内部流程"按序号执行。
4. 错误三类：写 `last_error` 或回滚；不吞错，不把可重试当致命。
5. 单一数据源：只写自己"自有数据"的表/文件。
6. 幂等：`content_hash`/`commit_hash` 判重。
7. 事务：多表写必须一事务，失败整体回滚。
8. 路径/时间：rel_path + 毫秒 + 本地时区；不绕过 config 拼路径。
9. 日志/密钥：中文 `[模块]` 日志；密钥不进日志/库/Git。
10. 同步优先：能同步不异步；异步必须有事件通知。
11. go-git：实现前核对 §4.5 对照表与官方 v5 文档签名，不臆造；单处理器内串行调用。
12. LLM：提示词一律用附录 B 模板，只做参数替换。
13. 默认值：拿不准选默认值并注释"（默认值，待确认）"。
14. 变更登记：改接口先在本设计书 §13 登记 MINOR+1，再动代码。
15. 字段命名：REST DTO 用 camelCase（按 §5）、config.json 用 snake_case（按 §3.3 config.json 映射表）；两端映射只发生在 transport 与 config 模块，其他地方不得混用。

---

## 附录 B：LLM 提示词模板（只做参数替换，不改结构）

### B.1 标签生成（tag.ProcessFile）
- system 内容：
  > 你是文档分类助手。从下面的预定义标签库中为文档选择 1~3 个最贴切的标签；仅当确实没有合适标签时，才建议 1~2 个新标签并说明理由。禁止使用禁用词列表中的标签。只输出 JSON，格式：{"tags":["标签1","标签2"],"new_tags":[{"name":"新标签","reason":"理由"}]}
  > 标签库：合同、报告、会议纪要、数据、图纸、简历、发票、方案、清单、学习笔记、通知、制度、流程、分析、审批、日程、通讯录、表单、模板、其他
  > 禁用词：{禁用词列表，逗号分隔}
- user 内容：文档前 8000 字符原文。
- 解析：期望 `tags` 数组（1~3）、`new_tags`（≤2）。解析失败重试 1 次，再失败返回空，文件留未打标。

### B.2 提交摘要（timeline.GenerateSummary）
- system 内容：
  > 你是版本记录助手。根据一次 Git 提交的文件改动，用 1~2 句中文总结这次提交做了什么。只输出总结文本，不要输出其他内容。
- user 内容：
  > 修改文件：{文件清单，逗号分隔}
  > 改动统计：新增 {a}、修改 {m}、删除 {d}。
- 输出：直接作为 summary 文本存入 `commit_summaries`。

### B.3 文档问答（qa.Ask，单文件与全局共用）
- system 内容：
  > 你是文档问答助手。根据提供的文档片段回答问题。每个片段以 [文件=路径, 段落=序号] 开头。回答时若引用了片段内容，注明对应文件路径。若信息不足，如实说明"信息不足"。
- user 内容：
  > {上下文片段，见下方拼接规则}
  >
  > 问题：{question}
- 上下文拼接规则：
  - 单文件 ≤ `qa.maxContextChars`：全文作为一段，标注 `[文件=rel_path, 段落=全文]`。
  - 超限 / 全局：top-K 块按（file, seq）升序拼接，每块前缀 `[文件=rel_path, 段落=seq]`；块间空行分隔。
- 引用溯源：回答后记录 `sources = [{file, seq}]`，仅当模型输出中标注了 `[文件=...]` 才映射，未标注不臆造。

---

## 附录 C：实现细节速查

### C.1 向量 BLOB 编码（storage.vectors）
- 格式：`float32` 数组，**小端**，连续字节，无头、无长度字段、无对齐填充。
- 写入：每个 float32 转 4 字节小端依次追加。
- 读取：按 `dim` 逐元素小端读出为 `[]float32`。
- 校验：len(bytes) == dim*4，不符视为数据损坏，重建该块向量。

### C.2 SSE 推送（transport.SSE）
- 响应头：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`Connection: keep-alive`。
- 帧格式：`data: <json>\n\n`。
- 心跳：每 15s 发送 `: ping\n\n`（注释行），保持连接不被代理断开。
- 客户端：浏览器 `EventSource('/api/events')`，断线自动重连。
- 服务端：维护连接集合；events 模块桥接推送时向所有连接写帧；写失败即移除该连接。

### C.3 OpenAI 兼容端点结构（llm 模块）
- 聊天 `POST {baseUrl}/chat/completions`：
  - 请求体字段：`model`、`messages`（`[{role:"system",content}, {role:"user",content}]`）、`temperature`、`max_tokens`（可选）。
  - 请求头：`Authorization: Bearer {apiKey}`、`Content-Type: application/json`。
  - 响应：取 `choices[0].message.content`；token 用量取 `usage`。
- 嵌入 `POST {baseUrl}/embeddings`：
  - 请求体：`model`、`input`（字符串数组，一批 ≤16）。
  - 响应：取 `data[i].embedding`（float 数组）。
- 错误映射：HTTP 429/5xx/网络错误 → 可重试；HTTP 4xx → 致命并提示"检查 API 配置"。

### C.4 分块算法（index.ProcessFile 第 3 步，按 rune 数）
1. 按行扫描，空行（仅空白）为段落边界。
2. 段落序列中，rune 数 > 4000 的段落按中文句末标点（。！？；）分句后重新组段。
3. 贪心合并：从首段开始累积，累计达 2000（±10%）成一块；将超出 2200 时开新块，新块**前缀 = 上一块末尾 256 runes 文本**（重叠）再续。
4. 每块 `token_est = 块内 rune 数`（D37 近似）。
5. 文件尾不足 200 runes 的零头并入上一块（避免碎片块）。

### C.5 文件状态机（storage.files.MarkStatus）
| 当前 | 事件 | 下一 | 触发 |
|------|------|------|------|
| pending | 任务开始 | extracting | taskqueue |
| extracting | 提取成功 | embedding | index.ProcessFile |
| extracting | 提取失败 | failed | extract |
| embedding | 向量入库成功 | indexed | index.ProcessFile |
| embedding | 重试耗尽 | failed | llm |
| 任意 | 哈希未变 | indexed | index.ProcessFile（幂等跳过） |
| 任意 | 类型不支持/用户忽略 | ignored | extract / 用户 |
| 非终态 | 应用启动 | pending | storage.RecoverPending |

> 终态：`indexed` / `failed` / `ignored`；其余为过程态。重启只恢复过程态。

### C.6 时间线分桶规则（timeline.Get）
- 天：本地日期 `yyyy-MM-dd`。
- 周：ISO 周（**周一为每周第一天**；跨年周归属 ISO 规则，用标准库/库函数判定，不手写日历）。
- 月：自然月 `yyyy-MM`。
- 提交归属：以 commit 的本地时间入桶；未跟踪文件以 mtime 入桶。
- 排序：桶按时间倒序；桶内文件按修改时间倒序。
