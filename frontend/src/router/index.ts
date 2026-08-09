import { createRouter, createWebHashHistory } from 'vue-router'
import AllFilesPage from '@/views/AllFilesPage.vue'
import WorkspacePage from '@/views/WorkspacePage.vue'
import IndexPage from '@/views/IndexPage.vue'
import TimelinePage from '@/views/TimelinePage.vue'
import QAPage from '@/views/QAPage.vue'
import StatsPage from '@/views/StatsPage.vue'
import SettingsPage from '@/views/SettingsPage.vue'

const routes = [
  { path: '/', redirect: '/files' },
  { path: '/files', name: 'files', component: AllFilesPage },
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