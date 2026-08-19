<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { buildProjectTree, type ProjectTreeNode } from '../projectTree'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { GraphChart, LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'

use([CanvasRenderer, GraphChart, LineChart, GridComponent, TooltipComponent, LegendComponent])

type Author = { id?: number; name: string; username?: string; avatar_url?: string; web_url?: string }
type QualityMR = {
  project_id: number
  mr_iid: number
  title: string
  web_url: string
  source_branch: string
  target_branch: string
  state: 'opened' | 'merged' | 'closed' | string
  reviewed: boolean
  updated_at: string
  author: Author
  issue_counts: Record<string, number>
  severity_counts: Record<string, number>
  file_issue_counts: Record<string, number>
  file_issue_type_counts: Record<string, Record<string, number>>
  change_analysis?: string
  report_url?: string
}
type QualityProject = {
  id: number
  name: string
  description: string
  path_with_namespace: string
  web_url: string
}
type BranchRelationKind = 'mr' | 'git' | 'fork'
type QualityBranchGraph = { project_id: number; branches: Array<{ name: string; changed_files: number; open_merge_requests: number }>; relations: Array<{ source: string; target: string; kind: BranchRelationKind; mr_iid: number; title: string; state: string; web_url?: string; changed_files: number }> }
type QualityFile = { path: string; old_path?: string; additions: number; deletions: number; authors: Author[] }
type FileTreeNode = { key: string; label: string; kind: 'directory' | 'file'; file?: QualityFile; issueCount?: number; issueCounts?: Record<string, number>; children?: FileTreeNode[] }
type IssueSegment = { category: string; label: string; count: number; percent: number; color: string }
type FixTrendPoint = { time: string; issue_count: number; fixed_count: number }
type ChangeAnalysisSection = { title: string; content: string }

const getJSON = async <T,>(url: string): Promise<T> => {
  const response = await fetch(url)
  if (!response.ok) throw new Error(await response.text())
  return response.json()
}

const CATEGORY_META: Record<string, { label: string; color: string }> = {
  security: { label: '安全', color: '#e75d6c' },
  correctness: { label: '正确性', color: '#f39b46' },
  reliability: { label: '可靠性', color: '#8b6fda' },
  performance: { label: '性能', color: '#4a89dc' },
  maintainability: { label: '可维护性', color: '#45b7a4' },
  style: { label: '规范', color: '#7b8da8' },
  other: { label: '其他', color: '#a2a7b8' },
}
const FALLBACK_COLORS = ['#e07a5f', '#6d8fd1', '#7a69c7', '#4fa58d', '#c08a45', '#8b7f72']

const projects = useQuery({
  queryKey: ['quality-projects'],
  queryFn: () => getJSON<QualityProject[]>('/api/v1/admin/quality/projects'),
})
const selectedProjectId = ref<number | null>(null)
const qualityView = ref<'branches' | 'mrs'>('mrs')
const mrFilter = ref<'opened' | 'merged' | 'closed' | 'unreviewed' | 'reviewed'>('opened')
const projectTree = computed(() => buildProjectTree(projects.data.value ?? []))
const selectedProject = computed(() => {
  const list = projects.data.value ?? []
  if (selectedProjectId.value === null) return list[0]
  return list.find(project => project.id === selectedProjectId.value) ?? list[0]
})
const selectProject = (node: ProjectTreeNode<QualityProject>) => {
  if (node.project) selectedProjectId.value = node.project.id
}
const selectedProjectKey = computed(() => selectedProject.value?.id ?? 0)
const mergeRequests = useQuery({
  queryKey: ['quality-merge-requests', selectedProjectKey],
  queryFn: () => getJSON<QualityMR[]>(`/api/v1/admin/quality/projects/${selectedProjectKey.value}/mrs`),
  enabled: computed(() => selectedProjectKey.value > 0),
  staleTime: 60 * 1000,
})
const selectedMergeRequests = computed(() => {
  const rows = mergeRequests.data.value ?? []
  if (mrFilter.value === 'reviewed') return rows.filter(mr => mr.reviewed)
  if (mrFilter.value === 'unreviewed') return rows.filter(mr => !mr.reviewed)
  return rows.filter(mr => mr.state === mrFilter.value)
})
const mrFilterCount = (filter: 'opened' | 'merged' | 'closed' | 'unreviewed' | 'reviewed') => {
  const rows = mergeRequests.data.value ?? []
  if (filter === 'reviewed') return rows.filter(mr => mr.reviewed).length
  if (filter === 'unreviewed') return rows.filter(mr => !mr.reviewed).length
  return rows.filter(mr => mr.state === filter).length
}
const mrStateLabel = (state: string) => ({ opened: '待合并', merged: '已合并', closed: '已取消' }[state] ?? state)
const mrStateType = (state: string) => ({ opened: 'warning', merged: 'success', closed: 'info' }[state] ?? 'info') as 'warning' | 'success' | 'info'
const branchGraph = useQuery({
  queryKey: ['quality-branches', selectedProjectKey],
  queryFn: () => getJSON<QualityBranchGraph>(`/api/v1/admin/quality/projects/${selectedProjectKey.value}/branches`),
  enabled: computed(() => selectedProjectKey.value > 0),
  staleTime: 5 * 60 * 1000,
})
const relationVisibility = reactive<Record<BranchRelationKind, boolean>>({ mr: true, git: true, fork: true })
const toggleRelation = (kind: BranchRelationKind) => { relationVisibility[kind] = !relationVisibility[kind] }
const visibleBranchRelations = computed(() => (branchGraph.data.value?.relations ?? []).filter(relation => relationVisibility[relation.kind]))
type BranchLineAnimator = { scope?: string; when: (duration: number, value: { lineDashOffset: number }) => { start: () => void } }
type BranchLine = { animate: (property: string, loop: boolean) => BranchLineAnimator; stopAnimation: (scope?: string) => void; style?: { lineDashOffset?: number } }
type BranchEdgeElement = { childOfName?: (name: string) => BranchLine | undefined }
type BranchEdgeData = { eachItemGraphicEl: (callback: (element: BranchEdgeElement) => void) => void }
type BranchSeriesModel = { getData: (dataType: 'edge') => BranchEdgeData }
type BranchChartModel = { getSeriesByIndex: (index: number) => BranchSeriesModel }
type BranchChartInstance = { getModel: () => BranchChartModel; getZr: () => { animation: { start: () => void } } }
type BranchChartRef = { chart?: BranchChartInstance | { value?: BranchChartInstance } }
const branchChart = ref<BranchChartRef | null>(null)
const branchChartInstance = () => {
  const exposed = branchChart.value?.chart
  if (!exposed) return undefined
  return 'getModel' in exposed ? exposed : exposed.value
}
const flowAnimationScope = 'branch-flow'
const startBranchFlowAnimation = () => {
  const chart = branchChartInstance()
  const edgeData = chart?.getModel().getSeriesByIndex(0)?.getData('edge')
  if (!chart || !edgeData) return
  edgeData.eachItemGraphicEl((element: BranchEdgeElement) => {
    const line = element.childOfName?.('line')
    if (!line) return
    line.stopAnimation(flowAnimationScope)
    line.style = { ...line.style, lineDashOffset: 0 }
    const animator = line.animate('style', true)
    animator.scope = flowAnimationScope
    animator.when(900, { lineDashOffset: -18 }).start()
  })
  chart.getZr().animation.start()
}
onBeforeUnmount(() => {
  const edgeData = branchChartInstance()?.getModel().getSeriesByIndex(0)?.getData('edge')
  edgeData?.eachItemGraphicEl((element: BranchEdgeElement) => element.childOfName?.('line')?.stopAnimation(flowAnimationScope))
})

const branchGraphOption = computed(() => {
  const graph = branchGraph.data.value
  if (!graph) return { series: [] }
  const degree = new Map<string, number>()
  for (const relation of visibleBranchRelations.value) {
    degree.set(relation.source, (degree.get(relation.source) ?? 0) + 1)
    degree.set(relation.target, (degree.get(relation.target) ?? 0) + 1)
  }
  const maxChangedFiles = Math.max(1, ...graph.branches.map(branch => branch.changed_files ?? 0))
  const dense = graph.branches.length > 30
  const nodes = graph.branches.map(branch => {
    const changedFiles = branch.changed_files ?? 0
    const openMergeRequests = branch.open_merge_requests ?? 0
    const intensity = changedFiles / maxChangedFiles
    const lightness = Math.round(88 - intensity * 42)
    const nodeColor = `hsl(214 52% ${lightness}%)`
    const borderColor = openMergeRequests > 0 ? '#ee9b32' : branch.name === 'main' || branch.name === 'master' ? '#5146ca' : '#d2d8e7'
    const labelColor = intensity > .48 ? '#fff' : '#3f4e67'
    const maxNameLength = dense ? 16 : 22
    const shortName = branch.name.length > maxNameLength ? `${branch.name.slice(0, maxNameLength - 3)}…` : branch.name
    return {
      id: branch.name,
      name: `${shortName}\n${changedFiles} 个文件`,
      rawName: branch.name,
      changedFiles,
      openMergeRequests,
      relations: degree.get(branch.name) ?? 0,
      symbolSize: dense ? Math.min(72, 34 + Math.sqrt(degree.get(branch.name) ?? 0) * 2.5 + intensity * 12) : Math.min(94, 48 + Math.sqrt(degree.get(branch.name) ?? 0) * 4 + intensity * 17),
      itemStyle: { color: nodeColor, borderColor, borderWidth: openMergeRequests > 0 ? 4 : branch.name === 'main' || branch.name === 'master' ? 3 : 1.5, shadowBlur: openMergeRequests > 0 ? 17 : 8, shadowColor: openMergeRequests > 0 ? '#ee9b3277' : '#30426025' },
      emphasis: { itemStyle: { color: nodeColor, borderColor, borderWidth: 5, shadowBlur: 18, shadowColor: '#5146ca66' }, label: { show: true, color: labelColor } },
      label: { show: true, position: 'inside', color: labelColor, fontSize: dense ? 7.5 : 9, fontWeight: 600, lineHeight: dense ? 10 : 13 },
    }
  })
  const grouped = new Map<string, { source: string; target: string; kind: BranchRelationKind; count: number; latest: string; state: string; changedFiles: number }>()
  for (const relation of visibleBranchRelations.value) {
    const key = `${relation.kind}\u0000${relation.source}\u0000${relation.target}`
    const current = grouped.get(key)
    if (current) {
      current.count++
      current.changedFiles += relation.changed_files ?? 0
    } else {
      const latest = relation.kind === 'mr' ? `!${relation.mr_iid} ${relation.title}` : relation.kind === 'fork' ? relation.title : 'Git 提交树推断关系'
      grouped.set(key, { source: relation.source, target: relation.target, kind: relation.kind, count: 1, latest, state: relation.state, changedFiles: relation.changed_files ?? 0 })
    }
  }
  const maxRelationFiles = Math.max(1, ...Array.from(grouped.values()).filter(relation => relation.kind === 'mr').map(relation => relation.changedFiles))
  const links = Array.from(grouped.values()).map(relation => {
    const isGitRelation = relation.kind === 'git'
    const isForkRelation = relation.kind === 'fork'
    const lightness = Math.round(82 - relation.changedFiles / maxRelationFiles * 34)
    const color = isGitRelation ? '#7b61d1' : isForkRelation ? '#e08632' : `hsl(160 42% ${lightness}%)`
    return { ...relation, lineStyle: { color, width: isGitRelation || isForkRelation ? 2.2 : Math.min(4.5, 1.2 + relation.count * .35), type: 'dashed', opacity: isGitRelation ? .82 : .9, curveness: isGitRelation ? -.12 : isForkRelation ? .24 : .12 } }
  })
  return {
    tooltip: { confine: true, formatter: (params: { dataType: string; data: { rawName?: string; changedFiles?: number; openMergeRequests?: number; relations?: number; latest?: string; count?: number; kind?: BranchRelationKind } }) => params.dataType === 'edge' ? `${params.data.latest ?? ''}<br/>${params.data.kind === 'git' ? 'Git 提交树关系' : params.data.kind === 'fork' ? 'Fork 来源推断' : `${params.data.count ?? 1} 条 MR · ${params.data.changedFiles ?? 0} 个变动文件`}` : `${params.data.rawName ?? ''}<br/>${params.data.changedFiles ?? 0} 个变动文件 · ${params.data.relations ?? 0} 条关系${params.data.openMergeRequests ? `<br/><span style="color:#d98720">待合并 MR ${params.data.openMergeRequests} 条</span>` : ''}` },
    animationDuration: 700,
    animationDurationUpdate: 650,
    series: [{
      type: 'graph', layout: 'force', data: nodes, links, roam: true, draggable: true, scaleLimit: { min: .35, max: 4 }, edgeSymbol: ['none', 'arrow'], edgeSymbolSize: [0, 8],
      force: { initLayout: 'circular', repulsion: dense ? 680 : 420, edgeLength: dense ? [145, 220] : [120, 185], gravity: dense ? .08 : .1, friction: .6, layoutAnimation: true },
      emphasis: { focus: 'adjacency', blurScope: 'coordinateSystem', lineStyle: { width: 4.5, opacity: 1 }, label: { show: true } },
      blur: { itemStyle: { opacity: .12 }, lineStyle: { opacity: .06 }, label: { opacity: .18 } },
    }],
  }
})
const filesByMR = reactive<Record<string, QualityFile[]>>({})
const filesLoading = reactive<Record<string, boolean>>({})
const filesError = reactive<Record<string, string>>({})
const mrKey = (mr: QualityMR) => `${mr.project_id}:${mr.mr_iid}`
const loadMRFiles = async (mr: QualityMR, expandedRows: QualityMR[]) => {
  if (!expandedRows.some(item => mrKey(item) === mrKey(mr))) return
  const key = mrKey(mr)
  if (filesByMR[key] || filesLoading[key]) return
  filesLoading[key] = true
  delete filesError[key]
  try {
    filesByMR[key] = await getJSON<QualityFile[]>(`/api/v1/admin/quality/projects/${mr.project_id}/mrs/${mr.mr_iid}/files`)
  } catch (error) {
    filesError[key] = error instanceof Error ? error.message : String(error)
  } finally {
    filesLoading[key] = false
  }
}
const issueTypeEntries = (counts?: Record<string, number>) => Object.entries(counts ?? {}).filter(([, count]) => count > 0).sort((left, right) => right[1] - left[1])

const buildFileTree = (files: QualityFile[], issueCounts: Record<string, number>, issueTypeCounts: Record<string, Record<string, number>>): FileTreeNode[] => {
  const roots: FileTreeNode[] = []
  const directories = new Map<string, FileTreeNode>()
  for (const file of files) {
    const segments = file.path.split('/').filter(Boolean)
    let children = roots
    let directoryPath = ''
    for (const segment of segments.slice(0, -1)) {
      directoryPath = directoryPath ? `${directoryPath}/${segment}` : segment
      const key = `directory:${directoryPath}`
      let directory = directories.get(key)
      if (!directory) {
        directory = { key, label: segment, kind: 'directory', children: [] }
        directories.set(key, directory)
        children.push(directory)
      }
      children = directory.children!
    }
    children.push({ key: `file:${file.path}`, label: segments[segments.length - 1] || file.path, kind: 'file', file, issueCount: issueCounts[file.path] ?? 0, issueCounts: issueTypeCounts[file.path] ?? {} })
  }
  const sortNodes = (nodes: FileTreeNode[]) => {
    nodes.sort((left, right) => left.kind === right.kind
      ? left.label.localeCompare(right.label, 'zh-CN')
      : left.kind === 'directory' ? -1 : 1)
    nodes.forEach(node => node.children && sortNodes(node.children))
  }
  sortNodes(roots)
  return roots
}

const issueSegments = (mr: QualityMR): IssueSegment[] => {
  const entries = Object.entries(mr.issue_counts ?? {}).filter(([, count]) => count > 0)
  const total = entries.reduce((sum, [, count]) => sum + count, 0)
  return entries
    .sort((left, right) => right[1] - left[1])
    .map(([category, count], index) => ({
      category,
      label: CATEGORY_META[category]?.label ?? category,
      count,
      percent: total === 0 ? 0 : count / total * 100,
      color: CATEGORY_META[category]?.color ?? FALLBACK_COLORS[index % FALLBACK_COLORS.length],
    }))
}
const issueTotal = (mr: QualityMR) => Object.values(mr.issue_counts ?? {}).reduce((sum, count) => sum + count, 0)
const blockingTotal = (mr: QualityMR) => (mr.severity_counts?.critical ?? 0) + (mr.severity_counts?.high ?? 0)
const authorInitial = (author?: Author) => (author?.name || author?.username || '?').slice(0, 1).toUpperCase()
const formatDate = (value: string) => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—'

const trendVisible = ref(false)
const trendMR = ref<QualityMR | null>(null)
const trendPoints = ref<FixTrendPoint[]>([])
const trendLoading = ref(false)
const trendError = ref('')
const openFixTrend = async (mr: QualityMR) => {
  trendMR.value = mr
  trendVisible.value = true
  trendLoading.value = true
  trendError.value = ''
  trendPoints.value = []
  try {
    trendPoints.value = await getJSON<FixTrendPoint[]>(`/api/v1/admin/quality/projects/${mr.project_id}/mrs/${mr.mr_iid}/trend`)
  } catch (error) {
    trendError.value = error instanceof Error ? error.message : String(error)
  } finally {
    trendLoading.value = false
  }
}
const trendChart = computed(() => ({
  tooltip: { trigger: 'axis' },
  legend: { data: ['问题数量', '累计已修复'] },
  grid: { left: 58, right: 28, top: 48, bottom: 58 },
  xAxis: {
    type: 'category', name: '时间', nameLocation: 'middle', nameGap: 40,
    data: trendPoints.value.map(point => new Date(point.time).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })),
    axisLabel: { rotate: trendPoints.value.length > 5 ? 25 : 0 },
  },
  yAxis: { type: 'value', name: '问题数量', minInterval: 1, min: 0 },
  series: [
    { name: '问题数量', type: 'line', smooth: true, symbolSize: 8, lineStyle: { width: 3, color: '#e75d6c' }, itemStyle: { color: '#e75d6c' }, data: trendPoints.value.map(point => point.issue_count) },
    { name: '累计已修复', type: 'line', smooth: true, symbolSize: 8, lineStyle: { width: 3, color: '#35a979' }, itemStyle: { color: '#35a979' }, data: trendPoints.value.map(point => point.fixed_count) },
  ],
}))

