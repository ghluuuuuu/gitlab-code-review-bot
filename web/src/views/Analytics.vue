<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart, PieChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { getJSON, formatDate, type AnalyticsReport, type ProjectAnalytics } from '../adminApi'

use([CanvasRenderer, BarChart, PieChart, GridComponent, TooltipComponent, LegendComponent])

type AnalyticsView = 'projects' | 'contributors'
type ProjectTreeRow = ProjectAnalytics & { key: string; kind: 'group' | 'project'; children?: ProjectTreeRow[] }
type GroupTreeOption = { value: string; label: string; children?: GroupTreeOption[] }

const formatDay = (value: Date) => {
  const year = value.getFullYear()
  const month = String(value.getMonth() + 1).padStart(2, '0')
  const day = String(value.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}
const dayOffset = (days: number) => {
  const value = new Date()
  value.setHours(0, 0, 0, 0)
  value.setDate(value.getDate() + days)
  return formatDay(value)
}
const dateRange = ref<[string, string]>([dayOffset(-29), dayOffset(0)])
const analyticsView = ref<AnalyticsView>('projects')
const selectedGroups = ref<string[]>([])
const queryURL = computed(() => {
  const [from, to] = dateRange.value
  const params = new URLSearchParams({ from, to })
  for (const group of selectedGroups.value) params.append('group', group)
  return `/api/v1/admin/analytics?${params}`
})
const report = useQuery({ queryKey: ['admin-analytics', queryURL], queryFn: () => getJSON<AnalyticsReport>(queryURL.value), staleTime: 60_000 })
const groupOptions = computed(() => report.data.value?.groups ?? [])
const groupTreeOptions = computed<GroupTreeOption[]>(() => {
  const roots: GroupTreeOption[] = []
  const nodes = new Map<string, GroupTreeOption>()
  const counts = new Map(groupOptions.value.map(group => [group.path, group.project_count]))
  for (const group of groupOptions.value) {
    const parts = group.path.split('/').filter(Boolean)
    let children = roots
    let path = ''
    for (const part of parts) {
      path = path ? `${path}/${part}` : part
      let node = nodes.get(path)
      if (!node) {
        node = { value: path, label: `${part}（${counts.get(path) ?? 0}）`, children: [] }
        nodes.set(path, node)
        children.push(node)
      }
      children = node.children ?? []
    }
  }
  return roots
})
const groupsInitialized = ref(false)
watch(groupOptions, groups => {
  if (!groupsInitialized.value && groups.length) {
    selectedGroups.value = groups.map(group => group.path)
    groupsInitialized.value = true
  }
})
const groupSelectionLabel = computed(() => selectedGroups.value.length ? `已选 ${selectedGroups.value.length} 个分组` : '全部分组')
const setRange = (days: number) => { dateRange.value = [dayOffset(-(days - 1)), dayOffset(0)] }
const contributorRowKey = (row: { user_id?: number; name: string; email?: string }) => row.user_id ? `user:${row.user_id}` : row.email || row.name
const percentage = (value: number) => `${Number.isFinite(value) ? value.toFixed(1) : '0.0'}%`
const countEntries = (value?: Record<string, number>) => Object.entries(value ?? {}).sort((left, right) => right[1] - left[1])
const severityLabel = (value: string) => ({ critical: '严重', high: '高', medium: '中', low: '低', unknown: '未知' }[value] ?? value)
const severityType = (value: string) => ({ critical: 'danger', high: 'danger', medium: 'warning', low: 'info', unknown: 'info' }[value] ?? 'info') as 'danger' | 'warning' | 'info'
const categoryLabel = (value: string) => ({ correctness: '正确性', security: '安全', performance: '性能', maintainability: '可维护性', style: '规范', other: '其他' }[value] ?? value)
const projectStatus = (available: boolean, commits: number) => available ? commits > 0 ? '有更新' : '无更新' : '读取失败'
const projectStatusType = (available: boolean, commits: number) => available ? commits > 0 ? 'success' : 'info' : 'danger'
const projectTree = computed<ProjectTreeRow[]>(() => {
  const roots: ProjectTreeRow[] = []
  const groups = new Map<string, ProjectTreeRow>()
  const ensureGroup = (segments: string[]) => {
    let children = roots
    let path = ''
    let current: ProjectTreeRow | undefined
    for (const segment of segments) {
      path = path ? `${path}/${segment}` : segment
      current = groups.get(path)
      if (!current) {
        current = { key: `group:${path}`, kind: 'group', project_id: 0, name: segment, path_with_namespace: path, web_url: '', commit_count: 0, contributor_count: 0, review_count: 0, passed_reviews: 0, failed_reviews: 0, pass_rate: 0, finding_count: 0, blocking_findings: 0, severity_counts: {}, category_counts: {}, commit_data_available: true, children: [] }
        groups.set(path, current)
        children.push(current)
      }
      children = current.children ?? []
    }
    return children
  }
  for (const project of report.data.value?.projects ?? []) {
    const segments = project.path_with_namespace.split('/').filter(Boolean)
    const children = ensureGroup(segments.slice(0, -1))
    children.push({ ...project, key: `project:${project.project_id}`, kind: 'project' })
  }
  const aggregate = (row: ProjectTreeRow): ProjectTreeRow => {
    if (row.kind === 'project') return row
    const children = (row.children ?? []).map(aggregate)
    row.children = children
    row.commit_count = children.reduce((sum, child) => sum + child.commit_count, 0)
    row.contributor_count = children.reduce((sum, child) => sum + child.contributor_count, 0)
    row.review_count = children.reduce((sum, child) => sum + child.review_count, 0)
    row.passed_reviews = children.reduce((sum, child) => sum + child.passed_reviews, 0)
    row.failed_reviews = children.reduce((sum, child) => sum + child.failed_reviews, 0)
    row.finding_count = children.reduce((sum, child) => sum + child.finding_count, 0)
    row.blocking_findings = children.reduce((sum, child) => sum + child.blocking_findings, 0)
    row.pass_rate = row.review_count ? row.passed_reviews / row.review_count * 100 : 0
    row.commit_data_available = children.every(child => child.commit_data_available)
    row.latest_commit_at = children.map(child => child.latest_commit_at ?? '').sort().at(-1)
    return row
  }
  return roots.map(aggregate)
})
const topProjects = computed(() => [...(report.data.value?.projects ?? [])].filter(project => project.commit_count > 0).sort((left, right) => right.commit_count - left.commit_count).slice(0, 12).reverse())
const topQualityProjects = computed(() => [...(report.data.value?.projects ?? [])].filter(project => project.finding_count > 0).sort((left, right) => right.finding_count - left.finding_count).slice(0, 12).reverse())
const projectCommitChart = computed(() => ({
  tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } }, grid: { left: 145, right: 24, top: 12, bottom: 28 },
  xAxis: { type: 'value', minInterval: 1 }, yAxis: { type: 'category', data: topProjects.value.map(project => project.path_with_namespace.split('/').at(-1)) },
  series: [{ name: '提交数', type: 'bar', data: topProjects.value.map(project => project.commit_count), itemStyle: { color: '#6d63d9', borderRadius: [0, 5, 5, 0] }, barMaxWidth: 18 }],
}))
const projectQualityChart = computed(() => ({
  tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } }, legend: { bottom: 0 }, grid: { left: 145, right: 24, top: 12, bottom: 45 },
  xAxis: { type: 'value', minInterval: 1 }, yAxis: { type: 'category', data: topQualityProjects.value.map(project => project.path_with_namespace.split('/').at(-1)) },
  series: [
    { name: '严重', type: 'bar', stack: 'severity', data: topQualityProjects.value.map(project => project.severity_counts.critical ?? 0), itemStyle: { color: '#df5666' } },
    { name: '高', type: 'bar', stack: 'severity', data: topQualityProjects.value.map(project => project.severity_counts.high ?? 0), itemStyle: { color: '#ed8c45' } },
    { name: '中', type: 'bar', stack: 'severity', data: topQualityProjects.value.map(project => project.severity_counts.medium ?? 0), itemStyle: { color: '#e4b34e' } },
    { name: '低', type: 'bar', stack: 'severity', data: topQualityProjects.value.map(project => project.severity_counts.low ?? 0), itemStyle: { color: '#6f9bd2' } },
    { name: '未知', type: 'bar', stack: 'severity', data: topQualityProjects.value.map(project => project.severity_counts.unknown ?? 0), itemStyle: { color: '#a3a9b8' } },
  ],
}))
const topContributors = computed(() => (report.data.value?.contributors ?? []).filter(item => item.changed_lines > 0).slice(0, 15).reverse())
const contributorChart = computed(() => ({
  tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, valueFormatter: (value: number) => `${value.toLocaleString()} 行` }, grid: { left: 120, right: 24, top: 12, bottom: 28 },
  xAxis: { type: 'value', minInterval: 1 }, yAxis: { type: 'category', data: topContributors.value.map(item => item.name || item.username || '未知') },
  series: [{ name: '代码变更行数', type: 'bar', data: topContributors.value.map(item => item.changed_lines), itemStyle: { color: '#4da58f', borderRadius: [0, 5, 5, 0] }, barMaxWidth: 18 }],
}))
const contributorShareChart = computed(() => ({
  tooltip: { trigger: 'item', formatter: '{b}<br/>{c} 行（{d}%）' }, legend: { type: 'scroll', bottom: 0 },
  series: [{ name: '代码变更占比', type: 'pie', radius: ['42%', '68%'], center: ['50%', '44%'], data: (report.data.value?.contributors ?? []).slice(0, 10).map(item => ({ name: item.name || item.username || '未知', value: item.changed_lines })), label: { formatter: '{b}\n{d}%' } }],
}))
</script>

