<script setup lang="ts">
import { ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getJSON, formatDate, formatTokens, type Dashboard } from '../adminApi'

type SystemStatus = { database: { status: string }; gitlab: { base_url: string; status: string }; llm: { model: string; status: string }; code_graph: { enabled: boolean; command: string; status: string }; viewer: { url: string; status: string }; dashboard: Dashboard; budgets: { daily: number; monthly: number; daily_used: number; monthly_used: number; daily_exceeded: boolean; monthly_exceeded: boolean } }
type AuditEvent = { id: number; actor: string; action: string; review_job_id?: number; detail?: string; created_at: string }
type CurrentAdmin = { name: string; roles: string[]; permissions: string[] }
const system = useQuery({ queryKey: ['system-status'], queryFn: () => getJSON<SystemStatus>('/api/v1/admin/system') })
const audit = useQuery({ queryKey: ['audit-events'], queryFn: () => getJSON<AuditEvent[]>('/api/v1/admin/audit-events?limit=50') })
const me = useQuery({ queryKey: ['admin-me'], queryFn: () => getJSON<CurrentAdmin>('/api/v1/admin/me') })
const reconcileLoading = ref(false)
const reconcile = async () => {
  try {
    await ElMessageBox.confirm('立即向 GitLab 重新发现分配给 Bot 的 Merge Request？', '重新发现审查任务', { type: 'warning' })
    reconcileLoading.value = true
    await getJSON('/api/v1/admin/reconcile', { method: 'POST' })
    ElMessage.success('重新发现已完成')
    await Promise.all([system.refetch(), audit.refetch()])
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') ElMessage.error(error instanceof Error ? error.message : String(error))
  } finally {
    reconcileLoading.value = false
  }
}
const statusLabel = (value: string) => value === 'ready' || value === 'configured' ? '正常' : value === 'not_configured' ? '未配置' : value
const statusType = (value: string) => value === 'ready' || value === 'configured' ? 'success' : value === 'not_configured' ? 'warning' : 'danger'
</script>

