# Memora 性能基准（Phase 5：性能和可观测性）

本目录是 `SYSTEM_REMEDIATION_PLAN.md` Phase 5 的基准基础设施，用于建立
**500 / 5000 文件、10k / 50k chunks** 基准数据以及 **p50 / p95** 记录。

- `gen_data.go` — 基准数据生成器（独立可运行，仅依赖 Go 标准库）。
- `bench_vector_search_test.go` — 本地向量候选检索测量（全量排序 vs 有界小顶堆 topK）。
- `README.md` — 基准机信息、工具链、数据规模、测量方法、目标阈值与结果记录。

> 本目录是独立 Go module（`memora/benchmarks`），不依赖 `backend/` 生产代码；
> 基准只做**候选检索**层面的测量，不要求跑通整系统。

---

## 1. 基准机信息

| 项 | 值 |
|---|---|
| CPU | AMD Ryzen 7 4700U with Radeon Graphics，8 核心 / 8 线程 |
| 内存 | 16 GB（16505954304 bytes ≈ 15.4 GiB） |
| 磁盘 | Samsung MZVLB512HBJQ（NVMe SSD，512 GB），类型：SSD |
| 操作系统 | Windows 10（10.0.19045，win32） |
| 备注 | 基准在空闲状态、接电源、无后台大负载时执行 |

> 占位说明：更换基准机后请更新本表，并在结果表中标注机器与日期。

## 2. 工具链版本

| 组件 | 版本 | 说明 |
|---|---|---|
| Go | 1.26.5（`backend/go.mod` 目标为 go 1.22） | `go version go1.26.5 windows/amd64` |
| Node | v26.4.0 | 前端工具链 |
| 前端框架 | Vue 3 / Vite（npm 依赖见 `frontend/package.json`） | 仅记录，候选检索基准不涉及 |
| 向量维度 | **1024** | `embed.dimensions` 默认 1024（`backend/internal/config/config.go`），本基准默认 `-bench.dim 1024` |
| 向量计算 | float32 余弦相似度 | 基准文件内的拷贝版实现，不 import 生产包 |

## 3. 数据生成（`gen_data.go`）

- **seed**：默认 `42`，可用 `-seed` 覆盖。`math/rand.New(rand.NewSource(seed))` 保证可复现。
- **文件数规模**：`-files 500` / `-files 5000`。
- **chunks 规模**：以 Markdown 内容块（标题/段落/列表/代码块）近似 chunk。
  每文件块数由 `-paras` 控制（默认 20）：

| 基准规模 | 参数 | 约 chunks |
|---|---|---|
| 500 文件 / 10k chunks | `go run gen_data.go -files 500 -paras 20` | 500 × 20 ≈ 10k |
| 5000 文件 / 50k chunks | `go run gen_data.go -files 5000 -paras 10` | 5000 × 10 ≈ 50k |

生成命令示例（在 `benchmarks/` 下）：

```bash
go run gen_data.go -files 500  -paras 20 -dir ./benchdata -seed 42
go run gen_data.go -files 5000 -paras 10 -dir ./benchdata -seed 42
```

输出结构：`<dir>/` 下 `doc_000000.md` … `doc_NNNNNN.md`（Markdown，随机英文/中文内容，
可含 `#` 标题、`-` 列表、` ``` ` 代码块）。

## 4. 测量流程

- **预热次数**：`5` 次（`-bench.warmup 5`），不计入统计。
- **正式运行次数**：**不少于 30 次**（默认 `-bench.runs 30`，可上调）。
- **p50/p95 换算**：对正式运行的每次耗时排序，用 **nearest-rank** 方法：
  `index = ceil(p × n) - 1`（越界时取 0 或 n-1）。p50 即中位数，p95 即第 95 百分位。
- 运行方式：
  ```bash
  # 自定义统计（p50/p95，含预热与 30 次正式运行）
  go test -run 'TestBench.*P50P95' -v -count=1 .

  # testing.B 吞吐/分配基线（ns/op、allocs/op）
  go test -bench 'BenchmarkCandidate' -benchmem -count=1 .

  # 调整维度/规模/topK
  go test . -args -bench.dim 1024 -bench.vectors 50000 -bench.topk 100
  ```

### 冷缓存 / 热缓存条件

- **冷缓存**：进程重启后首轮查询，索引/向量集从磁盘加载，测量包含索引加载过程。
  在全新进程上执行第一轮测量并记录为冷缓存数据。
- **热缓存**：索引常驻内存，连续多轮查询，测量聚焦纯候选检索路径。
  各测试函数内部先完成预热再采样，即默认处于热缓存条件；做冷缓存记录时，
  应重启进程后立刻执行第一轮，并在结果表中标注「冷 / 热」。

## 5. 当前目标阈值（抄录自 `SYSTEM_REMEDIATION_PLAN.md` Phase 5，基线固化前为候选目标）

- 5k 文件 / 50k chunks 本地向量候选检索 **p95 < 300ms**，不含远端 rerank。
- search/QA 每次候选元数据 **SQL 查询数为 O(1)**，不随 topK 线性增长。
- 空闲状态**不进行 8 秒递归全盘扫描**；SSE 正常时**无 5 秒 queue 轮询**。
- （前端）全量索引期间同类刷新请求按秒级合并，不随 progress 事件数线性增长。

> 基线测量后在同一 PR 内固化阈值，届时更新本表为「已固化」并记录基线 commit。

## 6. 结果记录表模板（p50 / p95）

| 日期 | 机器 | 规模（文件/chunks） | 缓存 | 实现 | p50 | p95 | 目标(p95) | Go/Node | commit |
|---|---|---|---|---|---|---|---|---|---|
| YYYY-MM-DD | 见 §1 | 500 / 10k | 冷 | 全量排序 | _ | _ | — | Go 1.26.5 / Node 26 | `xxxxxxx` |
| YYYY-MM-DD | 见 §1 | 500 / 10k | 冷 | 小顶堆 topK=100 | _ | _ | — | Go 1.26.5 / Node 26 | `xxxxxxx` |
| YYYY-MM-DD | 见 §1 | 500 / 10k | 热 | 全量排序 | _ | _ | — | Go 1.26.5 / Node 26 | `xxxxxxx` |
| YYYY-MM-DD | 见 §1 | 500 / 10k | 热 | 小顶堆 topK=100 | _ | _ | — | Go 1.26.5 / Node 26 | `xxxxxxx` |
| YYYY-MM-DD | 见 §1 | 5000 / 50k | 热 | 小顶堆 topK=100 | _ | _ | **< 300ms** | Go 1.26.5 / Node 26 | `xxxxxxx` |

> 填写说明：每次正式测量后把 `TestBench*P50P95` 的输出填入上表；
> 阈值列为「—」表示该规模无候选目标，仅记录基线。

## 7. 验证命令

```bash
cd benchmarks
go vet ./...
go build ./...
go test -run TestNonexistent -count=1 ./...   # 确认可编译
go run gen_data.go -files 20 -dir /tmp/memora_benchdata -paras 2 -seed 42
```
