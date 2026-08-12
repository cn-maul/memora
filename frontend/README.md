# Memora 前端（frontend）

Vue 3 + TypeScript + Vite 单页应用，提供文件索引、语义搜索、标签、问答、统计、提交记录等界面。仅中文界面。

## 技术栈

- Vue 3（`<script setup>`）+ TypeScript
- Vite 构建
- Pinia（状态管理）+ Vue Router（路由）
- axios（REST 调用）+ 原生 SSE（服务端推送）

## 开发

```bash
npm install
npm run dev      # Vite 开发服务器（默认 5173 端口，需配合后端 API）
```

前端通过 `/api` 相对路径访问后端，开发时由 Vite 代理或直接同源部署。

## 构建

```bash
npm run build    # vue-tsc 类型检查 + Vite 生产构建，产物输出到 dist/
```

`backend/build.bat` 会在后端构建前把 `frontend/dist` 复制到
`backend/internal/web/dist`（go:embed 内嵌到 exe），因此发布前需先执行本目录构建。

## 目录结构

```
src/
  api/client.ts      # REST/SSE API 封装
  components/        # Icon、TreeBranch（目录树）等通用组件
  stores/            # Pinia：workspace/files/tags/qa/settings
  views/             # 页面：AllFiles/Workspace/Index/Timeline/QA/Stats/Settings
  types/index.ts     # 前后端契约类型
  style.css          # 全局设计令牌（颜色/圆角/阴影/明暗主题）
```

## 与后端契约

所有接口字段、错误码以 `backend/internal/contract` 与项目 `docs/API_REFERENCE.md` 为准，
修改后端接口时请同步更新 `src/types/index.ts`。
