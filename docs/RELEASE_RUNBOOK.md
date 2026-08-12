# Memora 发布手册（Release Runbook）

> 适用范围：Windows 本地单进程版本（Go 后端内嵌前端，`bin\memora.exe`）。
> 相关文档：根 `README.md`、`docs/SYSTEM_REMEDIATION_PLAN.md` Phase 6。

## 1. 发布前置条件

- 当前代码已通过根级门禁：在项目根目录运行 `verify.bat`，四项检查**全部通过**（前端类型检查+构建、`go vet`、`go test -count=1 ./...`、`gofmt -l` 漂移为空）。
- 对应 Phase 的验收标准已满足（参考整改计划对应 Phase 的「验收」小节）。
- 变更日志（CHANGELOG）已更新到本次版本号。

## 2. 发布流程

```bat
REM 1) 确认工作树干净，feature/bugfix 已合并到目标分支
git status
git log --oneline -10

REM 2) 打版本标签（语义化版本，tag 名 vX.Y.Z）
git tag vX.Y.Z

REM 3) 执行发布脚本（tag 驱动；产物输出目录以脚本输出为准，默认 bin\ 或 dist\）
release.bat vX.Y.Z
```

`release.bat` 见 **Phase 6 交付**：由 tag 驱动产出 Windows 制品，并生成 SHA-256 校验和、SBOM 与变更日志。

### 产物清单

| 产物 | 说明 |
|---|---|
| `memora.exe` | 自包含可执行文件（前端已内嵌，无需额外静态资源） |
| `*.sha256` | 各制品的 SHA-256 校验和 |
| `*sbom*` | 软件物料清单（后端 Go 依赖 + 前端 npm 依赖） |
| `CHANGELOG` / `CHANGELOG.md` | 变更日志 |
| `VERSION` | 版本号文件（与 tag 一致） |

## 3. 升级前备份（必做）

升级前**必须**备份所有用户工作区的 `.memora/` 目录：

```
<工作区>\.memora\
  config.json    用户配置（含经 DPAPI 加密的 API Key 引用）
  meta.db        SQLite 数据库（索引、问答、统计等）
  text_cache\    抽取文本缓存
  meta.db.bak-*  数据库迁移自动备份（如已产生）
```

备份方式：

```bat
REM 先停止 memora 进程，再整体复制
xcopy /E /I /H /Y "<工作区>\.memora" "<备份目录>\memora_backup_<日期>"
```

> 提示：请确认所有使用过的工作区都已覆盖；API Key 虽经 DPAPI 加密存储，备份目录仍应妥善保管（含本地加密密钥上下文）。

## 4. 升级步骤

1. 停止旧进程（托盘/控制台退出，或任务管理器结束 `memora.exe`）。
2. 校验新制品校验和：

   ```bat
   REM 输出应与 .sha256 文件内容一致
   certutil -hashfile memora.exe SHA256
   ```

3. 用新 `memora.exe` 覆盖旧文件（保持可执行文件所在目录不变，避免配置定位变化）。
4. 启动新进程。
5. 确认就绪：

   ```bat
   REM liveness：进程活着即 200
   curl http://127.0.0.1:19000/health

   REM readiness：200 {status:"ready"}；503 时查看 reasons 字段定位原因
   curl http://127.0.0.1:19000/ready
   ```

6. 按第 7 节「冒烟清单」做一轮冒烟，确认正常后即升级完成。

## 5. 回滚

### 5.1 常规回滚（升级后功能异常）

1. 停止进程。
2. 恢复备份的 `.memora/` 到原工作区（覆盖被新版改动过的配置/数据库）。
3. 换回旧版 `memora.exe`。
4. 启动并验证 `/health`、`/ready` 与冒烟清单。

### 5.2 数据库迁移失败回滚

storage 模块在 schema 迁移前会先执行 WAL checkpoint 并把 `meta.db` 复制为
`meta.db.bak-<毫秒时间戳>`（同目录），迁移在单事务内执行，失败自动回滚且原库保持不变
（实现见 `backend/internal/storage/migrations.go`）。

处理流程：

1. 首次启动新版若报迁移失败：先查看日志中的 `errorCode` 与 `err`（含失败版本号与 SQL）。
2. 不要反复启动新版重试迁移（可能触发多次备份）；先停止进程。
3. 检查 `meta.db` 与同目录 `meta.db.bak-*`：

   - 若 `meta.db` 的 `PRAGMA user_version` 仍为旧版本（迁移未提交）→ 数据库本身未被改动，可直接回滚到旧 exe，或修复迁移后重试。
   - 若 `meta.db` 已损坏/部分写入 → 用最近的 `meta.db.bak-*` 覆盖恢复。
4. 恢复后启动旧版 exe 验证 `/ready` 与数据完整性（索引、问答记录、统计可正常读取）。

> 若新版本对 schema 有破坏性变更且无法回退，按「5.1 常规回滚」整体恢复旧 exe + 备份 `.memora/`。

## 6. 签名策略

- **当前状态：制品未签名**（未接入代码签名证书）。
- 可选方案：使用 `signtool sign` 对 `memora.exe` 做 Authenticode 签名，并配合 `signtool verify /pa /v` 验证。
- 决策记录：是否引入签名、证书来源（代码签名证书 / EV 证书）与轮换策略，由发布负责人决定并在 CHANGELOG 中注明。

## 7. 冒烟清单

以下场景每次发布（尤其跨 Phase）都应人工或脚本走一遍：

| # | 场景 | 操作与预期 |
|---|---|---|
| 1 | 首次启动 | 全新环境启动 exe → 浏览器自动打开首页，`/health` 200 |
| 2 | 升级旧库 | 用含旧版本 `meta.db`/`config.json` 的工作区启动 → 迁移成功，`/ready` ready，无数据丢失 |
| 3 | 初始化 | 选择工作区 → 建立索引，进度正常，文件进入「可搜索」状态 |
| 4 | 索引 | 增/改/删文件 → 状态与搜索结果正确更新；全量重建可用 |
| 5 | 搜索 | 文件名与内容搜索均返回预期结果 |
| 6 | 问答 | 全局与单文件问答可流式返回，引用来源正确，无「发送中」卡死 |
| 7 | 提交 | 自动/手动提交成功，版本记录可见，差异正确 |
| 8 | 恢复 | 从历史版本恢复文件，未跟踪文件冲突被正确拦截/提示 |
| 9 | 重启 | 重启进程后配置、索引、问答会话与统计保持一致 |
| 10 | 回滚演练 | 按第 5 节流程演练一次备份恢复，确认可回到旧版本 |

## 8. 故障排查入口

- 错误码与用户建议：见 `docs/ERROR_CODES.md`。
- 诊断端点：`GET /health`、`GET /ready`、`GET /diagnostics`（版本、generation、队列深度、storage、缓存、uptime）。
- 日志：默认 JSON Lines（控制台调试可用环境变量 `MEMORA_LOG_FORMAT=console` 切换人类可读格式）；错误级写 stderr。用界面 `requestId` 在日志中按 `requestId`/`operationId` 关联整条操作链。
