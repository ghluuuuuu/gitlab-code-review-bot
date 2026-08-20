import { createRouter, createWebHistory } from 'vue-router'
import { authState, initializeAuth } from './auth'
const Dashboard = () => import('./views/Dashboard.vue')
const ReviewList = () => import('./views/ReviewList.vue')
const ReviewDetail = () => import('./views/ReviewDetail.vue')
const TokenUsage = () => import('./views/TokenUsage.vue')
const QualityTrend = () => import('./views/QualityTrend.vue')
const SystemStatus = () => import('./views/SystemStatus.vue')
const Login = () => import('./views/Login.vue')
const UserManagement = () => import('./views/UserManagement.vue')
const SystemConfig = () => import('./views/SystemConfig.vue')
const MCPConfig = () => import('./views/MCPConfig.vue')

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/dashboard' },
    { path: '/dashboard', name: 'dashboard', component: Dashboard },
    { path: '/reviews', name: 'reviews', component: ReviewList },
    { path: '/reviews/:id', name: 'review-detail', component: ReviewDetail },
    { path: '/quality', name: 'quality', component: QualityTrend },
    { path: '/login', name: 'login', component: Login },
    { path: '/usage', name: 'usage', component: TokenUsage, meta: { superadmin: true } },
    { path: '/system', name: 'system', component: SystemStatus, meta: { superadmin: true } },
    { path: '/users', name: 'users', component: UserManagement, meta: { superadmin: true } },
    { path: '/config', name: 'config', component: SystemConfig, meta: { superadmin: true } },
    { path: '/mcp-config', name: 'mcp-config', component: MCPConfig },
  ],
})


router.beforeEach(async to => {
  await initializeAuth()
  if (!authState.config.enabled) return to.name === 'login' ? { name: 'dashboard' } : true
  if (!authState.user && to.name !== 'login') return { name: 'login', query: { redirect: to.fullPath } }
  if (authState.user && to.name === 'login') return { name: 'dashboard' }
  if (to.meta.superadmin && authState.user?.role !== 'superadmin') return { name: 'dashboard' }
  return true
})
export default router
