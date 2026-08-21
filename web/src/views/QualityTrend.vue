<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { useRoute, useRouter } from 'vue-router'
import { buildProjectTree, type ProjectTreeNode } from '../projectTree'
import DOMPurify from 'dompurify'
import { marked } from 'marked'
import hljs from 'highlight.js/lib/common'
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
  file_blocking_counts: Record<string, number>
  change_analysis?: string
  report_url?: string
}
type QualityProject = {
  id: number
  name: string
  description: string
  path_with_namespace: string
  web_url: string
  tech_stack: string
}
type BranchRelationKind = 'mr' | 'git' | 'fork'
type QualityBranchGraph = { project_id: number; branches: Array<{ name: string; changed_files: number; open_merge_requests: number }>; relations: Array<{ source: string; target: string; kind: BranchRelationKind; mr_iid: number; title: string; state: string; web_url?: string; changed_files: number }> }
type QualityFile = { path: string; old_path?: string; additions: number; deletions: number; authors: Author[] }
type FileTreeNode = { key: string; label: string; kind: 'directory' | 'file'; file?: QualityFile; issueCount?: number; blockingCount?: number; issueCounts?: Record<string, number>; children?: FileTreeNode[] }
type IssueSegment = { category: string; label: string; count: number; percent: number; color: string }
type FixTrendPoint = { time: string; issue_count: number; fixed_count: number }
type QualityFileFinding = { content: string; suggestion_code?: string; existing_code?: string; start_line: number; end_line: number; category?: string; severity?: string; status: string }
type QualityFileDetail = { path: string; ref: string; content: string; findings: QualityFileFinding[] }
type ProjectTreeRef = { filter: (value: string) => void }
type HighlightedCodeLine = { number: number; html: string }

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

const route = useRoute()
const router = useRouter()
const positiveQueryInt = (value: unknown) => {
  const raw = Array.isArray(value) ? value[0] : value
  const parsed = Number.parseInt(typeof raw === 'string' ? raw : '', 10)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null
}
const replaceQualityRoute = (projectID: number, mrIID?: number | null) => {
  const query: Record<string, string> = { project_id: String(projectID) }
  if (mrIID && mrIID > 0) query.mr_iid = String(mrIID)
  void router.replace({ name: 'quality', query })
}

