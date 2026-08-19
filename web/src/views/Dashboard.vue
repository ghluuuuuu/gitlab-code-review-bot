<script setup lang="ts">
import { computed } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { getJSON, type Dashboard as DashboardData, type Review, formatDate, formatTokens, reviewStateLabel, reviewStateType, stageLabel, progressPercent } from '../adminApi'

const dashboard = useQuery({ queryKey: ['admin-dashboard'], queryFn: () => getJSON<DashboardData>('/api/v1/admin/dashboard') })
const reviews = useQuery({ queryKey: ['admin-active-reviews'], queryFn: () => getJSON<{ items: Review[] }>('/api/v1/admin/reviews?scope=active&page_size=8&sort=updated_at.desc') })
const activeReviews = computed(() => reviews.data.value?.items ?? [])
const cards = computed(() => {
  const value = dashboard.data.value
  return [
    { label: '待审任务', value: value?.queued ?? 0, hint: '等待 Worker', color: '' },
    { label: '审查中', value: value?.running ?? 0, hint: '正在执行', color: 'blue' },
    { label: '等待重试', value: value?.retry_wait ?? 0, hint: '保留 Session', color: 'orange' },
    { label: '发布中', value: value?.publishing ?? 0, hint: 'GitLab 发布', color: 'purple' },
    { label: '已通过', value: value?.passed ?? 0, hint: '累计完成', color: 'green' },
    { label: '未通过', value: value?.failed ?? 0, hint: '包含基础设施失败', color: 'red' },
    { label: '已过期', value: value?.stale ?? 0, hint: '被新版本替代', color: 'gray' },
    { label: '今日 Token', value: formatTokens(value?.today_tokens ?? 0), hint: `本月 ${formatTokens(value?.month_tokens ?? 0)}`, color: 'teal' },
  ]
})
</script>

<template>
  <div class="page-heading">
    <div>
      <h1>运行总览</h1>
      <p>从任务、阶段、覆盖率和运行成本观察代码审查 Bot</p>
    </div>
  </div>
  <div class="summary-grid">
    <div v-for="card in cards" :key="card.label" class="summary-card"><span class="summary-label">{{ card.label
        }}</span><strong :class="card.color">{{ card.value }}</strong><span class="summary-hint">{{ card.hint }}</span>
    </div>
  </div>
  <div class="dashboard-grid">
    <el-card shadow="never" class="active-card">
      <template #header>
        <div class="card-header"><strong>活动审查任务</strong>
          <RouterLink to="/reviews">查看全部 →</RouterLink>
        </div>
      </template>
      <el-table :data="activeReviews" v-loading="reviews.isLoading.value" stripe>
        <el-table-column label="任务" min-width="245"><template #default="scope">
            <div class="review-title"><b>!{{ scope.row.mr_iid }}</b><span>{{ scope.row.title || '未命名审查' }}</span></div>
            <small>{{ scope.row.project_path || `项目 ${scope.row.project_id}` }} · {{ scope.row.head_sha.slice(0, 10)
              }}</small>
          </template></el-table-column>
        <el-table-column label="阶段" width="150"><template #default="scope"><el-tag
              :type="reviewStateType(scope.row.state)" effect="light">{{ reviewStateLabel(scope.row.state)
              }}</el-tag><small class="stage">{{ stageLabel(scope.row.stage) }}</small></template></el-table-column>
        <el-table-column label="进度" width="160"><template #default="scope"><el-progress
              :percentage="progressPercent(scope.row)" :stroke-width="7" :show-text="false" /><small>{{
                scope.row.progress_completed }} / {{ scope.row.progress_total || '?' }}
              个文件</small></template></el-table-column>
        <el-table-column label="Token" width="90"><template #default="scope">{{ formatTokens(scope.row.total_tokens)
            }}</template></el-table-column>
        <el-table-column label="更新时间" width="145"><template #default="scope">{{ formatDate(scope.row.finished_at ||
          scope.row.started_at || scope.row.queued_at) }}</template></el-table-column>
        <el-table-column label="操作" width="78"><template #default="scope">
            <RouterLink :to="`/reviews/${scope.row.id}`">详情</RouterLink>
          </template></el-table-column>
      </el-table>
      <el-empty v-if="!reviews.isLoading.value && !activeReviews.length" description="当前没有活动任务" />
    </el-card>
    <el-card shadow="never" class="health-card">
      <template #header><strong>运行健康</strong></template>
      <div class="health-item"><span class="health-dot good" />数据库 <b>就绪</b></div>
      <div class="health-item"><span class="health-dot good" />GitLab <b>已配置</b></div>
      <div class="health-item"><span class="health-dot" :class="dashboard.isError.value ? 'bad' : 'good'" />任务统计 <b>{{
        dashboard.isError.value ? '读取失败' : '正常' }}</b></div>
      <div class="health-item"><span class="health-dot good" />刷新时间 <b>{{ formatDate(new Date().toISOString()) }}</b>
      </div>
      <div class="health-note">详细依赖状态可在系统状态页面查看。敏感凭据不会通过后台接口返回。</div>
    </el-card>
  </div>