<template>
  <div class="page-heading">
    <div><span class="eyebrow">ADMIN ANALYTICS</span><h1>综合分析</h1><p>按时间段汇总所有 GitLab 项目的更新、人员贡献和代码质量，仅超管可访问。</p></div>
    <div class="range-actions">
      <el-button-group><el-button @click="setRange(7)">近 7 天</el-button><el-button @click="setRange(30)">近 30 天</el-button><el-button @click="setRange(90)">近 90 天</el-button></el-button-group>
      <el-tree-select v-model="selectedGroups" :data="groupTreeOptions" multiple show-checkbox check-strictly default-expand-all collapse-tags collapse-tags-tooltip clearable filterable :placeholder="groupSelectionLabel" class="group-filter" node-key="value" />
      <el-date-picker v-model="dateRange" type="daterange" value-format="YYYY-MM-DD" :clearable="false" unlink-panels range-separator="至" start-placeholder="开始日期" end-placeholder="结束日期" />
      <el-button type="primary" :loading="report.isFetching.value" @click="report.refetch()">重新分析</el-button>
    </div>
  </div>

  <el-alert v-if="report.isError.value" type="error" show-icon :closable="false" title="分析数据加载失败" :description="String(report.error.value ?? '')" />
  <template v-if="report.data.value">
    <div class="period-note">统计周期：{{ report.data.value.from }} 至 {{ report.data.value.to }}</div>
    <section class="metric-grid">
      <article><span>项目更新</span><strong>{{ report.data.value.summary.updated_projects }}<small> / {{ report.data.value.summary.project_count }}</small></strong><p>时间段内有提交的项目</p></article>
      <article><span>代码提交</span><strong>{{ report.data.value.summary.commit_count }}</strong><p>{{ report.data.value.summary.contributor_count }} 位贡献者</p></article>
      <article><span>完成审查</span><strong>{{ report.data.value.summary.review_count }}</strong><p>通过 {{ report.data.value.summary.passed_reviews }} · 未通过 {{ report.data.value.summary.failed_reviews }}</p></article>
      <article><span>审查通过率</span><strong>{{ percentage(report.data.value.quality.pass_rate) }}</strong><p>仅统计完成且有质量结论的审查</p></article>
      <article><span>发现缺陷</span><strong>{{ report.data.value.summary.finding_count }}</strong><p>阻断缺陷 {{ report.data.value.summary.blocking_findings }}</p></article>
      <article :class="{ warning: report.data.value.summary.unavailable_projects }"><span>数据完整性</span><strong>{{ report.data.value.summary.project_count - report.data.value.summary.unavailable_projects }}<small> / {{ report.data.value.summary.project_count }}</small></strong><p>{{ report.data.value.summary.unavailable_projects ? `${report.data.value.summary.unavailable_projects} 个项目提交数据不可用` : '所有项目读取成功' }}</p></article>
    </section>

    <section class="quality-overview">
      <div class="section-title"><div><span class="eyebrow">QUALITY</span><h2>代码质量概览</h2></div></div>
      <div class="quality-panels">
        <article><h3>严重度分布</h3><div class="tag-list"><el-tag v-for="entry in countEntries(report.data.value.quality.severity_counts)" :key="entry[0]" :type="severityType(entry[0])" effect="light">{{ severityLabel(entry[0]) }} {{ entry[1] }}</el-tag><span v-if="!countEntries(report.data.value.quality.severity_counts).length" class="empty-text">暂无缺陷</span></div></article>
        <article><h3>缺陷类别</h3><div class="category-list"><div v-for="entry in countEntries(report.data.value.quality.category_counts).slice(0, 8)" :key="entry[0]"><span>{{ categoryLabel(entry[0]) }}</span><b>{{ entry[1] }}</b></div><span v-if="!countEntries(report.data.value.quality.category_counts).length" class="empty-text">暂无缺陷</span></div></article>
      </div>
    </section>

    <section class="analysis-switcher">
      <el-radio-group v-model="analyticsView" size="large">
        <el-radio-button value="projects">项目更新与质量</el-radio-button>
        <el-radio-button value="contributors">人员贡献度</el-radio-button>
      </el-radio-group>
    </section>

    <template v-if="analyticsView === 'projects'">
      <section class="chart-grid">
        <article><div class="chart-title"><b>项目提交活跃度</b><span>提交数前 12 个项目</span></div><v-chart class="analytics-chart" :option="projectCommitChart" autoresize /></article>
        <article><div class="chart-title"><b>项目缺陷分布</b><span>按严重度分色</span></div><v-chart class="analytics-chart" :option="projectQualityChart" autoresize /></article>
      </section>
      <section class="table-section">
        <div class="section-title"><div><span class="eyebrow">PROJECT ACTIVITY</span><h2>项目更新与质量</h2></div><span>按 GitLab 命名空间分组 · {{ report.data.value.projects.length }} 个项目</span></div>
        <el-table class="project-tree-table" :data="projectTree" row-key="key" default-expand-all :tree-props="{ children: 'children' }" v-loading="report.isFetching.value">
          <el-table-column label="分组 / 项目" min-width="330"><template #default="scope"><div v-if="scope.row.kind === 'group'"><b>{{ scope.row.name }}</b><small>{{ scope.row.path_with_namespace }}</small></div><div v-else><a :href="scope.row.web_url" target="_blank" rel="noreferrer" class="project-link">{{ scope.row.name }}</a><small>{{ scope.row.path_with_namespace }}</small></div></template></el-table-column>
          <el-table-column label="更新状态" width="110"><template #default="scope"><el-tag :type="projectStatusType(scope.row.commit_data_available, scope.row.commit_count)" effect="light">{{ projectStatus(scope.row.commit_data_available, scope.row.commit_count) }}</el-tag></template></el-table-column>
          <el-table-column label="提交 / 人员" width="120"><template #default="scope"><b>{{ scope.row.commit_count }}</b><small>{{ scope.row.kind === 'project' ? `${scope.row.contributor_count} 人` : '分组汇总' }}</small></template></el-table-column>
          <el-table-column label="最后提交" width="170"><template #default="scope">{{ formatDate(scope.row.latest_commit_at) }}</template></el-table-column>
          <el-table-column label="审查" width="110"><template #default="scope"><b>{{ scope.row.review_count }}</b><small>通过 {{ scope.row.passed_reviews }}</small></template></el-table-column>
          <el-table-column label="通过率" width="105"><template #default="scope"><span :class="scope.row.review_count && scope.row.pass_rate < 80 ? 'risk' : ''">{{ scope.row.review_count ? percentage(scope.row.pass_rate) : '—' }}</span></template></el-table-column>
          <el-table-column label="缺陷" width="110"><template #default="scope"><b :class="scope.row.blocking_findings ? 'risk' : ''">{{ scope.row.finding_count }}</b><small>阻断 {{ scope.row.blocking_findings }}</small></template></el-table-column>
        </el-table>
      </section>
    </template>

    <template v-else>
      <section class="chart-grid">
        <article><div class="chart-title"><b>贡献者代码变更排行</b><span>按增删行数合计</span></div><v-chart class="analytics-chart" :option="contributorChart" autoresize /></article>
        <article><div class="chart-title"><b>代码变更占比</b><span>按增删行数合计</span></div><v-chart class="analytics-chart" :option="contributorShareChart" autoresize /></article>
      </section>
      <section class="table-section">
        <div class="section-title"><div><span class="eyebrow">CONTRIBUTORS</span><h2>人员贡献度</h2></div><span>按代码变更行数排序 · 新增 + 删除</span></div>
        <el-table :data="report.data.value.contributors" stripe :row-key="contributorRowKey" empty-text="该时间段暂无代码贡献">
          <el-table-column type="index" label="#" width="58" />
          <el-table-column label="贡献者" min-width="240"><template #default="scope"><div class="contributor-name"><el-avatar :size="32" :src="scope.row.avatar_url">{{ (scope.row.name || '?').slice(0, 1) }}</el-avatar><div><a v-if="scope.row.web_url" :href="scope.row.web_url" target="_blank" rel="noreferrer">{{ scope.row.name || scope.row.username || '未知贡献者' }}</a><b v-else>{{ scope.row.name || '未知贡献者' }}</b><small>{{ scope.row.email || '未提供邮箱' }}<el-tag v-if="scope.row.user_id" size="small" type="success" effect="plain">GitLab #{{ scope.row.user_id }}</el-tag><el-tag v-else size="small" type="info" effect="plain">提交身份</el-tag></small></div></div></template></el-table-column>
          <el-table-column prop="added_lines" label="新增行" width="110" sortable />
          <el-table-column prop="deleted_lines" label="删除行" width="110" sortable />
          <el-table-column prop="changed_lines" label="变更行" width="110" sortable />
          <el-table-column prop="project_count" label="参与项目" width="110" sortable />
          <el-table-column label="项目范围" min-width="280"><template #default="scope"><div class="project-tags"><el-tag v-for="project in scope.row.projects.slice(0, 4)" :key="project" size="small" effect="plain">{{ project }}</el-tag><span v-if="scope.row.projects.length > 4">+{{ scope.row.projects.length - 4 }}</span></div></template></el-table-column>
          <el-table-column label="最近提交" width="170"><template #default="scope">{{ formatDate(scope.row.latest_commit_at) }}</template></el-table-column>
        </el-table>
      </section>
    </template>
  </template>