<template>
  <div class="page-heading">
    <div>
      <h1>系统状态</h1>
      <p>查看 GitLab、LLM、Code Graph、Viewer 和审查运行态</p>
    </div>
    <div><el-button v-if="me.data.value?.permissions.includes('system.reconcile')" type="primary"
        :loading="reconcileLoading" @click="reconcile">重新发现任务</el-button></div>
  </div>
  <div class="status-grid" v-loading="system.isLoading.value">
    <el-card shadow="never"><template #header><strong>数据库</strong></template>
      <div class="status-row"><span>状态</span><el-tag :type="statusType(system.data.value?.database.status ?? 'unknown')"
          effect="light">{{ statusLabel(system.data.value?.database.status ?? 'unknown') }}</el-tag></div>
    </el-card>
    <el-card shadow="never"><template #header><strong>GitLab</strong></template>
      <div class="status-row"><span>状态</span><el-tag :type="statusType(system.data.value?.gitlab.status ?? 'unknown')"
          effect="light">{{ statusLabel(system.data.value?.gitlab.status ?? 'unknown') }}</el-tag></div><code>{{
            system.data.value?.gitlab.base_url || '—' }}</code>
    </el-card>
    <el-card shadow="never"><template #header><strong>LLM</strong></template>
      <div class="status-row"><span>状态</span><el-tag :type="statusType(system.data.value?.llm.status ?? 'unknown')"
          effect="light">{{ statusLabel(system.data.value?.llm.status ?? 'unknown') }}</el-tag></div><code>{{
            system.data.value?.llm.model || '—' }}</code>
    </el-card>
    <el-card shadow="never"><template #header><strong>Code Graph</strong></template>
      <div class="status-row"><span>启用</span><el-tag :type="system.data.value?.code_graph.enabled ? 'success' : 'info'"
          effect="light">{{ system.data.value?.code_graph.enabled ? '是' : '否' }}</el-tag></div><code>{{
            system.data.value?.code_graph.command || '—' }}</code>
    </el-card>
    <el-card shadow="never"><template #header><strong>OCR Viewer</strong></template>
      <div class="status-row"><span>状态</span><el-tag :type="statusType(system.data.value?.viewer.status ?? 'unknown')"
          effect="light">{{ statusLabel(system.data.value?.viewer.status ?? 'unknown') }}</el-tag></div><code>{{
            system.data.value?.viewer.url || '—' }}</code>
    </el-card>
    <el-card shadow="never"><template #header><strong>Token 预算</strong></template>
      <div class="status-row"><span>今日</span><el-tag
          :type="system.data.value?.budgets.daily_exceeded ? 'danger' : 'success'" effect="light">{{
            formatTokens(system.data.value?.budgets.daily_used ?? 0) }} / {{ system.data.value?.budgets.daily ?
            formatTokens(system.data.value.budgets.daily) : '不限' }}</el-tag></div>
      <div class="status-row"><span>本月</span><el-tag
          :type="system.data.value?.budgets.monthly_exceeded ? 'danger' : 'success'" effect="light">{{
            formatTokens(system.data.value?.budgets.monthly_used ?? 0) }} / {{ system.data.value?.budgets.monthly ?
            formatTokens(system.data.value.budgets.monthly) : '不限' }}</el-tag></div>
    </el-card>
  </div>
  <div class="overview-grid"><el-card shadow="never"><template #header><strong>当前任务概览</strong></template>
      <div class="metric-list">
        <div><span>排队中</span><b>{{ system.data.value?.dashboard.queued ?? 0 }}</b></div>
        <div><span>审查中</span><b>{{ system.data.value?.dashboard.running ?? 0 }}</b></div>
        <div><span>等待重试</span><b>{{ system.data.value?.dashboard.retry_wait ?? 0 }}</b></div>
        <div><span>今日 Token</span><b>{{ formatTokens(system.data.value?.dashboard.today_tokens ?? 0) }}</b></div>
      </div>
    </el-card><el-card shadow="never"><template #header><strong>最近审计操作</strong></template><el-table
        :data="audit.data.value ?? []" size="small"><el-table-column label="时间" width="155"><template
            #default="scope">{{ formatDate(scope.row.created_at) }}</template></el-table-column><el-table-column
          label="操作者" width="110" prop="actor" /><el-table-column label="操作" width="155"
          prop="action" /><el-table-column label="任务" width="70" prop="review_job_id" /><el-table-column label="原因"
          min-width="220" prop="detail" /></el-table><el-empty v-if="!audit.data.value?.length"
        description="暂无审计记录" /></el-card></div>
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

.status-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
  margin-bottom: 18px;
}

.status-grid .el-card,
.overview-grid .el-card {
  border: 1px solid #ebedf4;
  border-radius: 12px;
}

.status-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 13px;
  color: #858b9f;
  font-size: 12px;
}

.status-grid code {
  display: block;
  overflow: hidden;
  color: #606981;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.overview-grid {
  display: grid;
  grid-template-columns: 360px minmax(0, 1fr);
  gap: 18px;
}

.metric-list {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.metric-list div {
  padding: 13px;
  border-radius: 8px;
  background: #f7f8fc;
}

.metric-list span,
.metric-list b {
  display: block;
}

.metric-list span {
  color: #9298aa;
  font-size: 11px;
}

.metric-list b {
  margin-top: 5px;
  color: #363d57;
  font-size: 20px;
}

@media (max-width:900px) {
  .status-grid {
    grid-template-columns: 1fr 1fr;
  }

  .overview-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width:600px) {
  .status-grid {
    grid-template-columns: 1fr;
  }

  .page-heading {
    gap: 10px;
  }

  .page-heading h1 {
    font-size: 21px;
  }
}
</style>