const changeVisible = ref(false)
const changeMR = ref<QualityMR | null>(null)
const openChangeAnalysis = (mr: QualityMR) => {
  changeMR.value = mr
  changeVisible.value = true
}
const changeAnalysisSections = computed<ChangeAnalysisSection[]>(() => {
  const text = changeMR.value?.change_analysis?.trim() ?? ''
  if (!text) return []
  const sections: ChangeAnalysisSection[] = []
  let title = '变更影响结论'
  let lines: string[] = []
  const flush = () => {
    const content = lines.join('\n').trim()
    if (content) sections.push({ title, content })
    lines = []
  }
  for (const line of text.split(/\r?\n/)) {
    const heading = line.match(/^#{1,6}\s+(.+)$/)
    if (heading) {
      flush()
      title = heading[1].trim()
    } else {
      lines.push(line)
    }
  }
  flush()
  return sections
})
</script>

<template>
  <div class="page-heading">
    <div>
      <h1>分析情况</h1>
      <p>按 GitLab 项目分析 Merge Request 的文件变更、缺陷构成与提交人</p>
    </div>
  </div>
  <div class="quality-layout">
    <el-card class="project-card" shadow="never">
      <template #header>
        <div class="card-title"><span>GitLab 项目</span><el-badge :value="projects.data.value?.length ?? 0"
            type="primary" /></div>
      </template>
      <div v-if="projects.isLoading.value" class="empty">加载项目中…</div>
      <div v-else-if="!(projects.data.value?.length)" class="empty">暂无分析数据</div>
      <el-tree v-else class="project-tree" :data="projectTree" node-key="key" :indent="12"
        :current-node-key="selectedProject ? `project:${selectedProject.id}` : undefined" :expand-on-click-node="false"
        default-expand-all highlight-current @node-click="selectProject">
        <template #default="{ data }">
          <div class="project-tree-node" :title="data.project?.path_with_namespace || data.label">
            <span v-if="data.kind === 'group'" class="folder-icon" />
            <span v-else class="project-avatar">{{ data.label.slice(0, 1).toUpperCase() }}</span>
            <span class="tree-label">{{ data.label }}</span>
            <span v-if="data.kind === 'group'" class="node-count">{{ data.projectCount }}</span>
            <span v-else-if="data.project?.id === selectedProjectKey && mergeRequests.data.value" class="node-count">{{ mergeRequests.data.value.length }}</span>
          </div>
        </template>
      </el-tree>
    </el-card>
    <el-card class="mr-card" shadow="never">
      <template #header>
        <div class="mr-header">
          <div>
            <h2>{{ selectedProject?.name || '选择项目' }}</h2>
            <p>{{ selectedProject?.path_with_namespace || '从左侧选择项目' }}</p>
          </div>
          <a v-if="selectedProject?.web_url" :href="selectedProject.web_url" target="_blank">打开项目 ↗</a>
        </div>
      </template>
      <div class="quality-view-tabs" role="tablist" aria-label="质量分析视图">
        <button type="button" role="tab" :aria-selected="qualityView === 'mrs'"
          :class="{ active: qualityView === 'mrs' }" @click="qualityView = 'mrs'">MR 质量列表</button>
        <button type="button" role="tab" :aria-selected="qualityView === 'branches'"
          :class="{ active: qualityView === 'branches' }" @click="qualityView = 'branches'">分支合并关系</button>
      </div>
      <div v-show="qualityView === 'branches'" role="tabpanel" aria-label="分支合并关系">
        <section class="branch-graph-section">
          <div class="branch-graph-header">
            <div><strong>分支合并关系</strong>
              <p>箭头和虚线动画表示流向；右上角可分别控制 MR、Git 提交树和 Fork 来源关系</p>
            </div>
            <div class="branch-graph-stats"><span>{{ branchGraph.data.value?.branches.length ?? 0 }} 个分支</span><span>{{
              visibleBranchRelations.length }} / {{ branchGraph.data.value?.relations.length ?? 0 }} 条关系</span></div>
          </div>
          <div v-if="branchGraph.data.value?.branches.length" class="branch-force-wrap"><v-chart ref="branchChart"
              class="branch-force-graph" :option="branchGraphOption" autoresize @finished="startBranchFlowAnimation" /><span
              class="branch-interaction-hint">拖拽节点 · 拖动画布 · 滚轮缩放 ·
              悬浮高亮关联</span>
            <div class="relation-legend" role="group" aria-label="关系显示开关">
              <button type="button" :class="{ active: relationVisibility.mr }" :aria-pressed="relationVisibility.mr" @click="toggleRelation('mr')"><i class="mr-relation" />MR 关系</button>
              <button type="button" :class="{ active: relationVisibility.git }" :aria-pressed="relationVisibility.git" @click="toggleRelation('git')"><i class="git-relation" />Git 提交树关系</button>
              <button type="button" :class="{ active: relationVisibility.fork }" :aria-pressed="relationVisibility.fork" @click="toggleRelation('fork')"><i class="fork-relation" />Fork 来源</button>
            </div>
            <div class="complexity-legend"><span>文件变动复杂度</span><i class="legend-gradient" /><small>浅 · 低</small><small>深
                ·
                高</small></div>
          </div>
          <el-empty v-else-if="!branchGraph.isLoading.value" description="该项目暂无分支合并关系" />
          <div v-else class="branch-graph-loading" v-loading="true" />
        </section>
      </div>
      <div v-show="qualityView === 'mrs'" role="tabpanel" aria-label="MR 质量列表">
        <div class="mr-filters" role="group" aria-label="MR 状态筛选">
          <el-radio-group v-model="mrFilter" size="small">
            <el-radio-button value="opened">待合并 <span class="filter-count">{{ mrFilterCount('opened') }}</span></el-radio-button>
            <el-radio-button value="merged">已合并 <span class="filter-count">{{ mrFilterCount('merged') }}</span></el-radio-button>
            <el-radio-button value="closed">已取消 <span class="filter-count">{{ mrFilterCount('closed') }}</span></el-radio-button>
            <el-radio-button value="unreviewed">未审查 <span class="filter-count">{{ mrFilterCount('unreviewed') }}</span></el-radio-button>
            <el-radio-button value="reviewed">已审查 <span class="filter-count">{{ mrFilterCount('reviewed') }}</span></el-radio-button>
          </el-radio-group>
        </div>
        <el-table :data="selectedMergeRequests" row-key="mr_iid" v-loading="mergeRequests.isLoading.value"
          class="quality-table" @expand-change="loadMRFiles">
          <el-table-column type="expand" width="48">
            <template #default="scope">
              <div class="file-expand" v-loading="filesLoading[mrKey(scope.row)]">
                <div v-if="filesError[mrKey(scope.row)]" class="file-error">文件变更加载失败：{{ filesError[mrKey(scope.row)] }}
                </div>
                <el-tree v-else-if="filesByMR[mrKey(scope.row)]?.length" class="file-tree"
                  :data="buildFileTree(filesByMR[mrKey(scope.row)], scope.row.file_issue_counts ?? {}, scope.row.file_issue_type_counts ?? {})"
                  node-key="key" :indent="22" default-expand-all>
                  <template #default="{ data }">
                    <div class="file-tree-node" :title="data.file?.path || data.label">
                      <span v-if="data.kind === 'directory'" class="folder-icon file-folder" />
                      <span v-else class="file-dot" />
                      <span class="file-name">{{ data.label }}</span>
                      <span v-if="data.kind === 'file' && data.issueCount" class="file-issue-tags"><el-tag
                          class="file-issue-tag total" type="danger" size="small" effect="dark">总计 {{ data.issueCount
                          }}</el-tag><el-tag v-for="([category, count]) in issueTypeEntries(data.issueCounts)"
                          :key="category" class="file-issue-tag" size="small" effect="plain"
                          :style="{ color: CATEGORY_META[category]?.color ?? '#7b8da8', borderColor: `${CATEGORY_META[category]?.color ?? '#7b8da8'}66`, background: `${CATEGORY_META[category]?.color ?? '#7b8da8'}0d` }">{{
                            CATEGORY_META[category]?.label ?? category }} {{ count }}</el-tag></span>
                      <template v-if="data.file">
                        <span class="line-stat additions">+{{ data.file.additions }}</span>
                        <span class="line-stat deletions">-{{ data.file.deletions }}</span>
                        <span class="file-authors">
                          <template v-for="author in data.file.authors" :key="author.id || author.name">
                            <a v-if="author.web_url" :href="author.web_url" target="_blank"
                              :title="author.name"><el-avatar :size="23" :src="author.avatar_url">{{
                                authorInitial(author) }}</el-avatar></a>
                            <span v-else :title="author.name"><el-avatar :size="23" :src="author.avatar_url">{{
                              authorInitial(author) }}</el-avatar></span>
                          </template>
                        </span>
                      </template>
                    </div>
                  </template>
                </el-tree>
                <div v-else-if="!filesLoading[mrKey(scope.row)]" class="empty-files">本次提交没有可展示的文件变更</div>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="MR 标题" min-width="155">
            <template #default="scope">
              <div class="mr-title"><b>!{{ scope.row.mr_iid }}</b><span>{{ scope.row.title }}</span></div>
            </template>
          </el-table-column>
          <el-table-column label="分支关系" min-width="240">
            <template #default="scope">
              <div class="branch-flow">
                <code>{{ scope.row.source_branch }}</code><span>→</span><code>{{ scope.row.target_branch }}</code>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="96">
            <template #default="scope"><el-tag :type="mrStateType(scope.row.state)" effect="light">{{ mrStateLabel(scope.row.state) }}</el-tag></template>
          </el-table-column>
          <el-table-column label="问题条状图" min-width="170">
            <template #default="scope">
              <div v-if="issueTotal(scope.row)" class="issue-cell">
                <div class="issue-bar" :title="`共 ${issueTotal(scope.row)} 个问题`">
                  <span v-for="segment in issueSegments(scope.row)" :key="segment.category"
                    :style="{ width: `${segment.percent}%`, background: segment.color }"
                    :title="`${segment.label}: ${segment.count}`" />
                </div>
                <div class="issue-legend"><span v-for="segment in issueSegments(scope.row)" :key="segment.category"><i
                      :style="{ background: segment.color }" />{{ segment.label }} {{ segment.count }}</span></div>
              </div>
              <span v-else class="no-issues">{{ scope.row.reviewed ? '无问题' : '未审查' }}</span>
            </template>
          </el-table-column>
          <el-table-column label="审查" width="92">
            <template #default="scope"><el-tag v-if="scope.row.reviewed" :type="blockingTotal(scope.row) ? 'danger' : 'success'" effect="light">{{ blockingTotal(scope.row) ? `阻断 ${blockingTotal(scope.row)}` : '已审查' }}</el-tag><el-tag v-else type="info" effect="plain">未审查</el-tag></template>
          </el-table-column>
          <el-table-column label="代码提交人" width="105">
            <template #default="scope">
              <a v-if="scope.row.author?.web_url" class="author-link" :href="scope.row.author.web_url" target="_blank"
                :title="scope.row.author.name">
                <el-avatar :size="30" :src="scope.row.author.avatar_url">{{ authorInitial(scope.row.author)
                  }}</el-avatar><span>{{ scope.row.author.name }}</span>
              </a>
              <span v-else class="author-link"><el-avatar :size="30" :src="scope.row.author?.avatar_url">{{
                authorInitial(scope.row.author) }}</el-avatar><span>{{ scope.row.author?.name || '未知' }}</span></span>
            </template>
          </el-table-column>
          <el-table-column label="MR 更新时间" width="135"><template #default="scope">{{ formatDate(scope.row.updated_at)
              }}</template></el-table-column>
          <el-table-column label="操作" width="300" fixed="right">
            <template #default="scope">
              <div class="actions"><el-button v-if="scope.row.reviewed" type="warning" link
                  @click="openChangeAnalysis(scope.row)">查看变更情况</el-button><el-button v-if="scope.row.reviewed" type="success" link
                  @click="openFixTrend(scope.row)">修复趋势</el-button><el-button v-if="scope.row.report_url" type="primary"
                  link tag="a" :href="scope.row.report_url" target="_blank">OCR 报告</el-button><el-button type="primary"
                  link tag="a" :href="scope.row.web_url" target="_blank">打开 MR</el-button></div>
            </template>
          </el-table-column>
        </el-table>
        <el-empty v-if="selectedProject && !mergeRequests.isLoading.value && !selectedMergeRequests.length" description="没有符合当前条件的 MR" />
      </div>
    </el-card>
  </div>
  <el-dialog v-model="trendVisible" :title="`!${trendMR?.mr_iid ?? ''} 修复趋势`" width="780px" destroy-on-close>
    <div class="trend-dialog-body" v-loading="trendLoading">
      <div v-if="trendError" class="file-error">修复趋势加载失败：{{ trendError }}</div>
      <v-chart v-else-if="trendPoints.length" class="fix-trend-chart" :option="trendChart" autoresize />
      <el-empty v-else-if="!trendLoading" description="暂无历史审查数据" />
    </div>
  </el-dialog>
  <el-dialog v-model="changeVisible" :title="`!${changeMR?.mr_iid ?? ''} 变更情况`" width="780px" destroy-on-close>
    <div class="change-analysis-dialog">
      <template v-if="changeAnalysisSections.length">
        <section v-for="section in changeAnalysisSections" :key="section.title" class="change-analysis-section">
          <h3>{{ section.title }}</h3>
          <pre>{{ section.content }}</pre>
        </section>
      </template>
      <el-empty v-else description="本次审查尚未生成变更影响结论" />
    </div>
  </el-dialog>
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
}

