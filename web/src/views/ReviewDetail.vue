<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useQuery, useQueryClient } from '@tanstack/vue-query'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getJSON, type ReviewDetail as Detail, type ReviewEvent, type Review, formatDate, formatTokens, progressPercent, reviewStateLabel, reviewStateType, stageLabel } from '../adminApi'
import { subscribeAdminEvents } from '../eventStream'

const route = useRoute()
const router = useRouter()
const queryClient = useQueryClient()
const reviewID = computed(() => String(route.params.id))
const detail = useQuery<Detail>({ queryKey: ['review-detail', reviewID], queryFn: () => getJSON<Detail>(`/api/v1/admin/reviews/${reviewID.value}`) })
const events = useQuery<ReviewEvent[]>({ queryKey: ['review-events', reviewID], queryFn: () => getJSON<ReviewEvent[]>(`/api/v1/admin/reviews/${reviewID.value}/events?limit=100`) })
const revisions = useQuery({ queryKey: ['review-revisions', reviewID], queryFn: () => getJSON<Review[]>(`/api/v1/admin/reviews/${reviewID.value}/revisions`) })
let stopEvents = () => { }
onMounted(() => {
  stopEvents = subscribeAdminEvents(event => {
    if (event.review_job_id !== Number(reviewID.value)) return
    queryClient.setQueryData<ReviewEvent[]>(['review-events', reviewID], current => {
      const filtered = (current ?? []).filter(item => event.id > 0 ? item.id !== event.id : item.event_type !== event.event_type)
      return [event, ...filtered].slice(0, 100)
    })
    if (event.event_type !== 'analysis_log') {
      void queryClient.invalidateQueries({ queryKey: ['review-detail', reviewID] })
      void queryClient.invalidateQueries({ queryKey: ['review-revisions', reviewID] })
    }
  })
})
onUnmounted(() => stopEvents())

const activeTab = ref('overview')
const actionLoading = ref(false)
const review = computed(() => detail.data.value?.review)
const reviewDetail = computed(() => detail.data.value)
const analysisLogs = computed(() => (events.data.value ?? []).filter(event => ['analysis_log', 'stage_started', 'retry_scheduled', 'job_finished', 'job_canceled'].includes(event.event_type)))
const canRetry = computed(() => ['failed_infra', 'completed_fail', 'rejected_rule_missing', 'rejected_rule_invalid', 'canceled'].includes(review.value?.state ?? ''))
const canCancel = computed(() => ['queued', 'running', 'retry_wait', 'publishing'].includes(review.value?.state ?? ''))
const canPrioritize = computed(() => ['queued', 'retry_wait'].includes(review.value?.state ?? ''))

const eventTitle = (event: ReviewEvent) => event.event_type === 'analysis_log' ? '分析执行' : event.event_type === 'finding_updated' ? '发现代码问题' : event.event_type === 'usage_updated' ? 'Token 用量更新' : event.event_type === 'progress_updated' ? '文件进度更新' : stageLabel(event.stage || event.event_type)
const eventTone = (event: ReviewEvent) => event.event_type === 'finding_updated' ? 'danger' : event.event_type === 'job_finished' ? 'success' : event.event_type === 'analysis_log' ? 'analysis' : 'primary'

