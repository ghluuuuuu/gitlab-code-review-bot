<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { buildProjectTree, type ProjectTreeNode } from '../projectTree'

const getJSON = async <T,>(url: string): Promise<T> => {
  const response = await fetch(url)
  if (!response.ok) throw new Error(await response.text())
  return response.json()
}

type Dashboard = { queued: number; running: number; failed: number; passed: number; today_tokens: number; month_tokens: number }
type Job = {
  id: number
  project_id: number
  mr_iid: number
  title: string
  web_url: string
  head_sha: string
  state: string
  stage: string
  progress_completed: number
  progress_total: number
  queued_at: string
  finished_at: string | null
  total_tokens: number
  input_tokens: number
  output_tokens: number
  report_url: string
  source_branch: string
  target_branch: string
}
type Project = { id: number; name: string; description: string; path_with_namespace: string; web_url: string; reviews: Job[] }

const formatTokens = (value: number) => {
  const absolute = Math.abs(value)
  if (absolute < 1000) return value.toLocaleString('zh-CN')
  if (absolute < 1_000_000) return `${(value / 1_000).toFixed(absolute < 10_000 ? 1 : 0)}K`
  if (absolute < 1_000_000_000) return `${(value / 1_000_000).toFixed(absolute < 10_000_000 ? 1 : 0)}M`
  return `${(value / 1_000_000_000).toFixed(absolute < 10_000_000_000 ? 1 : 0)}B`
}


const dashboard = useQuery({ queryKey: ['dashboard'], queryFn: () => getJSON<Dashboard>('/api/v1/admin/dashboard') })
const projects = useQuery({ queryKey: ['projects'], queryFn: () => getJSON<Project[]>('/api/v1/admin/projects') })
const projectTree = computed(() => buildProjectTree(projects.data.value ?? []))
const selectedProjectId = ref<number | null>(null)
const selectedProject = computed(() => {
  const list = projects.data.value ?? []
  if (selectedProjectId.value === null) return list[0]
  return list.find(project => project.id === selectedProjectId.value) ?? list[0]
})
const selectedReviews = computed(() => selectedProject.value?.reviews ?? [])
const selectTreeNode = (node: ProjectTreeNode<Project>) => {
  if (node.project) selectedProjectId.value = node.project.id
}
const stateColor = (state: string) => ({
  completed_pass: 'success', completed_fail: 'danger', rejected_rule_missing: 'warning', rejected_rule_invalid: 'warning',
  failed_infra: 'danger', stale: 'info', running: 'primary', retry_wait: 'warning', publishing: 'primary', queued: '',
}[state] ?? '')
const stateLabel = (state: string) => ({
  completed_pass: '通过', completed_fail: '未通过', rejected_rule_missing: '规则缺失', rejected_rule_invalid: '规则无效',
  failed_infra: '基础设施失败', stale: '已过期', running: '审查中', retry_wait: '等待重试', publishing: '发布中', queued: '排队中',
}[state] ?? state)
const stageLabel = (stage: string) => ({ rule_preflight: '规则检查', git_prepare: '准备代码', code_graph: '更新代码图', ocr_review: 'OCR 审查', publishing: '发布结果', finished: '已完成', queued: '排队中' }[stage] ?? stage)
const formatDate = (value: string | null) => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—'
const progressPercent = (job: Job) => {
  if (job.progress_total > 0) return Math.min(100, Math.round(job.progress_completed / job.progress_total * 100))
  return job.state === 'completed_pass' || job.state === 'completed_fail' ? 100 : 0
}
const progressStatus = (job: Job) => job.state === 'completed_pass' ? 'success' : job.state === 'completed_fail' || job.state === 'failed_infra' ? 'exception' : undefined
</script>