.page-heading p {
  margin: 0;
  color: #9499ad;
  font-size: 13px;
}

.quality-layout {
  display: grid;
  grid-template-columns: clamp(320px, 24vw, 420px) minmax(0, 1fr);
  gap: 18px;
  align-items: start;
}

.project-card,
.mr-card {
  border: 1px solid #ebedf4;
  border-radius: 12px;
}

.project-card :deep(.el-card__body) {
  max-height: calc(100vh - 205px);
  padding: 10px 12px;
  overflow: auto;
  scrollbar-gutter: stable;
}

.card-title,
.mr-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
}

.mr-header h2 {
  margin: 0 0 5px;
  font-size: 17px;
}

.mr-header p {
  margin: 0;
  color: #969bad;
  font-size: 11px;
}

.mr-header a {
  color: #6258d8;
  font-size: 12px;
  text-decoration: none;
}

.empty {
  padding: 28px 0;
  color: #a2a7b8;
  text-align: center;
}

.project-tree {
  min-width: max-content;
  background: transparent;
  --el-tree-node-hover-bg-color: #f5f4ff;
}

.project-tree :deep(.el-tree-node__content) {
  width: max-content;
  min-width: 100%;
  height: 42px;
  margin: 2px 0;
  padding-right: 8px;
  border-radius: 8px;
}

