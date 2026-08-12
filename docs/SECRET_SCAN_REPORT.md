# 密钥与敏感文件泄露扫描记录

> 审计项：P0-01 —— 强制 `.memora/` ignore、提交排除，并检查工作树 / Git index / 可用历史。
> 本文件是扫描**记录**，不是整改计划。整改计划见 `SYSTEM_REMEDIATION_PLAN.md`。

## 1. 扫描工具

新增：

- `backend/internal/git/git.go`：
  - `ScanForMemoraLeaks(path) ([]string, error)` —— 扫描工作树与 Git index 中已 tracked 的 `.memora` 路径。
  - `ScanHistoryForMemoraLeaks(path) ([]string, error)` —— 遍历全部提交历史（`LogOptions{All:true}`），返回曾进入任意提交树的 `.memora` 路径。
  - 两者只记录**路径**，绝不读取/打印文件内容。
- `backend/cmd/review_scan/main.go` —— 命令行工具：`go run ./cmd/review_scan <repo-path>` 同时输出工作树/index 与历史扫描结果。
- 行为保障：`EnsureRepo` 对所有仓库（含复用已有仓库的 PlainOpen 分支）强制写入 `.gitignore` 的 `.memora/` 规则；`CommitAuto`/`CommitManual`/初始提交任一 `add` 失败即中止（P1-04），避免不完整或越权提交。

## 2. 扫描结果

扫描对象：本仓库 `C:\Users\PC\Desktop\projects\Memora`，基线 commit `c9fe01d` 之后。

| 范围 | 结果 |
|---|---|
| 工作树 / Git index | 无泄露 |
| Git 可用历史 | 无泄露 |

**结论：当前仓库未发现 `.memora` 敏感文件被 tracked 或进入历史；无需执行 Key 轮换。**

## 3. 处置建议（若未来发现泄露）

- 立即停用对应 API Key 并在服务商侧轮换（本工具不自动轮换，属人工确认操作）。
- 从 index 移除：`git rm -r --cached .memora && git commit`。
- 若已推送，历史清理需逐仓库重写（filter-repo）并确认无其他克隆后强制推送，作为单独、需确认的操作。

## 4. 验证

- `go test -count=1 ./internal/git/...`：通过（含工作树扫描、index 扫描、历史扫描、已有仓库 ignore 补写用例）。
- `go run ./cmd/review_scan <repo>`：工作树/index 与历史均输出 `(none)`。