const action = async (kind: 'retry' | 'cancel') => {
  if (!review.value) return
  const label = kind === 'retry' ? '重试' : '取消'
  try {
    const result = await ElMessageBox.prompt(`请输入${label}原因`, `${label}审查任务`, { inputPlaceholder: '说明本次操作的原因', inputValidator: (value: string) => value.trim() ? true : '原因不能为空' })
    actionLoading.value = true
    await getJSON(`/api/v1/admin/reviews/${review.value.id}/${kind}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ reason: result.value, expected_state: review.value.state }) })
    ElMessage.success(`${label}操作已提交`)
    await Promise.all([detail.refetch(), events.refetch(), revisions.refetch(), queryClient.invalidateQueries({ queryKey: ['admin-reviews'] }), queryClient.invalidateQueries({ queryKey: ['admin-dashboard'] })])
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') ElMessage.error(error instanceof Error ? error.message : String(error))
  } finally {
    actionLoading.value = false
  }
}

const changePriority = async () => {
  if (!review.value) return
  try {
    const result = await ElMessageBox.prompt('请输入 -1000 到 1000 之间的优先级', '调整任务优先级', { inputValue: String(review.value.priority), inputValidator: (value: string) => /^-?\d+$/.test(value.trim()) && Number(value) >= -1000 && Number(value) <= 1000 ? true : '请输入有效优先级' })
    actionLoading.value = true
    const priority = Number(result.value)
    await getJSON(`/api/v1/admin/reviews/${review.value.id}/priority`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ priority, reason: `管理员调整优先级为 ${priority}`, expected_state: review.value.state }) })
    ElMessage.success('优先级已更新')
    await Promise.all([detail.refetch(), events.refetch(), queryClient.invalidateQueries({ queryKey: ['admin-reviews'] })])
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') ElMessage.error(error instanceof Error ? error.message : String(error))
  } finally {
    actionLoading.value = false
  }
}
</script>

<template>
  <div v-if="review" class="detail-page">
    <button class="back-button" type="button" @click="router.push('/reviews')"><span>←</span>返回任务列表</button>

    <section class="review-hero">
      <div class="hero-main">
        <div class="hero-copy">
          <div class="title-row"><span class="mr-number">!{{ review.mr_iid }}</span>
            <h1>{{ review.title || '未命名审查' }}</h1><el-tag :type="reviewStateType(review.state)" effect="light" round>{{
              reviewStateLabel(review.state) }}</el-tag>
          </div>
          <p class="project-path">{{ review.project_path || `项目 ${review.project_id}` }}</p>
          <div class="branch-route"><code>{{ review.source_branch }}</code><span
              class="route-arrow">→</span><code>{{ review.target_branch }}</code><span class="sha-pill">{{
                review.head_sha.slice(0, 10) }}</span></div>
        </div>
        <div class="heading-actions">
          <el-button v-if="canPrioritize" :loading="actionLoading" @click="changePriority">调整优先级</el-button>
          <el-button v-if="canRetry" type="warning" :loading="actionLoading" @click="action('retry')">重试任务</el-button>
          <el-button v-if="canCancel" type="danger" plain :loading="actionLoading"
            @click="action('cancel')">取消任务</el-button>
          <el-button v-if="review.web_url" plain tag="a" :href="review.web_url" target="_blank">GitLab MR ↗</el-button>
          <el-button v-if="review.report_url" type="primary" tag="a" :href="review.report_url" target="_blank">OCR 报告
            ↗</el-button>
        </div>
      </div>
      <div class="hero-progress">
        <div class="progress-copy"><strong>{{ stageLabel(review.stage) }}</strong><span>{{ review.progress_completed }}
            / {{ review.progress_total || '?' }} 个文件</span></div>
        <el-progress :percentage="progressPercent(review)" :stroke-width="9" :show-text="false" />
        <time>{{ formatDate(review.finished_at || review.started_at || review.queued_at) }}</time>
      </div>
    </section>

    <section class="detail-content">
      <el-tabs v-model="activeTab" class="detail-tabs">
        <el-tab-pane label="概览" name="overview">
          <div class="metric-grid">
            <div class="metric metric-code"><span>Head SHA</span><code>{{ review.head_sha }}</code></div>
            <div class="metric metric-code"><span>Target SHA</span><code>{{ review.target_sha }}</code></div>
            <div class="metric metric-code"><span>Base SHA</span><code>{{ review.base_sha || '—' }}</code></div>
            <div class="metric"><span>执行次数</span><b>{{ review.attempt }}</b><small>优先级 {{ review.priority }}</small>
            </div>
            <div class="metric"><span>Token</span><b>{{ formatTokens(review.total_tokens) }}</b><small>输入 {{
              formatTokens(review.input_tokens) }} · 输出 {{ formatTokens(review.output_tokens) }}</small></div>
            <div class="metric"><span>问题</span><b :class="review.findings.blocking ? 'danger' : ''">{{
              review.findings.total }}</b><small>阻断 {{ review.findings.blocking }} · 新增 {{ review.findings.new
                }}</small></div>
          </div>
          <div class="overview-grid">
            <div class="info-panel">
              <div class="panel-title"><span>规则与模型</span><small>审查运行配置</small></div>
              <div class="key-value"><span>规则状态</span><el-tag
                  :type="reviewDetail?.rule.status === 'valid' ? 'success' : 'warning'" effect="light">{{
                    reviewDetail?.rule.status === 'valid' ? '有效' : '默认规则' }}</el-tag></div>
              <div class="key-value"><span>规则 SHA</span><code>{{ reviewDetail?.rule.sha256 || '—' }}</code></div>
              <div class="key-value"><span>LLM Provider</span><b>{{ reviewDetail?.llm.provider || '—' }}</b></div>
              <div class="key-value"><span>LLM Model</span><b>{{ reviewDetail?.llm.model || '—' }}</b></div>
            </div>
            <div class="info-panel">
              <div class="panel-title"><span>审查覆盖</span><small>{{ review.coverage.complete ? '覆盖完整' : '覆盖不完整' }}</small>
              </div>
              <div class="coverage-summary">
                <div><b>{{ review.coverage.selected }}</b><span>计划</span></div>
                <div><b>{{ review.coverage.completed }}</b><span>完成</span></div>
                <div><b>{{ review.coverage.reused }}</b><span>复用</span></div>
                <div><b>{{ review.coverage.failed }}</b><span>失败</span></div>
              </div><el-alert v-if="!review.coverage.complete" title="覆盖不完整，请检查失败文件" type="warning" :closable="false"
                show-icon />
            </div>
          </div>
          <el-alert v-if="review.failure_reason" :title="review.failure_reason" type="error" :closable="false"
            class="failure-alert" show-icon />
        </el-tab-pane>

        <el-tab-pane label="执行时间线" name="events">
          <div class="timeline-shell" v-loading="events.isLoading.value">
            <div v-for="event in (events.data.value ?? [])" :key="event.id || `${event.event_type}-${event.created_at}`"
              class="timeline-row">
              <div class="timeline-rail"><span class="timeline-dot" :class="eventTone(event)" /></div>
              <article class="timeline-card">
                <header>
                  <div><strong>{{ eventTitle(event) }}</strong><el-tag v-if="event.stage" size="small" effect="plain">{{
                      stageLabel(event.stage) }}</el-tag></div><time>{{ formatDate(event.created_at) }}</time>
                </header>
                <p>{{ event.safe_message || '状态已更新' }}</p>
                <footer v-if="event.total || event.total_tokens"><span v-if="event.total">文件 {{ event.completed }} / {{
                    event.total }}</span><span v-if="event.total_tokens">Token {{ formatTokens(event.total_tokens)
                    }}</span></footer>
              </article>
            </div>
            <el-empty v-if="!events.isLoading.value && !events.data.value?.length" description="暂无执行事件" />
          </div>
        </el-tab-pane>

        <el-tab-pane label="执行日志" name="logs">
          <div class="log-panel">
            <div class="log-toolbar"><span class="terminal-lights"><i /><i /><i /></span><strong>OCR Analysis
                Stream</strong><span class="live-indicator"><i />SSE 实时</span></div>
            <div class="log-console">
              <div v-for="event in analysisLogs" :key="event.id || `${event.event_type}-${event.created_at}`"
                class="log-line"><time>{{ new Date(event.created_at).toLocaleTimeString('zh-CN', { hour12: false })
                  }}</time><span class="log-stage">[{{ stageLabel(event.stage || event.event_type) }}]</span><span>{{
                    event.safe_message || eventTitle(event) }}</span><em v-if="event.total_tokens">{{
                    formatTokens(event.total_tokens) }} tokens</em></div>
              <div v-if="!analysisLogs.length" class="log-empty">等待分析日志通过 SSE 推送…</div>
            </div>
          </div>
        </el-tab-pane>

        <el-tab-pane label="缺陷" name="findings"><el-table :data="reviewDetail?.findings ?? []" stripe><el-table-column
              label="位置" min-width="190"><template
                #default="scope"><code>{{ scope.row.path }} L{{ scope.row.start_line }}{{ scope.row.end_line && scope.row.end_line !== scope.row.start_line ? `-L${scope.row.end_line}` : '' }}</code></template></el-table-column><el-table-column
              label="严重度" width="95"><template #default="scope"><el-tag
                  :type="scope.row.severity === 'critical' || scope.row.severity === 'high' ? 'danger' : 'warning'"
                  effect="light">{{ scope.row.severity || '未标注' }}</el-tag></template></el-table-column><el-table-column
              label="状态" width="95"><template #default="scope"><el-tag
                  :type="scope.row.status === 'fixed' ? 'success' : scope.row.status === 'new' ? 'danger' : 'warning'"
                  effect="light">{{ scope.row.status === 'fixed' ? '已修复' : scope.row.status === 'new' ? '新增' :
                    scope.row.status === 'unfixed' ? '未修复' : '当前' }}</el-tag></template></el-table-column><el-table-column
              label="类别" width="125"><template #default="scope">{{ scope.row.category || '其他'
                }}</template></el-table-column><el-table-column label="问题" min-width="360"><template #default="scope">
                <div class="finding-content">{{ scope.row.content }}</div>
                <details v-if="scope.row.suggestion_code">
                  <summary>查看建议代码</summary>
                  <pre>{{ scope.row.suggestion_code }}</pre>
                </details>
              </template></el-table-column></el-table><el-empty v-if="!reviewDetail?.findings.length"
            description="当前没有缺陷" /></el-tab-pane>
        <el-tab-pane label="覆盖文件" name="coverage">
          <div class="file-columns">
            <div class="file-panel">
              <h3>已完成 <span>{{ reviewDetail?.coverage.completed_files?.length ?? 0 }}</span></h3><code
                v-for="file in reviewDetail?.coverage.completed_files ?? []" :key="file">{{ file }}</code>
            </div>
            <div class="file-panel">
              <h3>复用 <span>{{ reviewDetail?.coverage.reused_files?.length ?? 0 }}</span></h3><code
                v-for="file in reviewDetail?.coverage.reused_files ?? []" :key="file">{{ file }}</code>
            </div>
            <div class="file-panel failed">
              <h3>失败 <span>{{ reviewDetail?.coverage.failed_files?.length ?? 0 }}</span></h3><code
                v-for="file in reviewDetail?.coverage.failed_files ?? []" :key="file">{{ file }}</code>
            </div>
            <div class="file-panel">
              <h3>Code Graph 影响文件 <span>{{ reviewDetail?.coverage.affected_files?.length ?? 0 }}</span></h3><code
                v-for="file in reviewDetail?.coverage.affected_files ?? []" :key="file">{{ file }}</code>
            </div>
          </div>
        </el-tab-pane>
        <el-tab-pane label="版本链" name="revisions"><el-table :data="revisions.data.value ?? []" stripe><el-table-column
              label="Head SHA" min-width="170"><template
                #default="scope"><code>{{ scope.row.head_sha.slice(0, 12) }}</code></template></el-table-column><el-table-column
              label="Target SHA" min-width="170"><template
                #default="scope"><code>{{ scope.row.target_sha.slice(0, 12) }}</code></template></el-table-column><el-table-column
              label="状态" width="120"><template #default="scope"><el-tag :type="reviewStateType(scope.row.state)"
                  effect="light">{{ reviewStateLabel(scope.row.state)
                  }}</el-tag></template></el-table-column><el-table-column label="进度" width="120"><template
                #default="scope">{{ scope.row.progress_completed }} / {{ scope.row.progress_total || '?'
                }}</template></el-table-column><el-table-column label="Token" width="100"><template #default="scope">{{
                  formatTokens(scope.row.total_tokens) }}</template></el-table-column><el-table-column label="时间"
              width="150"><template #default="scope">{{ formatDate(scope.row.finished_at || scope.row.queued_at)
                }}</template></el-table-column></el-table></el-tab-pane>
      </el-tabs>
    </section>
  </div>
  <el-skeleton v-else-if="detail.isLoading.value" :rows="10" animated />
  <el-result v-else icon="error" title="无法读取审查任务" sub-title="任务不存在或后台接口不可用"><template #extra><el-button type="primary"
        @click="router.push('/reviews')">返回任务列表</el-button></template></el-result>
</template>

<style scoped>
.detail-page {
  max-width: 1480px;
  margin: 0 auto;
}

.back-button {
  display: inline-flex;
  align-items: center;
  gap: 9px;
  margin: 0 0 14px;
  padding: 8px 13px 8px 9px;
  border: 1px solid #e5e8f1;
  border-radius: 9px;
  color: #5f667c;
  background: #fff;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: .18s ease;
}

.back-button span {
  display: grid;
  width: 23px;
  height: 23px;
  place-items: center;
  border-radius: 7px;
  color: #6258d8;
  background: #efedff;
  font-size: 15px;
}

.back-button:hover {
  color: #5147c6;
  border-color: #cbc6f7;
  box-shadow: 0 5px 14px #3f457312;
  transform: translateY(-1px);
}

.review-hero {
  margin-bottom: 18px;
  overflow: hidden;
  border: 1px solid #e7e9f2;
  border-radius: 15px;
  background: linear-gradient(135deg, #fff 0%, #fbfaff 64%, #f2f5ff 100%);
  box-shadow: 0 8px 28px #31375d0b;
}

.hero-main {
  display: flex;
  justify-content: space-between;
  gap: 24px;
  padding: 24px 26px 21px;
}

.hero-copy {
  min-width: 0;
}

.title-row {
  display: flex;
  align-items: center;
  gap: 11px;
}

.title-row h1 {
  min-width: 0;
  margin: 0;
  overflow: hidden;
  color: #292f47;
  font-size: 23px;
  letter-spacing: -.3px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mr-number {
  flex: 0 0 auto;
  color: #6258d8;
  font-size: 20px;
  font-weight: 800;
}

.project-path {
  margin: 8px 0 13px;
  color: #8f95a8;
  font-size: 12px;
}

.branch-route {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.branch-route code {
  padding: 5px 9px;
  border: 1px solid #e8e6f7;
  border-radius: 7px;
  color: #5b53bf;
  background: #f6f5ff;
  font-size: 11px;
}

.route-arrow {
  color: #a2a7b7;
}

.sha-pill {
  padding: 5px 9px;
  border-radius: 7px;
  color: #6e758a;
  background: #eef1f7;
  font: 10px ui-monospace, SFMono-Regular, Consolas, monospace;
}

.heading-actions {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-content: flex-start;
  justify-content: flex-end;
  gap: 7px;
}

.hero-progress {
  display: grid;
  grid-template-columns: 180px minmax(160px, 1fr) 170px;
  align-items: center;
  gap: 18px;
  padding: 13px 26px;
  border-top: 1px solid #eeeff5;
  background: #fff9;
}

.progress-copy {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.progress-copy strong {
  color: #474e67;
  font-size: 12px;
}

.progress-copy span,
.hero-progress time {
  color: #989eb0;
  font-size: 10px;
}

.hero-progress time {
  text-align: right;
}

.hero-progress :deep(.el-progress-bar__outer) {
  background: #e9ebf3;
}

.detail-content {
  padding: 3px 22px 22px;
  border: 1px solid #e8eaf2;
  border-radius: 14px;
  background: #fff;
  box-shadow: 0 5px 20px #31375d08;
}

.detail-tabs :deep(.el-tabs__header) {
  margin-bottom: 22px;
}

.detail-tabs :deep(.el-tabs__item) {
  height: 50px;
  padding: 0 19px;
  color: #777e93;
  font-size: 13px;
}

.detail-tabs :deep(.el-tabs__item.is-active) {
  color: #5c52ce;
  font-weight: 700;
}

.detail-tabs :deep(.el-tabs__active-bar) {
  height: 3px;
  border-radius: 4px;
  background: #665bd9;
}

.metric-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
  margin-bottom: 17px;
}

.metric {
  min-height: 96px;
  padding: 16px 18px;
  border: 1px solid #eceef4;
  border-radius: 11px;
  background: #fbfcfe;
}

.metric span,
.metric small {
  display: block;
  color: #959bad;
  font-size: 11px;
}

.metric b {
  display: block;
  margin: 9px 0 4px;
  color: #30364e;
  font-size: 22px;
}

.metric code {
  display: block;
  margin-top: 13px;
  overflow: hidden;
  color: #5f57c2;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.metric-code {
  background: linear-gradient(145deg, #fbfaff, #f8f9fc);
}

.danger {
  color: #df5968 !important;
}

.overview-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}

.info-panel {
  padding: 18px 20px;
  border: 1px solid #eaecf3;
  border-radius: 12px;
}

.panel-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding-bottom: 13px;
  border-bottom: 1px solid #f0f1f5;
}

.panel-title span {
  color: #3f465f;
  font-size: 14px;
  font-weight: 700;
}

.panel-title small {
  color: #a1a6b6;
  font-size: 10px;
}

.key-value {
  display: flex;
  justify-content: space-between;
  gap: 18px;
  padding: 11px 0;
  border-bottom: 1px solid #f3f4f7;
  color: #8b91a4;
  font-size: 12px;
}

.key-value:last-child {
  border-bottom: 0;
}

.key-value code,
.key-value b {
  max-width: 72%;
  overflow: hidden;
  color: #454c65;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.coverage-summary {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
  margin: 15px 0;
}

.coverage-summary div {
  padding: 11px 5px;
  border-radius: 8px;
  background: #f6f7fb;
  text-align: center;
}

.coverage-summary b,
.coverage-summary span {
  display: block;
}

.coverage-summary b {
  color: #565ead;
  font-size: 19px;
}

.coverage-summary span {
  margin-top: 3px;
  color: #969caf;
  font-size: 10px;
}

.failure-alert {
  margin-top: 16px;
}

.timeline-shell {
  position: relative;
  max-width: 980px;
  margin: 3px auto 0;
  padding: 4px 0 10px;
}

.timeline-row {
  display: grid;
  grid-template-columns: 30px minmax(0, 1fr);
  gap: 12px;
}

.timeline-rail {
  position: relative;
  display: flex;
  justify-content: center;
}

.timeline-rail::after {
  position: absolute;
  top: 24px;
  bottom: -7px;
  width: 2px;
  background: #e8eaf1;
  content: '';
}

.timeline-row:last-child .timeline-rail::after {
  display: none;
}

.timeline-dot {
  position: relative;
  z-index: 1;
  width: 13px;
  height: 13px;
  margin-top: 18px;
  border: 3px solid #fff;
  border-radius: 50%;
  background: #6c64db;
  box-shadow: 0 0 0 3px #e8e6ff;
}

.timeline-dot.analysis {
  background: #388edf;
  box-shadow: 0 0 0 3px #e3f1ff;
}

.timeline-dot.danger {
  background: #e26773;
  box-shadow: 0 0 0 3px #ffe6e9;
}

.timeline-dot.success {
  background: #31ad7b;
  box-shadow: 0 0 0 3px #def5eb;
}

.timeline-card {
  margin-bottom: 12px;
  padding: 14px 17px;
  border: 1px solid #eaecf3;
  border-radius: 10px;
  background: #fcfcfe;
  transition: .15s ease;
}

.timeline-card:hover {
  border-color: #d7d4f4;
  background: #fff;
  box-shadow: 0 5px 17px #373d6810;
}

.timeline-card header {
  display: flex;
  justify-content: space-between;
  gap: 15px;
}

.timeline-card header>div {
  display: flex;
  align-items: center;
  gap: 9px;
}

.timeline-card strong {
  color: #424961;
  font-size: 13px;
}

.timeline-card time {
  color: #9ca2b2;
  font-size: 10px;
}

.timeline-card p {
  margin: 7px 0 0;
  color: #697087;
  font-size: 12px;
  line-height: 1.55;
}

.timeline-card footer {
  display: flex;
  gap: 14px;
  margin-top: 9px;
  color: #8990a4;
  font-size: 10px;
}

.log-panel {
  overflow: hidden;
  border: 1px solid #252b3a;
  border-radius: 12px;
  background: #111722;
  box-shadow: 0 10px 28px #10152222;
}

.log-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 43px;
  padding: 0 15px;
  color: #c7cddd;
  background: #1c2330;
  font-size: 11px;
}

.terminal-lights {
  display: flex;
  gap: 6px;
}

.terminal-lights i {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #ef6b73;
}

.terminal-lights i:nth-child(2) {
  background: #e6b04c;
}

.terminal-lights i:nth-child(3) {
  background: #42bd7f;
}

.live-indicator {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
  color: #8bdcb7;
}

.live-indicator i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #42c389;
  box-shadow: 0 0 0 4px #42c38920;
}

.log-console {
  min-height: 330px;
  max-height: 56vh;
  padding: 14px 17px;
  overflow: auto;
  color: #c0c8d8;
  font: 11px/1.7 ui-monospace, SFMono-Regular, Consolas, monospace;
}

.log-line {
  display: grid;
  grid-template-columns: 80px 125px minmax(0, 1fr) auto;
  gap: 9px;
  padding: 4px 0;
  border-bottom: 1px solid #ffffff0a;
}

.log-line time {
  color: #667188;
}

.log-stage {
  color: #7ea8e8;
}

.log-line em {
  color: #8c96aa;
  font-style: normal;
}

.log-empty {
  padding: 100px 0;
  color: #667188;
  text-align: center;
}

.finding-content {
  line-height: 1.65;
  white-space: pre-wrap;
}

.finding-content+details {
  margin-top: 8px;
}

.finding-content~details summary {
  color: #6258d8;
  cursor: pointer;
  font-size: 11px;
}

.finding-content~details pre {
  padding: 10px;
  overflow: auto;
  border-radius: 7px;
  background: #f7f8fb;
  font-size: 11px;
}

.file-columns {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 14px;
}

.file-panel {
  min-height: 150px;
  max-height: 390px;
  padding: 15px 17px;
  overflow: auto;
  border: 1px solid #eaecf3;
  border-radius: 11px;
  background: #fcfcfe;
}

.file-panel h3 {
  margin: 0 0 12px;
  color: #4b526b;
  font-size: 13px;
}

.file-panel h3 span {
  margin-left: 6px;
  color: #7971d5;
}

.file-panel code {
  display: block;
  padding: 5px 0;
  color: #626a82;
  font-size: 11px;
  overflow-wrap: anywhere;
}

.file-panel.failed {
  border-color: #f2dadd;
  background: #fffafa;
}

@media (max-width: 900px) {
  .hero-main {
    flex-direction: column;
  }

  .heading-actions {
    justify-content: flex-start;
  }

  .hero-progress {
    grid-template-columns: 150px 1fr;
  }

  .hero-progress time {
    display: none;
  }

  .metric-grid,
  .overview-grid,
  .file-columns {
    grid-template-columns: 1fr 1fr;
  }

  .log-line {
    grid-template-columns: 75px 110px minmax(0, 1fr);
  }

  .log-line em {
    display: none;
  }
}

@media (max-width: 600px) {
  .title-row {
    align-items: flex-start;
    flex-wrap: wrap;
  }

  .title-row h1 {
    width: 100%;
    white-space: normal;
  }

  .metric-grid,
  .overview-grid,
  .file-columns {
    grid-template-columns: 1fr;
  }

  .coverage-summary {
    grid-template-columns: repeat(2, 1fr);
  }

  .hero-progress {
    grid-template-columns: 1fr;
  }

  .log-line {
    grid-template-columns: 70px minmax(0, 1fr);
  }

  .log-stage {
    display: none;
  }
}
</style>
