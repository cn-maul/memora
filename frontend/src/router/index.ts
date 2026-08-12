import { createRouter, createWebHashHistory } from 'vue-router'

// 路由懒加载（P2-18）：页面按需加载，缩小首屏 bundle
const RecentFilesPage = () => import('@/views/RecentFilesPage.vue')
const WorkspacePage = () => import('@/views/WorkspacePage.vue')
const IndexPage = () => import('@/views/IndexPage.vue')
const TimelinePage = () => import('@/views/TimelinePage.vue')
const QAPage = () => import('@/views/QAPage.vue')
const StatsPage = () => import('@/views/StatsPage.vue')
const SettingsPage = () => import('@/views/SettingsPage.vue')

const routes = [
  { path: '/', redirect: '/files' },
  { path: '/files', name: 'files', component: RecentFilesPage },
  { path: '/workspace', name: 'workspace', component: WorkspacePage },
  { path: '/index', name: 'index', component: IndexPage },
  { path: '/timeline', name: 'timeline', component: TimelinePage },
  { path: '/qa', name: 'qa', component: QAPage },
  { path: '/stats', name: 'stats', component: StatsPage },
  { path: '/settings', name: 'settings', component: SettingsPage },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

export default router