</template>
<style scoped>
.page-heading{display:flex;align-items:flex-end;justify-content:space-between;gap:24px;margin-bottom:18px}.page-heading h1{margin:3px 0 6px;font-size:27px;color:#25293b}.page-heading p{margin:0;color:#858ca3;font-size:13px}.eyebrow{color:#6d63d9;font-size:10px;font-weight:800;letter-spacing:1.4px}.range-actions{display:flex;align-items:center;justify-content:flex-end;gap:10px;flex-wrap:wrap}.range-actions :deep(.el-date-editor){width:270px}.period-note{margin:8px 0 14px;color:#858ca3;font-size:12px}.metric-grid{display:grid;grid-template-columns:repeat(6,minmax(0,1fr));gap:12px}.metric-grid article{padding:18px;border:1px solid #e7e9f2;border-radius:14px;background:#fff;box-shadow:0 8px 22px #363c6908}.metric-grid article.warning{border-color:#f3c56d;background:#fffaf0}.metric-grid span{display:block;color:#878da1;font-size:12px}.metric-grid strong{display:block;margin:8px 0 5px;color:#282c40;font-size:27px;line-height:1}.metric-grid strong small{font-size:14px;color:#9298aa}.metric-grid p{margin:0;color:#9aa0b3;font-size:11px;line-height:1.45}.quality-overview,.table-section{margin-top:18px;padding:20px;border:1px solid #e6e8f1;border-radius:15px;background:#fff}.section-title{display:flex;align-items:center;justify-content:space-between;margin-bottom:15px}.section-title h2{margin:3px 0 0;font-size:17px;color:#303449}.section-title>span{color:#949aad;font-size:12px}.quality-panels{display:grid;grid-template-columns:1fr 1fr;gap:16px}.quality-panels article{padding:16px;border-radius:12px;background:#f8f9fd}.quality-panels h3{margin:0 0 12px;font-size:13px;color:#5b6074}.tag-list{display:flex;gap:8px;flex-wrap:wrap}.category-list{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px 18px}.category-list div{display:flex;justify-content:space-between;padding-bottom:6px;border-bottom:1px solid #e8eaf2;color:#6f7589;font-size:12px}.category-list b{color:#34384b}.empty-text{color:#a2a7b7;font-size:12px}.project-link{display:block;color:#4f57be;font-weight:700;text-decoration:none}.el-table small{display:block;margin-top:4px;color:#969caf;font-size:11px}.risk{color:#d94c55;font-weight:700}.project-tags{display:flex;align-items:center;gap:5px;flex-wrap:wrap}.project-tags>span{color:#898fa3;font-size:11px}@media(max-width:1200px){.metric-grid{grid-template-columns:repeat(3,1fr)}}@media(max-width:760px){.page-heading{align-items:flex-start;flex-direction:column}.range-actions{justify-content:flex-start}.metric-grid{grid-template-columns:repeat(2,1fr)}.quality-panels{grid-template-columns:1fr}}
.group-filter{width:320px}.group-name small,.project-name small{display:block;margin-top:3px;color:#98a0b2;font-size:11px}
.project-tree-table :deep(td.el-table__cell div){display:inline-block;vertical-align:middle}
.analysis-switcher{display:flex;justify-content:center;margin:20px 0 4px}.chart-grid{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin-top:16px}.chart-grid article{min-width:0;padding:18px;border:1px solid #e6e8f1;border-radius:15px;background:#fff}.chart-title{display:flex;align-items:flex-start;justify-content:space-between;margin-bottom:8px}.chart-title b{color:#34384c;font-size:14px}.chart-title span{color:#969caf;font-size:11px}.analytics-chart{width:100%;height:340px}.group-name{display:flex;align-items:center;gap:7px;color:#444960}.group-name>span{color:#786edc}.group-name small{display:inline;margin:0 0 0 6px}.contributor-name{display:flex;align-items:center;gap:10px}.contributor-name a{color:#4f57be;font-weight:700;text-decoration:none}.contributor-name small{display:flex;align-items:center;gap:6px}.contributor-name :deep(.el-tag){margin-left:3px}@media(max-width:980px){.chart-grid{grid-template-columns:1fr}.analytics-chart{height:310px}}
</style>
