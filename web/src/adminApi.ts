export type Dashboard = {
  queued: number
  running: number
  retry_wait: number
  publishing: number
  failed: number
  failed_infra: number
  passed: number
  stale: number
  canceled: number
  today_tokens: number
  month_tokens: number
}

export type Coverage = {
  selected: number
  completed: number
  reused: number
  failed: number
  waived: number
  complete: boolean
  selected_files?: string[]
  completed_files?: string[]
  reused_files?: string[]
  failed_files?: string[]
  waived_files?: string[]
  affected_files?: string[]
}

export type FindingCounts = { total: number; blocking: number; new: number; unfixed: number; fixed: number }

export type Review = {
  id: number
  project_id: number
  project_name?: string
  project_path?: string
  project_web_url?: string
  mr_iid: number
  title: string
  web_url: string
  source_branch: string
  target_branch: string
  head_sha: string
  target_sha: string
  base_sha: string
  rule_sha256?: string
  state: string
  stage: string
  priority: number
  attempt: number
  failure_reason?: string
  progress_completed: number
  progress_total: number
  queued_at: string
  started_at?: string
  finished_at?: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  comments: number
  tool_calls: number
  llm_provider?: string
  llm_model?: string
  session_id?: string
  coverage: Coverage
  findings: FindingCounts
  report_url?: string
}

export type ReviewPage = { items: Review[]; page: number; page_size: number; total: number; has_next: boolean }
export type ReviewEvent = { id: number; review_job_id: number; event_type: string; stage?: string; safe_message?: string; completed?: number; total?: number; input_tokens?: number; output_tokens?: number; total_tokens?: number; created_at: string }
export type Comment = { path: string; content: string; suggestion_code?: string; existing_code?: string; start_line: number; end_line: number; category?: string; severity?: string; status: 'current' | 'new' | 'unfixed' | 'fixed' }
export type ReviewDetail = { review: Review; rule: { path: string; status: string; sha256?: string }; session: { id?: string; resumed: boolean; resumed_from?: string }; llm: { provider?: string; model?: string }; coverage: Coverage; findings: Comment[]; publication: { state: string; comments: number; report_url?: string }; revisions: Review[] }
export type UsageSummary = { input_tokens: number; output_tokens: number; total_tokens: number; comments: number; tool_calls: number; review_count: number; failed_reviews: number; retried_reviews: number; stale_reviews: number }
export type UsageTrendPoint = { date: string; input_tokens: number; output_tokens: number; total_tokens: number; review_count: number }
export type AnalyticsSummary = { project_count: number; updated_projects: number; unavailable_projects: number; commit_count: number; contributor_count: number; review_count: number; passed_reviews: number; failed_reviews: number; finding_count: number; blocking_findings: number }
export type AnalyticsQuality = { pass_rate: number; severity_counts: Record<string, number>; category_counts: Record<string, number> }
export type ProjectAnalytics = { project_id: number; name: string; path_with_namespace: string; web_url: string; commit_count: number; contributor_count: number; latest_commit_at?: string; review_count: number; passed_reviews: number; failed_reviews: number; pass_rate: number; finding_count: number; blocking_findings: number; severity_counts: Record<string, number>; category_counts: Record<string, number>; commit_data_available: boolean; commit_data_error?: string }
export type ContributorAnalytics = { user_id?: number; username?: string; name: string; email?: string; avatar_url?: string; web_url?: string; identity_source: 'gitlab_user' | 'commit'; added_lines: number; deleted_lines: number; changed_lines: number; project_count: number; projects: string[]; latest_commit_at?: string }
export type AnalyticsGroup = { path: string; name: string; project_count: number }
export type AnalyticsReport = { from: string; to: string; groups: AnalyticsGroup[]; summary: AnalyticsSummary; quality: AnalyticsQuality; projects: ProjectAnalytics[]; contributors: ContributorAnalytics[] }

export const getJSON = async <T>(url: string, init?: RequestInit): Promise<T> => {
  const headers = new Headers(init?.headers)
  if (init?.method && init.method !== 'GET' && init.method !== 'HEAD') headers.set('X-Requested-With', 'XMLHttpRequest')
  const response = await fetch(url, { credentials: 'same-origin', ...init, headers })
  if (!response.ok) {
    const body = await response.text()
    throw new Error(body || `${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

export const reviewStateLabel = (state: string) => ({
  queued: '排队中', retry_wait: '等待重试', running: '审查中', publishing: '发布中',
  completed_pass: '通过', completed_fail: '未通过', failed_infra: '基础设施失败', stale: '已过期',
  canceled: '已取消', rejected_rule_missing: '规则缺失', rejected_rule_invalid: '规则无效',
}[state] ?? state)

export const reviewStateType = (state: string) => ({
  completed_pass: 'success', completed_fail: 'danger', failed_infra: 'danger', rejected_rule_invalid: 'warning',
  rejected_rule_missing: 'warning', retry_wait: 'warning', running: 'primary', publishing: 'primary', stale: 'info', canceled: 'info',
}[state] ?? '') as '' | 'success' | 'warning' | 'danger' | 'info' | 'primary'

export const stageLabel = (stage: string) => ({
  queued: '排队中', rule_preflight: '规则检查', git_prepare: '准备代码', code_graph: '更新代码图',
  ocr_review: 'OCR 审查', publishing: '发布结果', finished: '已完成',
}[stage] ?? stage)

export const formatTokens = (value: number) => {
  const absolute = Math.abs(value)
  if (absolute < 1000) return value.toLocaleString('zh-CN')
  if (absolute < 1_000_000) return `${(value / 1_000).toFixed(absolute < 10_000 ? 1 : 0)}K`
  if (absolute < 1_000_000_000) return `${(value / 1_000_000).toFixed(absolute < 10_000_000 ? 1 : 0)}M`
  return `${(value / 1_000_000_000).toFixed(absolute < 10_000_000_000 ? 1 : 0)}B`
}

export const formatDate = (value?: string | null) => value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—'
export const progressPercent = (review: Review) => review.progress_total > 0 ? Math.min(100, Math.round(review.progress_completed / review.progress_total * 100)) : ['completed_pass', 'completed_fail', 'failed_infra', 'canceled'].includes(review.state) ? 100 : 0