.project-tree :deep(.el-tree-node.is-current > .el-tree-node__content) {
  color: #5146ca;
  background: #eeecff;
}

.project-tree-node,
.file-tree-node {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 8px;
  padding-right: 8px;
}

.project-tree-node {
  min-width: max-content;
}

.tree-label,
.file-name {
  min-width: 0;
  overflow: hidden;
  font-size: 12px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-tree .tree-label {
  overflow: visible;
  text-overflow: clip;
}

.mr-filters {
  padding: 14px 16px;
  overflow-x: auto;
  border-bottom: 1px solid #eceef4;
  white-space: nowrap;
}

.filter-count {
  margin-left: 3px;
  color: inherit;
  opacity: .65;
}

.node-count {
  min-width: 20px;
  margin-left: auto;
  padding: 1px 6px;
  border-radius: 9px;
  color: #8b90a5;
  background: #f0f2f7;
  font-size: 10px;
  text-align: center;
}

.project-avatar {
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

.folder-icon {
  position: relative;
  flex: 0 0 24px;
  width: 24px;
  height: 16px;
  border-radius: 3px;
  background: #dfe5f7;
}

.folder-icon::before {
  position: absolute;
  top: -4px;
  left: 2px;
  width: 10px;
  height: 5px;
  border-radius: 3px 3px 0 0;
  background: #c6d0ee;
  content: '';
}

.quality-table :deep(.el-table__header th) {
  color: #8f94a7;
  font-size: 12px;
  font-weight: 500;
}

.quality-table :deep(.el-table__expanded-cell) {
  padding: 0;
  background: #fafbfe;
}

.mr-title {
  display: flex;
  gap: 7px;
  align-items: flex-start;
}

.mr-title b {
  color: #6258d8;
}

.mr-title span {
  color: #4e546d;
}

.branch-flow {
  display: flex;
  align-items: center;
  gap: 7px;
}

.branch-flow code {
  max-width: 76px;
  overflow: hidden;
  color: #6258d8;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.branch-flow span {
  color: #a4a9ba;
}

.issue-cell {
  min-width: 180px;
}

.issue-bar {
  display: flex;
  width: 100%;
  height: 10px;
  overflow: hidden;
  border-radius: 6px;
  background: #eef0f5;
}

.issue-bar>span {
  min-width: 4px;
  height: 100%;
}

.issue-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 3px 10px;
  margin-top: 6px;
  color: #8f94a6;
  font-size: 9px;
}

.issue-legend span {
  white-space: nowrap;
}

.issue-legend i {
  display: inline-block;
  width: 6px;
  height: 6px;
  margin-right: 3px;
  border-radius: 50%;
}

.no-issues {
  color: #2cad7b;
  font-size: 11px;
}

.author-link {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 7px;
  color: #596078;
  text-decoration: none;
}

.author-link span {
  overflow: hidden;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.actions {
  display: flex;
  gap: 2px;
}

.file-expand {
  min-height: 68px;
  padding: 14px 30px 18px 68px;
}

.file-tree {
  max-width: 900px;
  background: transparent;
}

.file-tree :deep(.el-tree-node__content) {
  height: 34px;
  border-radius: 6px;
}

.file-tree :deep(.el-tree-node__content:hover) {
  background: #f0f2f8;
}

.file-folder {
  transform: scale(.78);
}

.file-dot {
  flex: 0 0 7px;
  width: 7px;
  height: 7px;
  border-radius: 2px;
  background: #8b92aa;
}

.file-name {
  font-weight: 500;
}

.line-stat {
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 10px;
}

.additions {
  margin-left: auto;
  color: #25a76f;
}

.deletions {
  color: #df6570;
}

.file-authors {
  display: flex;
  margin-left: 8px;
}

.file-authors>* {
  margin-left: -5px;
  border: 2px solid #fff;
  border-radius: 50%;
}

.file-error {
  color: #df6570;
  font-size: 12px;
}

.empty-files {
  color: #a1a6b7;
  font-size: 12px;
}

@media (max-width: 1100px) {
  .quality-layout {
    grid-template-columns: 240px minmax(0, 1fr);
  }
}

@media (max-width: 760px) {
  .quality-layout {
    grid-template-columns: 1fr;
  }

  .project-card :deep(.el-card__body) {
    max-height: 280px;
  }

  .file-expand {
    padding-left: 24px;
  }
}


.issue-cell {
  min-width: 0;
}


.actions :deep(.el-button) {
  padding: 4px;
  font-size: 11px;
}

.actions :deep(.el-button + .el-button) {
  margin-left: 2px;
}

.branch-flow {
  align-items: flex-start;
}

.branch-flow code {
  max-width: none;
  overflow: visible;
  text-overflow: clip;
  white-space: normal;
  overflow-wrap: anywhere;
}

.file-issue-tag {
  flex: 0 0 auto;
  margin-left: 2px;
  font-size: 10px;
}

.trend-dialog-body {
  min-height: 360px;
}

.fix-trend-chart {
  width: 100%;
  height: 380px;
}

.change-analysis-dialog {
  display: grid;
  gap: 14px;
  max-height: 62vh;
  overflow: auto;
}

.quality-view-tabs {
  display: flex;
  width: max-content;
  gap: 3px;
  margin: 0 2px 18px;
  padding: 4px;
  border-radius: 10px;
  background: #f1f2f7;
}

.quality-view-tabs button {
  height: 34px;
  padding: 0 18px;
  border: 0;
  border-radius: 7px;
  color: #7b8194;
  background: transparent;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: .16s ease;
}

.quality-view-tabs button:hover {
  color: #5b52c8;
}

.quality-view-tabs button.active {
  color: #5b52c8;
  background: #fff;
  box-shadow: 0 3px 10px #353b5e12;
  font-weight: 700;
}

.branch-force-wrap {
  position: relative;
  width: 100%;
  height: clamp(620px, 70vh, 780px);
  overflow: hidden;
  background-color: #fafbfe;
  background-image: radial-gradient(#dfe2ed 1px, transparent 1px);
  background-size: 18px 18px;
}

.branch-force-graph {
  width: 100%;
  height: clamp(620px, 70vh, 780px);
}

.branch-interaction-hint {
  position: absolute;
  z-index: 2;
  top: 12px;
  left: 14px;
  padding: 6px 9px;
  border: 1px solid #e1e4ed;
  border-radius: 7px;
  color: #80879a;
  background: #fffffff0;
  box-shadow: 0 3px 10px #30365212;
  font-size: 9px;
  pointer-events: none;
}

.relation-legend {
  position: absolute;
  z-index: 3;
  right: 14px;
  top: 12px;
  display: flex;
  gap: 8px;
  padding: 5px 7px;
  border: 1px solid #e0e2ed;
  border-radius: 8px;
  color: #737a90;
  background: #fffffff2;
  box-shadow: 0 4px 12px #353b5f12;
  font-size: 9px;
  pointer-events: auto;
  user-select: none;
}

.relation-legend button {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 3px 5px;
  border: 0;
  border-radius: 5px;
  color: #a5a9b7;
  background: transparent;
  font: inherit;
  cursor: pointer;
  opacity: .45;
}

.relation-legend button.active {
  color: #565d72;
  background: #f3f4f8;
  opacity: 1;
}

.relation-legend i {
  display: block;
  width: 24px;
  border-top: 2px dashed;
  pointer-events: none;
}

.relation-legend .mr-relation {
  border-color: hsl(160 42% 52%);
}

.relation-legend .git-relation {
  border-color: #7b61d1;
}

.relation-legend .fork-relation {
  border-color: #e08632;
}

.complexity-legend {
  position: absolute;
  z-index: 3;
  right: 14px;
  bottom: 12px;
  display: flex;
  width: max-content;
  align-items: center;
  gap: 7px;
  padding: 7px 10px;
  border: 1px solid #e0e2ed;
  border-radius: 8px;
  color: #737a90;
  background: #fffffff2;
  box-shadow: 0 4px 12px #353b5f12;
  font-size: 9px;
  pointer-events: none;
}

.legend-gradient {
  display: block;
  width: 72px;
  height: 8px;
  border-radius: 5px;
  background: linear-gradient(90deg, hsl(214 52% 88%), hsl(214 52% 46%));
}

.branch-graph-loading {
  height: 560px;
}

.change-analysis-section pre {
  margin: 0;
  color: #60667c;
  font: inherit;
  font-size: 12px;
  line-height: 1.7;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.branch-graph-section {
  margin: 2px 2px 20px;
  overflow: hidden;
  border: 1px solid #e8eaf2;
  border-radius: 12px;
  background: linear-gradient(145deg, #fcfcff, #f7f9fe);
}

.branch-graph-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 18px;
  padding: 15px 18px 11px;
  border-bottom: 1px solid #eceef4;
}

.branch-graph-header strong {
  color: #414861;
  font-size: 14px;
}

.branch-graph-header p {
  margin: 4px 0 0;
  color: #969cae;
  font-size: 10px;
}

@media (max-width: 760px) {
  .branch-graph-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .file-issue-tags {
    flex-basis: 100%;
    padding-left: 39px;
  }
}

.branch-graph-stats span {
  padding: 5px 9px;
  border-radius: 7px;
  color: #655dc3;
  background: #eeecff;
  font-size: 10px;
  white-space: nowrap;
}

.file-tree :deep(.el-tree-node__content) {
  min-height: 42px;
  height: auto;
  padding: 5px 0;
}

.file-tree-node {
  flex-wrap: wrap;
  row-gap: 5px;
}

.file-issue-tags {
  display: flex;
  min-width: 0;
  flex: 1 1 300px;
  flex-wrap: wrap;
  gap: 4px;
}

.file-issue-tag {
  margin-left: 0;
  font-size: 9px;
}

.file-issue-tag.total {
  border-color: #df5968;
}
</style>
