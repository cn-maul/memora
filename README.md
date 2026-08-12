# Memora（智能文档库）

> Windows 本地单进程智能文档库：Go 后端 + Vue 3 前端，RAG 问答。

Memora 是一个面向 Windows 的本地单进程文档知识库：在用户选定的工作区目录内建立索引，提供文档语义搜索、全局/单文件 RAG 问答、Git 版本控制、统计与设置。数据（配置、数据库、缓存）均保存在工作区的 `.memora/` 目录中，不上传业务文档（问答内容仅流向用户自行配置的模型端点）。

---

**📖 项目详情请见 [docs/PROJECT_GUIDE.md](docs/PROJECT_GUIDE.md)**，包含：

- [功能清单](docs/PROJECT_GUIDE.md#21-功能清单) —— 索引 / 搜索 / 问答 / 版本控制 / 统计 / 设置
- [第三方开发接口](docs/API_REFERENCE.md) —— 全部 REST / SSE 端点、参数、响应 DTO、错误码
- [快速开始](docs/PROJECT_GUIDE.md#2-快速开始) —— 构建、运行、开发模式
- [架构概览](docs/PROJECT_GUIDE.md#3-架构概览) 与 [目录结构](docs/PROJECT_GUIDE.md#4-目录结构)
- [开发与验证](docs/PROJECT_GUIDE.md#5-开发与验证) —— verify.bat 门禁、前端/后端检查命令
- [发布](docs/PROJECT_GUIDE.md#6-发布) —— release.bat、升级、回滚
- [数据与隐私](docs/PROJECT_GUIDE.md#7-数据与隐私) —— DPAPI、文本缓存配额、日志脱敏
- [故障排查](docs/PROJECT_GUIDE.md#8-故障排查) —— 错误码、requestId、诊断端点
- [备份与恢复](docs/PROJECT_GUIDE.md#9-备份与恢复)
- [文档索引](docs/PROJECT_GUIDE.md#10-文档索引)