<template>
  <div class="page-heading">
    <div>
      <h1>审查队列</h1>
      <p>查看 GitLab 项目及其 Merge Request 的实时排队与审查进度</p>
    </div>
  </div>
  <div class="summary-row">
    <div class="summary-card"><span class="summary-label">待审任务</span><strong>{{ dashboard.data.value?.queued ?? 0
        }}</strong><span class="summary-hint">等待处理</span></div>
    <div class="summary-card"><span class="summary-label">审查中</span><strong class="blue">{{
      dashboard.data.value?.running ?? 0 }}</strong><span class="summary-hint">正在执行</span></div>
    <div class="summary-card"><span class="summary-label">已通过</span><strong class="green">{{
      dashboard.data.value?.passed ?? 0 }}</strong><span class="summary-hint">累计完成</span></div>
  </div>
  <div class="project-layout">
    <el-card class="project-tree-card" shadow="never">
      <template #header>
        <div class="card-title"><span>GitLab 项目</span><el-badge :value="projects.data.value?.length ?? 0"
            type="primary" /></div>
      </template>
      <div v-if="projects.isLoading.value" class="empty">加载项目中…</div>
      <div v-else-if="!(projects.data.value?.length)" class="empty">暂无审查项目</div>
      <el-tree v-else class="project-tree" :data="projectTree" node-key="key" :indent="18"
        :current-node-key="selectedProject ? `project:${selectedProject.id}` : undefined" :expand-on-click-node="false"
        default-expand-all highlight-current @node-click="selectTreeNode">
        <template #default="{ data }">
          <div class="tree-node" :class="`tree-node-${data.kind}`"
            :title="data.project?.path_with_namespace || data.label">
            <span v-if="data.kind === 'group'" class="tree-folder" aria-hidden="true" />
            <span v-else class="tree-project-avatar">{{ data.label.slice(0, 1).toUpperCase() }}</span>
            <span class="tree-label">{{ data.label }}</span>
            <span class="tree-count">{{ data.kind === 'group' ? data.projectCount : data.project?.reviews.length
              }}</span>
          </div>
        </template>
      </el-tree>
    </el-card>
    <el-card class="review-list-card" shadow="never">
      <template #header>
        <div class="review-header">
          <div>
            <h2>{{ selectedProject?.name || '选择项目' }}</h2>
            <p><code v-if="selectedProject?.path_with_namespace"
                class="selected-project-path">{{ selectedProject.path_with_namespace }}</code><span
                v-if="selectedProject?.description">{{ selectedProject.path_with_namespace ? ' · ' : '' }}{{
                  selectedProject.description }}</span><span v-else-if="!selectedProject">从左侧选择一个 GitLab 项目查看审查记录</span>
            </p>
          </div><a v-if="selectedProject?.web_url" :href="selectedProject.web_url" target="_blank">打开 GitLab ↗</a>
        </div>
      </template>
      <el-table :data="selectedReviews" v-loading="projects.isLoading.value" stripe class="review-table">
        <el-table-column label="Merge Request" min-width="195"><template #default="scope"><a :href="scope.row.web_url"
              target="_blank" class="mr-link">!{{ scope.row.mr_iid }} <span>{{ scope.row.title || '未命名审查'
                }}</span></a><code>{{ scope.row.head_sha.slice(0, 10) }}</code></template></el-table-column>
        <el-table-column label="分支关系" min-width="155"><template #default="scope">
            <div class="branch-flow">
              <code>{{ scope.row.source_branch || '未知源分支' }}</code><span>→</span><code>{{ scope.row.target_branch || '未知目标分支' }}</code>
            </div>
          </template></el-table-column>
        <el-table-column label="审查进度" width="175"><template #default="scope">
            <div class="progress-cell">
              <div class="progress-head"><el-tag :type="stateColor(scope.row.state)" effect="light">{{
                stateLabel(scope.row.state) }}</el-tag><span>{{ progressPercent(scope.row) }}%</span></div>
              <el-progress :percentage="progressPercent(scope.row)" :stroke-width="7" :show-text="false"
                :status="progressStatus(scope.row)" /><small>{{ scope.row.progress_total > 0 ?
                  `${scope.row.progress_completed} / ${scope.row.progress_total} 个文件` : stageLabel(scope.row.stage)
                }}</small>
            </div>
          </template></el-table-column>
        <el-table-column label="Token" width="70"><template #default="scope">{{ formatTokens(scope.row.total_tokens ??
            0) }}</template></el-table-column>
        <el-table-column label="更新时间" width="135"><template #default="scope">{{ formatDate(scope.row.finished_at ||
          scope.row.queued_at) }}</template></el-table-column>
        <el-table-column label="操作" width="115"><template #default="scope"><el-button v-if="scope.row.report_url"
              type="primary" link size="small" tag="a" :href="scope.row.report_url" target="_blank">查看 OCR 报告
              ↗</el-button><span v-else class="no-report">暂无报告</span></template></el-table-column>
      </el-table>
      <el-empty v-if="selectedProject && !selectedReviews.length" description="该项目暂无审查队列记录" />
    </el-card>
  </div>
</template>

<style scoped>
.page-heading {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 24px;
}

.page-heading h1 {
  margin: 0 0 7px;
  font-size: 25px;
  letter-spacing: -.5px;
}

.page-heading p {
  margin: 0;
  color: #9499ad;
  font-size: 13px;
}

.summary-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
  margin-bottom: 22px;
}

.summary-card {
  min-height: 118px;
  padding: 20px 22px;
  border: 1px solid #ebedf4;
  border-radius: 12px;
  background: #fff;
  box-shadow: 0 3px 12px #30345b08;
}

.summary-label {
  display: block;
  color: #8c91a5;
  font-size: 13px;
}

.summary-card strong {
  display: block;
  margin: 8px 0 3px;
  font-size: 27px;
  color: #30364e;
}

.summary-hint {
  color: #b1b5c4;
  font-size: 11px;
}

.blue {
  color: #3984e8 !important;
}

.green {
  color: #22b881 !important;
}

.branch-flow {
  display: flex;
  align-items: center;
  gap: 8px;
}

.branch-flow code {
  max-width: 90px;
  overflow: hidden;
  color: #6258d8;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.branch-flow span {
  color: #a3a7b8;
}

