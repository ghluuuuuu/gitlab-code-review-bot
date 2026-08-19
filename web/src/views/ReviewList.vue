<script setup lang="ts">
import { computed, reactive } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { useRouter } from 'vue-router'
import { getJSON, type ReviewPage, type Review, formatDate, formatTokens, progressPercent, reviewStateLabel, reviewStateType, stageLabel } from '../adminApi'

const router = useRouter()
const filters = reactive({ scope: 'active', state: '', project_id: '', mr_iid: '', page: 1, page_size: 20 })
const queryURL = computed(() => {
  const params = new URLSearchParams({ scope: filters.scope, page: String(filters.page), page_size: String(filters.page_size), sort: 'updated_at.desc' })
  if (filters.state) params.set('state', filters.state)
  if (filters.project_id) params.set('project_id', filters.project_id)
  if (filters.mr_iid) params.set('mr_iid', filters.mr_iid)
  return `/api/v1/admin/reviews?${params}`
})
const reviews = useQuery({ queryKey: ['admin-reviews', queryURL], queryFn: () => getJSON<ReviewPage>(queryURL.value) })
const rows = computed(() => reviews.data.value?.items ?? [])
const openRow = (row: Review) => router.push(`/reviews/${row.id}`)
const resetPage = () => { filters.page = 1 }
const applyFilters = () => { filters.page = 1; reviews.refetch() }
const changePage = (page: number) => { filters.page = page }
const clearFilters = () => { filters.state = ''; filters.project_id = ''; filters.mr_iid = ''; resetPage() }
</script>

