import { createRouter, createWebHistory } from 'vue-router'
const Dashboard = () => import('./views/Dashboard.vue')
const ReviewList = () => import('./views/ReviewList.vue')
const ReviewDetail = () => import('./views/ReviewDetail.vue')
const TokenUsage = () => import('./views/TokenUsage.vue')
const QualityTrend = () => import('./views/QualityTrend.vue')
const SystemStatus = () => import('./views/SystemStatus.vue')

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/dashboard' },
    { path: '/dashboard', name: 'dashboard', component: Dashboard },
    { path: '/reviews', name: 'reviews', component: ReviewList },
    { path: '/reviews/:id', name: 'review-detail', component: ReviewDetail },
    { path: '/quality', name: 'quality', component: QualityTrend },
    { path: '/usage', name: 'usage', component: TokenUsage },
    { path: '/system', name: 'system', component: SystemStatus },
  ],
})

export default router