const projects = useQuery({
  queryKey: ['quality-projects'],
  queryFn: () => getJSON<QualityProject[]>('/api/v1/admin/quality/projects'),
})
const selectedProjectId = ref<number | null>(positiveQueryInt(route.query.project_id))
const qualityView = ref<'branches' | 'mrs'>('mrs')
type MRFilter = 'all' | 'opened' | 'merged' | 'closed' | 'unreviewed' | 'reviewed' | 'unreviewed_merged' | 'reviewed_merged'
const mrFilter = ref<MRFilter>('all')
const expandedMRId = ref<number | null>(positiveQueryInt(route.query.mr_iid))
const expandedMRKeys = computed(() => expandedMRId.value ? [expandedMRId.value] : [])
const projectTree = computed(() => buildProjectTree(projects.data.value ?? []))
const projectSearch = ref('')
const projectTreeRef = ref<ProjectTreeRef | null>(null)
const filterProjectNode = (query: string, node: ProjectTreeNode<QualityProject>) => {
  const keyword = query.trim().toLocaleLowerCase('zh-CN')
  if (!keyword) return true
  if (node.kind === 'project') {
    return [node.label, node.project?.path_with_namespace, node.project?.tech_stack]
      .some(value => value?.toLocaleLowerCase('zh-CN').includes(keyword))
  }
  const containsProject = (items?: ProjectTreeNode<QualityProject>[]): boolean => (items ?? []).some(item =>
    item.kind === 'project'
      ? [item.label, item.project?.path_with_namespace, item.project?.tech_stack].some(value => value?.toLocaleLowerCase('zh-CN').includes(keyword))
      : containsProject(item.children))
  return node.label.toLocaleLowerCase('zh-CN').includes(keyword) || containsProject(node.children)
}
watch(projectSearch, async value => {
  await nextTick()
  projectTreeRef.value?.filter(value)
})
watch(() => route.query.project_id, value => {
  const projectID = positiveQueryInt(value)
  if (projectID !== null) selectedProjectId.value = projectID
})
watch(() => route.query.mr_iid, value => {
  expandedMRId.value = positiveQueryInt(value)
})
const selectedProject = computed(() => {
  const list = projects.data.value ?? []
  if (selectedProjectId.value === null) return list[0]
  return list.find(project => project.id === selectedProjectId.value) ?? list[0]
})
const selectProject = (node: ProjectTreeNode<QualityProject>) => {
  if (!node.project) return
  selectedProjectId.value = node.project.id
  expandedMRId.value = null
  replaceQualityRoute(node.project.id)
}
const selectedProjectKey = computed(() => selectedProject.value?.id ?? 0)
const mergeRequests = useQuery({
  queryKey: ['quality-merge-requests', selectedProjectKey],
  queryFn: () => getJSON<QualityMR[]>(`/api/v1/admin/quality/projects/${selectedProjectKey.value}/mrs`),
  enabled: computed(() => selectedProjectKey.value > 0),
  staleTime: 60 * 1000,
})
const matchesMRFilter = (mr: QualityMR, filter: MRFilter) => {
  if (filter === 'all') return true
  if (filter === 'reviewed') return mr.reviewed
  if (filter === 'unreviewed') return !mr.reviewed
  if (filter === 'reviewed_merged') return mr.state === 'merged' && mr.reviewed
  if (filter === 'unreviewed_merged') return mr.state === 'merged' && !mr.reviewed
  return mr.state === filter
}
const selectedMergeRequests = computed(() => {
  const rows = mergeRequests.data.value ?? []
  return rows.filter(mr => matchesMRFilter(mr, mrFilter.value))
})
const mrFilterCount = (filter: MRFilter) => (mergeRequests.data.value ?? []).filter(mr => matchesMRFilter(mr, filter)).length
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
const ensureMRFiles = async (mr: QualityMR) => {
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
const handleMRExpand = (mr: QualityMR, expandedRows: QualityMR[]) => {
  const expanded = expandedRows.some(item => mrKey(item) === mrKey(mr))
  if (expanded) {
    expandedMRId.value = mr.mr_iid
    replaceQualityRoute(mr.project_id, mr.mr_iid)
    void ensureMRFiles(mr)
  } else if (expandedMRId.value === mr.mr_iid) {
    expandedMRId.value = null
    replaceQualityRoute(mr.project_id)
  }
}
watch([() => mergeRequests.data.value, () => route.query.mr_iid], async ([rows, queryMRIID]) => {
  const mrIID = positiveQueryInt(queryMRIID)
  if (mrIID === null) {
    expandedMRId.value = null
    return
  }
  const target = rows?.find(mr => mr.mr_iid === mrIID && mr.project_id === selectedProjectKey.value)
  if (!target) return
  if (target.state === 'opened' || target.state === 'merged' || target.state === 'closed') {
    mrFilter.value = target.state
  } else {
    mrFilter.value = target.reviewed ? 'reviewed' : 'unreviewed'
  }
  expandedMRId.value = target.mr_iid
  await ensureMRFiles(target)
}, { immediate: true })
const fileDetailVisible = ref(false)
const fileDetail = ref<QualityFileDetail | null>(null)
const fileDetailLoading = ref(false)
const fileDetailError = ref('')
const LANGUAGE_BY_EXTENSION: Record<string, string> = {
  c: 'c', cc: 'cpp', cpp: 'cpp', cs: 'csharp', css: 'css', dart: 'dart', go: 'go',
  graphql: 'graphql', gql: 'graphql', h: 'c', hpp: 'cpp', htm: 'xml', html: 'xml', java: 'java',
  js: 'javascript', jsx: 'javascript', json: 'json', kt: 'kotlin', kts: 'kotlin', less: 'less',
  lua: 'lua', md: 'markdown', php: 'php', pl: 'perl', proto: 'protobuf', ps1: 'powershell',
  py: 'python', rb: 'ruby', rs: 'rust', scala: 'scala', scss: 'scss', sh: 'bash', sql: 'sql',
  swift: 'swift', ts: 'typescript', tsx: 'typescript', vue: 'xml', xml: 'xml', yaml: 'yaml', yml: 'yaml',
}
const languageForPath = (path: string) => {
  const fileName = path.split('/').pop()?.toLowerCase() ?? ''
  if (fileName === 'dockerfile' || fileName.startsWith('dockerfile.')) return 'dockerfile'
  if (fileName === 'makefile' || fileName === 'gnumakefile') return 'makefile'
  if (fileName === 'nginx.conf') return 'nginx'
  const extension = fileName.includes('.') ? fileName.slice(fileName.lastIndexOf('.') + 1) : ''
  return LANGUAGE_BY_EXTENSION[extension] ?? 'plaintext'
}
const languageLabel = (language: string) => ({
  bash: 'Shell', cpp: 'C++', csharp: 'C#', javascript: 'JavaScript', markdown: 'Markdown',
  plaintext: 'Text', powershell: 'PowerShell', protobuf: 'Protocol Buffers', typescript: 'TypeScript',
  xml: 'HTML / XML', yaml: 'YAML',
}[language] ?? language.toUpperCase())
const highlightedValue = (code: string, language: string) => {
  if (!code) return '&nbsp;'
  const resolved = hljs.getLanguage(language) ? language : 'plaintext'
  return DOMPurify.sanitize(hljs.highlight(code, { language: resolved, ignoreIllegals: true }).value)
}
const fileDetailLanguage = computed(() => languageForPath(fileDetail.value?.path ?? ''))
const fileDetailLanguageLabel = computed(() => languageLabel(fileDetailLanguage.value))
const highlightedFileLines = computed<HighlightedCodeLine[]>(() => {
  const detail = fileDetail.value
  if (!detail) return []
  const baseLanguage = languageForPath(detail.path)
  const isMarkupContainer = /\.(vue|html?|xml)$/i.test(detail.path)
  let embeddedLanguage = baseLanguage
  return detail.content.split('\n').map((line, index) => {
    const trimmed = line.trim().toLowerCase()
    let lineLanguage = embeddedLanguage
    if (isMarkupContainer && embeddedLanguage !== baseLanguage && /^<\/(script|style)>/.test(trimmed)) {
      embeddedLanguage = baseLanguage
      lineLanguage = baseLanguage
    }
    const highlighted = { number: index + 1, html: highlightedValue(line, lineLanguage) }
    if (isMarkupContainer && embeddedLanguage === baseLanguage) {
      if (/^<script\b/.test(trimmed) && !/<\/script>\s*$/.test(trimmed)) {
        embeddedLanguage = /lang=["']ts["']/.test(trimmed) ? 'typescript' : 'javascript'
      } else if (/^<style\b/.test(trimmed) && !/<\/style>\s*$/.test(trimmed)) {
        embeddedLanguage = /lang=["']scss["']/.test(trimmed) ? 'scss' : /lang=["']less["']/.test(trimmed) ? 'less' : 'css'
      }
    }
    return highlighted
  })
})
const highlightedSuggestion = (code: string) => highlightedValue(code, fileDetailLanguage.value)
const codeViewerRef = ref<HTMLElement | null>(null)
const codeViewportTop = ref(0)
const codeViewportHeight = ref(100)
const minimapSeeking = ref(false)
const fileDetailRawLines = computed(() => (fileDetail.value?.content ?? '').split('\n'))
const minimapPreviewLines = computed(() => {
  const lines = fileDetailRawLines.value
  const step = Math.max(1, Math.ceil(lines.length / 140))
  return lines.flatMap((line, index) => index % step === 0 ? [{
    number: index + 1,
    top: lines.length <= 1 ? 0 : index / (lines.length - 1) * 100,
    width: Math.min(92, Math.max(10, line.trim().length * 1.6)),
  }] : [])
})
const fileFindingMarkers = computed(() => {
  const lineCount = Math.max(1, fileDetailRawLines.value.length)
  return (fileDetail.value?.findings ?? []).map((finding, index) => ({
    key: `${finding.start_line}:${finding.end_line}:${index}`,
    line: Math.max(1, finding.start_line || 1),
    top: lineCount <= 1 ? 0 : (Math.max(1, finding.start_line || 1) - 1) / (lineCount - 1) * 100,
    label: `${findingLocation(finding)} ${finding.content}`,
  }))
})
const syncCodeViewport = () => {
  const viewer = codeViewerRef.value
  if (!viewer) return
  const scrollable = Math.max(1, viewer.scrollHeight)
  codeViewportTop.value = viewer.scrollTop / scrollable * 100
  codeViewportHeight.value = Math.min(100, viewer.clientHeight / scrollable * 100)
}
const jumpToFileLine = async (line: number) => {
  await nextTick()
  const row = codeViewerRef.value?.querySelector<HTMLElement>(`[data-code-line="${line}"]`)
  row?.scrollIntoView({ behavior: 'smooth', block: 'center' })
}
const seekFromMinimap = (event: PointerEvent) => {
  const viewer = codeViewerRef.value
  const track = event.currentTarget as HTMLElement
  if (!viewer || !track) return
  const rect = track.getBoundingClientRect()
  const ratio = Math.min(1, Math.max(0, (event.clientY - rect.top) / Math.max(1, rect.height)))
  viewer.scrollTop = ratio * Math.max(0, viewer.scrollHeight - viewer.clientHeight)
  syncCodeViewport()
}
const startMinimapSeek = (event: PointerEvent) => {
  minimapSeeking.value = true
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
  seekFromMinimap(event)
}
const moveMinimapSeek = (event: PointerEvent) => {
  if (minimapSeeking.value) seekFromMinimap(event)
}
const stopMinimapSeek = () => { minimapSeeking.value = false }
watch(fileDetail, async () => {
  await nextTick()
  syncCodeViewport()
})
const findingsAtLine = (line: number) => (fileDetail.value?.findings ?? []).filter(finding => {
  const start = Math.max(1, finding.start_line || 1)
  const end = Math.max(start, finding.end_line || start)
  return line >= start && line <= end
})
const openFileDetail = async (mr: QualityMR, node: FileTreeNode) => {
  if (!node.file) return
  fileDetailVisible.value = true
  fileDetail.value = null
  fileDetailLoading.value = true
  fileDetailError.value = ''
  try {
    fileDetail.value = await getJSON<QualityFileDetail>(`/api/v1/admin/quality/projects/${mr.project_id}/mrs/${mr.mr_iid}/file?path=${encodeURIComponent(node.file.path)}`)
  } catch (error) {
    fileDetailError.value = error instanceof Error ? error.message : String(error)
  } finally {
    fileDetailLoading.value = false
  }
}
const findingLocation = (finding: QualityFileFinding) => finding.end_line && finding.end_line !== finding.start_line
  ? `L${finding.start_line}–L${finding.end_line}`
  : `L${finding.start_line}`
const severityType = (severity?: string) => ['critical', 'high'].includes(severity ?? '') ? 'danger' : severity === 'medium' ? 'warning' : 'info'
const issueTypeEntries = (counts?: Record<string, number>) => Object.entries(counts ?? {}).filter(([, count]) => count > 0).sort((left, right) => right[1] - left[1])

const buildFileTree = (files: QualityFile[], issueCounts: Record<string, number>, issueTypeCounts: Record<string, Record<string, number>>, blockingCounts: Record<string, number>): FileTreeNode[] => {
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
    children.push({ key: `file:${file.path}`, label: segments[segments.length - 1] || file.path, kind: 'file', file, issueCount: issueCounts[file.path] ?? 0, blockingCount: blockingCounts[file.path] ?? 0, issueCounts: issueTypeCounts[file.path] ?? {} })
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
const renderedChangeAnalysis = computed(() => {
  const source = changeMR.value?.change_analysis?.trim() ?? ''
  return source ? DOMPurify.sanitize(marked.parse(source, { async: false }) as string) : ''
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
        <div class="project-card-header">
          <div class="card-title"><span>GitLab 项目</span><el-badge :value="projects.data.value?.length ?? 0" type="primary" /></div>
          <el-input v-model="projectSearch" class="project-search" size="small" clearable placeholder="快速搜索项目或技术栈" aria-label="快速搜索项目" />
        </div>
      </template>
      <div v-if="projects.isLoading.value" class="empty">加载项目中…</div>
      <div v-else-if="!(projects.data.value?.length)" class="empty">暂无分析数据</div>
      <el-tree v-else ref="projectTreeRef" class="project-tree" :data="projectTree" node-key="key" :indent="12"
        :current-node-key="selectedProject ? `project:${selectedProject.id}` : undefined" :expand-on-click-node="false"
        :filter-node-method="filterProjectNode" highlight-current @node-click="selectProject">
        <template #default="{ data }">
          <div class="project-tree-node" :title="data.project ? [data.project.path_with_namespace, data.project.tech_stack].filter(Boolean).join(' · ') : data.label">
            <span v-if="data.kind === 'group'" class="folder-icon" />
            <span v-else class="project-avatar">{{ data.label.slice(0, 1).toUpperCase() }}</span>
            <span class="tree-label">{{ data.label }}</span>
            <el-tag v-if="data.kind === 'project' && data.project?.tech_stack" class="tech-stack-tag" size="small" effect="plain">{{ data.project.tech_stack }}</el-tag>
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
            <el-radio-button value="all">全部 <span class="filter-count">{{ mrFilterCount('all') }}</span></el-radio-button>
            <el-radio-button value="opened">待合并 <span class="filter-count">{{ mrFilterCount('opened') }}</span></el-radio-button>
            <el-radio-button value="merged">已合并 <span class="filter-count">{{ mrFilterCount('merged') }}</span></el-radio-button>
            <el-radio-button value="closed">已取消 <span class="filter-count">{{ mrFilterCount('closed') }}</span></el-radio-button>
            <el-radio-button value="unreviewed_merged">未审查已合并 <span class="filter-count">{{ mrFilterCount('unreviewed_merged') }}</span></el-radio-button>
            <el-radio-button value="reviewed_merged">已审查已合并 <span class="filter-count">{{ mrFilterCount('reviewed_merged') }}</span></el-radio-button>
            <el-radio-button value="unreviewed">未审查 <span class="filter-count">{{ mrFilterCount('unreviewed') }}</span></el-radio-button>
            <el-radio-button value="reviewed">已审查 <span class="filter-count">{{ mrFilterCount('reviewed') }}</span></el-radio-button>
          </el-radio-group>
        </div>
        <el-table :data="selectedMergeRequests" row-key="mr_iid" :expand-row-keys="expandedMRKeys" v-loading="mergeRequests.isLoading.value"
          class="quality-table" @expand-change="handleMRExpand">
          <el-table-column type="expand" width="48">
            <template #default="scope">
              <div class="file-expand" v-loading="filesLoading[mrKey(scope.row)]">
                <div v-if="filesError[mrKey(scope.row)]" class="file-error">文件变更加载失败：{{ filesError[mrKey(scope.row)] }}
                </div>
                <el-tree v-else-if="filesByMR[mrKey(scope.row)]?.length" class="file-tree"
                  :data="buildFileTree(filesByMR[mrKey(scope.row)], scope.row.file_issue_counts ?? {}, scope.row.file_issue_type_counts ?? {}, scope.row.file_blocking_counts ?? {})"
                  node-key="key" :indent="22" default-expand-all :expand-on-click-node="false" @node-click="(data: FileTreeNode) => openFileDetail(scope.row, data)">
                  <template #default="{ data }">
                    <div class="file-tree-node" :class="{ clickable: data.kind === 'file' }" :title="data.file?.path || data.label">
                      <span v-if="data.kind === 'directory'" class="folder-icon file-folder" />
                      <span v-else class="file-dot" />
                      <span class="file-name">{{ data.label }}</span>
                      <span v-if="data.kind === 'file' && data.issueCount" class="file-issue-tags">
                        <el-tag v-if="data.blockingCount" class="file-issue-tag blocking" type="danger" size="small" effect="dark">阻断 {{ data.blockingCount }}</el-tag>
                        <el-tag class="file-issue-tag total" type="danger" size="small" effect="plain">总计 {{ data.issueCount }}</el-tag>
                        <el-tag v-for="([category, count]) in issueTypeEntries(data.issueCounts)" :key="category"
                          class="file-issue-tag" size="small" effect="plain"
                          :style="{ color: CATEGORY_META[category]?.color ?? '#7b8da8', borderColor: `${CATEGORY_META[category]?.color ?? '#7b8da8'}66`, background: `${CATEGORY_META[category]?.color ?? '#7b8da8'}0d` }">{{ CATEGORY_META[category]?.label ?? category }} {{ count }}</el-tag>
                      </span>
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
  <el-dialog v-model="fileDetailVisible" :title="fileDetail?.path || '文件代码与问题'" width="min(1120px, 94vw)" destroy-on-close>
    <div class="file-detail-dialog" v-loading="fileDetailLoading">
      <div v-if="fileDetailError" class="file-error">文件详情加载失败：{{ fileDetailError }}</div>
      <template v-else-if="fileDetail">
        <div class="file-detail-summary">
          <div class="file-detail-meta"><el-tag size="small" effect="plain">{{ fileDetailLanguageLabel }}</el-tag><span>版本 {{ fileDetail.ref.slice(0, 12) }}</span></div>
          <el-tag :type="fileDetail.findings.length ? 'danger' : 'success'" effect="light">{{ fileDetail.findings.length ? `${fileDetail.findings.length} 个问题` : '无问题' }}</el-tag>
        </div>
        <div class="code-workspace">
          <div ref="codeViewerRef" class="code-viewer" @scroll="syncCodeViewport">
            <div v-for="line in highlightedFileLines" :key="line.number" class="code-row"
              :class="{ 'has-finding': findingsAtLine(line.number).length }" :data-code-line="line.number">
              <span class="code-line-number">{{ line.number }}</span><code class="syntax-code hljs" v-html="line.html" />
              <div v-for="finding in findingsAtLine(line.number).filter(item => Math.max(1, item.start_line || 1) === line.number)"
                :key="`${findingLocation(finding)}:${finding.content}`" class="inline-finding">
                <div class="finding-heading"><el-tag :type="severityType(finding.severity)" size="small">{{ finding.severity || '未标注' }}</el-tag><b>{{ CATEGORY_META[finding.category || '']?.label ?? finding.category ?? '问题' }}</b><span>{{ findingLocation(finding) }}</span></div>
                <p>{{ finding.content }}</p>
                <div v-if="finding.suggestion_code" class="fix-suggestion"><strong>修复意见</strong><pre><code class="hljs" v-html="highlightedSuggestion(finding.suggestion_code)" /></pre></div>
              </div>
            </div>
          </div>
          <aside class="code-minimap" aria-label="代码快速导览">
            <strong>快速导览</strong>
            <div class="minimap-track" @pointerdown="startMinimapSeek" @pointermove="moveMinimapSeek"
              @pointerup="stopMinimapSeek" @pointercancel="stopMinimapSeek" @lostpointercapture="stopMinimapSeek">
              <i v-for="line in minimapPreviewLines" :key="line.number" class="minimap-code-line"
                :style="{ top: `${line.top}%`, width: `${line.width}%` }" />
              <span class="minimap-viewport" :style="{ top: `${codeViewportTop}%`, height: `${codeViewportHeight}%` }" />
              <button v-for="marker in fileFindingMarkers" :key="marker.key" type="button" class="minimap-finding"
                :style="{ top: `${marker.top}%` }" :title="marker.label" :aria-label="`跳转到问题 ${marker.label}`"
                @pointerdown.stop @click.stop="jumpToFileLine(marker.line)" />
            </div>
          </aside>
        </div>
      </template>
    </div>
  </el-dialog>
  <el-dialog v-model="trendVisible" :title="`!${trendMR?.mr_iid ?? ''} 修复趋势`" width="780px" destroy-on-close>
    <div class="trend-dialog-body" v-loading="trendLoading">
      <div v-if="trendError" class="file-error">修复趋势加载失败：{{ trendError }}</div>
      <v-chart v-else-if="trendPoints.length" class="fix-trend-chart" :option="trendChart" autoresize />
      <el-empty v-else-if="!trendLoading" description="暂无历史审查数据" />
    </div>
  </el-dialog>
  <el-dialog v-model="changeVisible" :title="`!${changeMR?.mr_iid ?? ''} 变更情况`" width="780px" destroy-on-close>
    <div v-if="renderedChangeAnalysis" class="change-analysis-markdown" v-html="renderedChangeAnalysis" />
    <el-empty v-else description="本次审查尚未生成变更影响结论" />
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

.project-card-header {
  display: grid;
  gap: 12px;
}

.project-search {
  width: 100%;
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

.tech-stack-tag {
  max-width: 100px;
  color: #6258d8;
  border-color: #d9d5ff;
  background: #f5f3ff;
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

.file-tree-node.clickable {
  cursor: pointer;
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

.file-detail-dialog {
  min-height: 240px;
}

.file-detail-summary {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
  color: #858b9e;
  font: 11px ui-monospace, SFMono-Regular, Consolas, monospace;
}

.file-detail-meta {
  display: flex;
  align-items: center;
  gap: 10px;
}

.code-workspace {
  display: grid;
  height: min(68vh, 720px);
  grid-template-columns: minmax(0, 1fr) 112px;
  gap: 8px;
}

.code-viewer {
  min-width: 0;
  overflow: hidden auto;
  border: 1px solid #dfe2e8;
  border-radius: 9px;
  color: #24292f;
  background: #f6f8fa;
  font: 12px/1.65 ui-monospace, SFMono-Regular, Consolas, "Liberation Mono", monospace;
}

.code-row {
  display: grid;
  width: 100%;
  grid-template-columns: 52px minmax(0, 1fr);
  border-bottom: 1px solid #eaeef2;
}

.code-row.has-finding {
  background: #fff7f0;
}

.code-row:hover {
  background: #eef3f8;
}

.code-line-number {
  padding: 0 12px;
  color: #8c959f;
  border-right: 1px solid #d8dee4;
  background: #f0f3f6;
  text-align: right;
  user-select: none;
}

.code-row>.syntax-code {
  min-width: 0;
  padding: 0 14px;
  color: inherit;
  background: transparent;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.inline-finding {
  grid-column: 2;
  min-width: 0;
  margin: 6px 14px 12px;
  padding: 10px 12px;
  border-left: 3px solid #df6570;
  border-radius: 4px 8px 8px 4px;
  background: #fff;
  box-shadow: 0 3px 12px #5f617014;
  font-family: inherit;
  white-space: normal;
}

.finding-heading {
  display: flex;
  align-items: center;
  gap: 8px;
  color: #656b7d;
}

.finding-heading span:last-child {
  margin-left: auto;
  color: #9297a8;
}

.inline-finding p {
  margin: 8px 0 0;
  color: #4f5568;
  line-height: 1.65;
  overflow-wrap: anywhere;
}

.fix-suggestion {
  margin-top: 10px;
  color: #2c8a66;
}

.fix-suggestion pre {
  margin: 6px 0 0;
  padding: 10px 12px;
  overflow: hidden;
  border: 1px solid #d8dee4;
  border-radius: 6px;
  color: #24292f;
  background: #f6f8fa;
}

.fix-suggestion code {
  display: block;
  color: inherit;
  background: transparent;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.code-minimap {
  display: flex;
  min-width: 0;
  flex-direction: column;
  padding: 8px;
  border: 1px solid #dfe2e8;
  border-radius: 9px;
  color: #7d8590;
  background: #f6f8fa;
  font-size: 10px;
}

.code-minimap>strong {
  margin-bottom: 7px;
  font-weight: 600;
  text-align: center;
}

.minimap-track {
  position: relative;
  min-height: 0;
  flex: 1;
  overflow: hidden;
  border-radius: 4px;
  background: #eef1f4;
  cursor: ns-resize;
  touch-action: none;
  user-select: none;
}

.minimap-code-line {
  position: absolute;
  left: 8%;
  display: block;
  height: 1px;
  max-width: 84%;
  background: #9aa4b0;
  opacity: .55;
  pointer-events: none;
}

.minimap-viewport {
  position: absolute;
  z-index: 2;
  right: 2px;
  left: 2px;
  min-height: 12px;
  border: 1px solid #0969da;
  border-radius: 3px;
  background: #54aeff1c;
  pointer-events: none;
}

.minimap-finding {
  position: absolute;
  z-index: 3;
  right: 3px;
  width: 14px;
  height: 5px;
  padding: 0;
  transform: translateY(-50%);
  border: 0;
  border-radius: 3px;
  background: #cf222e;
  box-shadow: 0 0 0 1px #fff;
  cursor: pointer;
}

.minimap-finding:hover,
.minimap-finding:focus-visible {
  width: 22px;
  outline: 2px solid #ff8182;
}

.code-viewer :deep(.hljs-comment),
.fix-suggestion :deep(.hljs-comment),
.code-viewer :deep(.hljs-quote),
.fix-suggestion :deep(.hljs-quote) { color: #6e7781; }

.code-viewer :deep(.hljs-keyword),
.fix-suggestion :deep(.hljs-keyword),
.code-viewer :deep(.hljs-selector-tag),
.fix-suggestion :deep(.hljs-selector-tag),
.code-viewer :deep(.hljs-built_in),
.fix-suggestion :deep(.hljs-built_in) { color: #cf222e; }

.code-viewer :deep(.hljs-title),
.fix-suggestion :deep(.hljs-title),
.code-viewer :deep(.hljs-function),
.fix-suggestion :deep(.hljs-function),
.code-viewer :deep(.hljs-section),
.fix-suggestion :deep(.hljs-section) { color: #8250df; }

.code-viewer :deep(.hljs-string),
.fix-suggestion :deep(.hljs-string),
.code-viewer :deep(.hljs-attribute),
.fix-suggestion :deep(.hljs-attribute),
.code-viewer :deep(.hljs-template-tag),
.fix-suggestion :deep(.hljs-template-tag) { color: #0a3069; }

.code-viewer :deep(.hljs-number),
.fix-suggestion :deep(.hljs-number),
.code-viewer :deep(.hljs-literal),
.fix-suggestion :deep(.hljs-literal),
.code-viewer :deep(.hljs-variable),
.fix-suggestion :deep(.hljs-variable) { color: #0550ae; }

.code-viewer :deep(.hljs-tag),
.fix-suggestion :deep(.hljs-tag),
.code-viewer :deep(.hljs-name),
.fix-suggestion :deep(.hljs-name),
.code-viewer :deep(.hljs-selector-class),
.fix-suggestion :deep(.hljs-selector-class) { color: #116329; }

.change-analysis-markdown {
  color: #555c70;
  font-size: 13px;
  line-height: 1.75;
}

.change-analysis-markdown :deep(h1),
.change-analysis-markdown :deep(h2),
.change-analysis-markdown :deep(h3) {
  margin: 20px 0 8px;
  color: #343a4d;
}

.change-analysis-markdown :deep(h1:first-child),
.change-analysis-markdown :deep(h2:first-child),
.change-analysis-markdown :deep(h3:first-child) {
  margin-top: 0;
}

.change-analysis-markdown :deep(pre) {
  padding: 12px 14px;
  overflow-x: auto;
  border-radius: 7px;
  color: #d7e2ef;
  background: #202532;
}

.change-analysis-markdown :deep(code) {
  padding: 2px 4px;
  border-radius: 4px;
  background: #f0f1f6;
}

.change-analysis-markdown :deep(pre code) {
  padding: 0;
  background: transparent;
}

.change-analysis-markdown :deep(table) {
  width: 100%;
  border-collapse: collapse;
}

.change-analysis-markdown :deep(th),
.change-analysis-markdown :deep(td) {
  padding: 7px 9px;
  border: 1px solid #e1e4ec;
  text-align: left;
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