.project-layout {
  display: grid;
  grid-template-columns: 310px minmax(0, 1fr);
  gap: 18px;
  align-items: start;
}

.project-tree-card,
.review-list-card {
  border: 1px solid #ebedf4;
  border-radius: 12px;
}

.project-tree-card :deep(.el-card__header),
.review-list-card :deep(.el-card__header) {
  border-bottom: 1px solid #f0f1f6;
}

.card-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 14px;
}

.project-node {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 10px;
  margin: 3px -8px;
  border-radius: 9px;
  cursor: pointer;
  transition: .18s;
}

.project-node:hover,
.project-node.selected {
  background: #f2f1ff;
}

.project-avatar {
  flex: 0 0 32px;
  width: 32px;
  height: 32px;
  display: grid;
  place-items: center;
  border-radius: 8px;
  color: #5d54cf;
  background: #e8e6ff;
  font-size: 13px;
  font-weight: 700;
}

.project-meta {
  min-width: 0;
  flex: 1;
}

.project-meta strong,
.project-meta small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-meta strong {
  color: #454b65;
  font-size: 13px;
}

.project-meta small {
  margin-top: 4px;
  color: #a2a6b5;
  font-size: 11px;
}

.project-count {
  color: #8e93a8;
  font-size: 12px;
}

.empty {
  padding: 25px 0;
  color: #a6aabb;
  text-align: center;
  font-size: 13px;
}

.review-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
}

.review-header h2 {
  margin: 0 0 6px;
  font-size: 17px;
}

.review-header p {
  max-width: 620px;
  margin: 0;
  overflow: hidden;
  color: #a0a5b6;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.review-header a,
a {
  color: #6258d8;
  text-decoration: none;
  font-size: 12px;
}

.review-table :deep(.el-table__header th) {
  color: #9297ab;
  font-size: 12px;
  font-weight: 500;
}

.mr-link {
  display: block;
  margin-bottom: 5px;
  font-size: 13px;
  font-weight: 600;
}

.mr-link span {
  margin-left: 4px;
  color: #555b73;
  font-weight: 400;
}

.review-table code {
  color: #adb1bf;
  font-size: 10px;
}

.progress-cell small {
  display: block;
  margin-top: 4px;
  color: #a6aaba;
  font-size: 11px;
}

.no-report {
  color: #b1b5c4;
  font-size: 11px;
}

@media (max-width: 1050px) {
  .project-layout {
    grid-template-columns: 240px minmax(0, 1fr);
  }
}

@media (max-width: 760px) {
  .summary-row {
    grid-template-columns: 1fr 1fr;
  }

  .project-layout {
    grid-template-columns: 1fr;
  }

  .project-tree-card {
    max-height: 260px;
    overflow: auto;
  }

  .page-heading {
    gap: 10px;
  }

  .page-heading h1 {
    font-size: 21px;
  }
}

.project-tree-card :deep(.el-card__body) {
  max-height: calc(100vh - 285px);
  padding: 10px 12px;
  overflow: auto;
}

.project-tree {
  background: transparent;
  --el-tree-node-hover-bg-color: #f5f4ff;
}

.project-tree :deep(.el-tree-node__content) {
  height: 42px;
  margin: 2px 0;
  border-radius: 8px;
}

.project-tree :deep(.el-tree-node.is-current > .el-tree-node__content) {
  color: #5146ca;
  background: #eeecff;
}

.project-tree :deep(.el-tree-node__expand-icon) {
  color: #9297aa;
}

.tree-node {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 8px;
  padding-right: 7px;
}

.tree-label {
  min-width: 0;
  overflow: hidden;
  font-size: 12px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tree-node-group .tree-label {
  color: #555b73;
}

.tree-project-avatar {
  display: grid;
  flex: 0 0 25px;
  width: 25px;
  height: 25px;
  place-items: center;
  border-radius: 7px;
  color: #6258d8;
  background: #ebe9ff;
  font-size: 11px;
  font-weight: 700;
}

.tree-folder {
  position: relative;
  flex: 0 0 24px;
  width: 24px;
  height: 16px;
  border-radius: 3px;
  background: #dfe5f7;
}

.tree-folder::before {
  position: absolute;
  top: -4px;
  left: 2px;
  width: 10px;
  height: 5px;
  border-radius: 3px 3px 0 0;
  background: #c6d0ee;
  content: '';
}

.tree-count {
  min-width: 20px;
  margin-left: auto;
  padding: 1px 6px;
  border-radius: 9px;
  color: #8b90a5;
  background: #f0f2f7;
  font-size: 10px;
  text-align: center;
}

.selected-project-path {
  color: #6258d8;
  font-size: 11px;
}

.progress-cell {
  min-width: 0;
}

.progress-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 7px;
}

.progress-head>span {
  color: #858ba1;
  font-size: 10px;
  font-variant-numeric: tabular-nums;
}

.progress-cell :deep(.el-progress) {
  margin-bottom: 4px;
}

.progress-cell :deep(.el-progress-bar__outer) {
  background: #eceef5;
}
</style>