</template>

<style scoped>
.page-heading {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 22px;
}

.page-heading h1 {
  margin: 0 0 7px;
  font-size: 25px;
}

.page-heading p {
  margin: 0;
  color: #9499ad;
  font-size: 13px;
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
  margin-bottom: 18px;
}

.summary-card {
  min-height: 112px;
  padding: 18px 20px;
  border: 1px solid #ebedf4;
  border-radius: 12px;
  background: #fff;
  box-shadow: 0 3px 12px #30345b08;
}

.summary-label {
  display: block;
  color: #8c91a5;
  font-size: 12px;
}

.summary-card strong {
  display: block;
  margin: 8px 0 3px;
  color: #30364e;
  font-size: 26px;
}

.summary-hint {
  color: #b1b5c4;
  font-size: 11px;
}

.blue {
  color: #3984e8 !important;
}

.orange {
  color: #e69a42 !important;
}

.purple {
  color: #7469df !important;
}

.green {
  color: #22b881 !important;
}

.red {
  color: #ed6870 !important;
}

.gray {
  color: #81899f !important;
}

.teal {
  color: #2ca89b !important;
}

.dashboard-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 290px;
  gap: 18px;
}

.active-card,
.health-card {
  border: 1px solid #ebedf4;
  border-radius: 12px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.card-header a {
  color: #6258d8;
  font-size: 12px;
  text-decoration: none;
}

.review-title {
  display: flex;
  gap: 7px;
}

.review-title b {
  color: #6258d8;
}

.review-title span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.review-title+small,
.stage,
.health-note {
  display: block;
  color: #a0a5b6;
  font-size: 11px;
}

.stage {
  margin-top: 5px;
}

.health-item {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 13px 0;
  border-bottom: 1px solid #f1f2f6;
  color: #656b7f;
  font-size: 13px;
}

.health-item b {
  margin-left: auto;
  color: #353b54;
  font-size: 12px;
}

.health-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #aab0c0;
}

.health-dot.good {
  background: #2fbe86;
  box-shadow: 0 0 0 4px #2fbe8618;
}

.health-dot.bad {
  background: #e76572;
  box-shadow: 0 0 0 4px #e7657218;
}

.health-note {
  margin-top: 16px;
  line-height: 1.6;
}

.active-card :deep(.el-table__header th) {
  color: #8f94a7;
  font-size: 12px;
  font-weight: 500;
}

.active-card :deep(.el-progress) {
  margin: 5px 0 4px;
}

.active-card :deep(.el-progress-bar__outer) {
  background: #eceef5;
}

@media (max-width:1050px) {
  .summary-grid {
    grid-template-columns: repeat(2, 1fr);
  }

  .dashboard-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width:600px) {
  .summary-grid {
    grid-template-columns: 1fr 1fr;
  }

  .page-heading {
    gap: 10px;
  }

  .page-heading h1 {
    font-size: 21px;
  }
}
</style>