<template>
  <div class="page-heading">
    <div>
      <h1>审查任务</h1>
      <p>以任务版本为中心查看排队、执行、发布和历史审查</p>
    </div>
    <div class="heading-actions"><el-button tag="a" href="/api/v1/admin/reviews/export?scope=history">导出历史
        CSV</el-button></div>
  </div>
  <el-card shadow="never" class="filter-card">
    <div class="filters">
      <el-radio-group v-model="filters.scope" @change="resetPage"><el-radio-button
          label="active">活动任务</el-radio-button><el-radio-button label="history">历史任务</el-radio-button><el-radio-button
          label="all">全部</el-radio-button></el-radio-group>
      <el-select v-model="filters.state" clearable placeholder="状态" class="filter-control"
        @change="resetPage"><el-option label="排队中" value="queued" /><el-option label="审查中" value="running" /><el-option
          label="等待重试" value="retry_wait" /><el-option label="发布中" value="publishing" /><el-option label="通过"
          value="completed_pass" /><el-option label="未通过" value="completed_fail" /><el-option label="基础设施失败"
          value="failed_infra" /><el-option label="已过期" value="stale" /><el-option label="已取消"
          value="canceled" /></el-select>
      <el-input v-model="filters.project_id" placeholder="项目 ID" clearable class="filter-control short"
        @keyup.enter="applyFilters" />
      <el-input v-model="filters.mr_iid" placeholder="MR IID" clearable class="filter-control short"
        @keyup.enter="applyFilters" />
      <el-button type="primary" @click="applyFilters">查询</el-button><el-button @click="clearFilters">清空</el-button>
    </div>
  </el-card>
  <el-card shadow="never" class="table-card">
    <template #header>
      <div class="table-header"><strong>{{ filters.scope === 'history' ? '历史审查' : filters.scope === 'all' ? '全部审查' :
          '活动任务' }}</strong><span>共 {{ reviews.data.value?.total ?? 0 }} 条</span></div>
    </template>
    <el-table :data="rows" v-loading="reviews.isLoading.value" stripe row-key="id" @row-click="openRow">
      <el-table-column label="项目 / Merge Request" min-width="255"><template #default="scope">
          <div class="mr-title"><b>!{{ scope.row.mr_iid }}</b><span>{{ scope.row.title || '未命名审查' }}</span></div>
          <small>{{ scope.row.project_path || `项目 ${scope.row.project_id}` }} · {{ scope.row.head_sha.slice(0, 10)
            }}</small>
        </template></el-table-column>
      <el-table-column label="分支关系" min-width="155"><template #default="scope">
          <div class="branch-flow">
            <code>{{ scope.row.source_branch || '未知' }}</code><span>→</span><code>{{ scope.row.target_branch || '未知' }}</code>
          </div><small>Target {{ scope.row.target_sha.slice(0, 10) }}</small>
        </template></el-table-column>
      <el-table-column label="状态 / 阶段" width="150"><template #default="scope"><el-tag
            :type="reviewStateType(scope.row.state)" effect="light">{{ reviewStateLabel(scope.row.state)
            }}</el-tag><small class="stage">{{ stageLabel(scope.row.stage) }}</small></template></el-table-column>
      <el-table-column label="进度" width="150"><template #default="scope"><el-progress
            :percentage="progressPercent(scope.row)" :stroke-width="7" :show-text="false"
            :status="scope.row.state === 'completed_pass' ? 'success' : scope.row.state === 'completed_fail' || scope.row.state === 'failed_infra' ? 'exception' : undefined" /><small>{{
              scope.row.progress_completed }} / {{ scope.row.progress_total || '?' }}
            文件</small></template></el-table-column>
      <el-table-column label="缺陷" width="82"><template #default="scope"><span
            :class="scope.row.findings.blocking ? 'blocking' : ''">{{ scope.row.findings.total }}</span><small
            v-if="scope.row.findings.blocking">阻断 {{ scope.row.findings.blocking }}</small></template></el-table-column>
      <el-table-column label="Token" width="82"><template #default="scope">{{ formatTokens(scope.row.total_tokens)
          }}</template></el-table-column>
      <el-table-column label="更新时间" width="145"><template #default="scope">{{ formatDate(scope.row.finished_at ||
        scope.row.started_at || scope.row.queued_at) }}</template></el-table-column>
      <el-table-column label="操作" width="80"><template #default="scope"><el-button type="primary" link
            @click.stop="router.push(`/reviews/${scope.row.id}`)">详情</el-button></template></el-table-column>
    </el-table>
    <el-empty v-if="!reviews.isLoading.value && !rows.length" description="没有符合条件的审查任务" />
    <div class="pagination"><el-pagination v-model:current-page="filters.page" :page-size="filters.page_size"
        :total="reviews.data.value?.total ?? 0" layout="prev, pager, next" @current-change="changePage" /></div>
  </el-card>
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

.filter-card,
.table-card {
  margin-bottom: 18px;
  border: 1px solid #ebedf4;
  border-radius: 12px;
}

.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
}

.filter-control {
  width: 150px;
}

.filter-control.short {
  width: 110px;
}

.table-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.table-header span,
.mr-title+small,
.branch-flow+small,
.stage,
.pagination {
  color: #a0a5b6;
  font-size: 11px;
}

.mr-title {
  display: flex;
  gap: 7px;
}

.mr-title b {
  color: #6258d8;
}

.mr-title span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.branch-flow {
  display: flex;
  align-items: center;
  gap: 7px;
}

.branch-flow code {
  max-width: 70px;
  overflow: hidden;
  color: #6258d8;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.branch-flow span {
  color: #a3a7b8;
}

.stage {
  display: block;
  margin-top: 5px;
}

.blocking {
  color: #df5967;
  font-weight: 700;
}

.el-table :deep(.el-table__row) {
  cursor: pointer;
}

.el-table :deep(.el-progress) {
  margin: 5px 0 4px;
}

.el-table :deep(.el-progress-bar__outer) {
  background: #eceef5;
}

.pagination {
  display: flex;
  justify-content: flex-end;
  padding-top: 18px;
}

@media (max-width:800px) {
  .page-heading {
    gap: 10px;
  }

  .page-heading h1 {
    font-size: 21px;
  }

  .filter-control {
    width: 130px;
  }
}
</style>
