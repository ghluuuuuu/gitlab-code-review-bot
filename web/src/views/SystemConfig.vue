<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { ElMessage } from 'element-plus'
import { getJSON } from '../adminApi'

type ConfigEnvelope = { config: any; path: string; can_save: boolean; restart_required: boolean; secrets: Record<string, boolean> }
const configQuery = useQuery({ queryKey: ['admin-config'], queryFn: () => getJSON<ConfigEnvelope>('/api/v1/admin/config') })
const form = reactive<any>({})
const initialized = ref(false)
const saving = ref(false)
watch(() => configQuery.data.value, value => {
  if (value?.config && !initialized.value) {
    Object.assign(form, JSON.parse(JSON.stringify(value.config)))
    initialized.value = true
  }
}, { immediate: true })
const save = async () => {
  saving.value = true
  try {
    const response = await getJSON<{ restart_required: boolean }>('/api/v1/admin/config', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(form) })
    ElMessage.success(response.restart_required ? '配置已保存，重启服务后生效' : '配置已保存')
  } catch (error) { ElMessage.error(error instanceof Error ? error.message : String(error)) } finally { saving.value = false }
}
</script>

<template>
  <div class="page-heading"><div><h1>系统配置</h1><p>管理 config.json 的全部运行配置。敏感字段留空时保留当前值。</p></div><el-button type="primary" :loading="saving" :disabled="!configQuery.data.value?.can_save" @click="save">保存配置</el-button></div>
  <el-alert v-if="configQuery.data.value && !configQuery.data.value.can_save" type="warning" show-icon :closable="false" title="当前配置不是从文件加载，无法持久化。请通过 --config 或 OCR_BOT_CONFIG 指定 config.json。" class="notice" />
  <div v-if="initialized" class="config-grid">
    <el-card shadow="never"><template #header><strong>基础存储</strong></template><el-form label-position="top"><el-form-item label="数据库路径"><el-input v-model="form.database_path" /></el-form-item><el-form-item label="数据目录"><el-input v-model="form.data_dir" /></el-form-item></el-form></el-card>
    <el-card shadow="never"><template #header><strong>GitLab</strong></template><el-form label-position="top"><el-form-item label="Base URL"><el-input v-model="form.gitlab.base_url" /></el-form-item><el-form-item label="Token"><el-input v-model="form.gitlab.token" type="password" show-password :placeholder="configQuery.data.value?.secrets.gitlab_token ? '已配置，留空不修改' : '请输入 Token'" /></el-form-item><el-form-item label="轮询间隔（秒）"><el-input-number v-model="form.gitlab.poll_seconds" :min="5" /></el-form-item></el-form></el-card>
    <el-card shadow="never"><template #header><strong>审查</strong></template><el-form label-position="top"><el-form-item label="规则路径"><el-input v-model="form.review.rule_path" /></el-form-item><div class="row"><el-form-item label="任务并发"><el-input-number v-model="form.review.concurrency" :min="1" /></el-form-item><el-form-item label="文件并发"><el-input-number v-model="form.review.file_concurrency" :min="1" /></el-form-item></div><el-form-item label="超时（分钟）"><el-input-number v-model="form.review.timeout_minutes" :min="1" /></el-form-item><el-form-item label="阻断严重度（逗号分隔）"><el-input :model-value="form.review.blocking_severities.join(', ')" @update:model-value="form.review.blocking_severities = String($event).split(',').map(v=>v.trim()).filter(Boolean)" /></el-form-item><el-form-item label="Viewer URL"><el-input v-model="form.review.viewer_url" /></el-form-item><div class="row"><el-form-item label="每日 Token 预算"><el-input-number v-model="form.review.daily_token_budget" :min="0" /></el-form-item><el-form-item label="每月 Token 预算"><el-input-number v-model="form.review.monthly_token_budget" :min="0" /></el-form-item></div></el-form></el-card>
    <el-card shadow="never"><template #header><strong>LLM</strong></template><el-form label-position="top"><el-form-item label="API URL"><el-input v-model="form.llm.url" /></el-form-item><el-form-item label="Token"><el-input v-model="form.llm.token" type="password" show-password :placeholder="configQuery.data.value?.secrets.llm_token ? '已配置，留空不修改' : '请输入 Token'" /></el-form-item><el-form-item label="模型"><el-input v-model="form.llm.model" /></el-form-item><el-form-item label="输出语言"><el-input v-model="form.llm.language" /></el-form-item><el-form-item label="协议"><el-switch v-model="form.llm.use_anthropic" active-text="Anthropic" inactive-text="OpenAI" /></el-form-item><el-form-item label="鉴权 Header"><el-input v-model="form.llm.auth_header" /></el-form-item><el-form-item label="额外 Headers"><el-input v-model="form.llm.extra_headers" type="textarea" /></el-form-item><el-form-item label="额外 Body"><el-input v-model="form.llm.extra_body" type="textarea" /></el-form-item><el-form-item label="超时（秒）"><el-input-number v-model="form.llm.timeout_seconds" :min="0" /></el-form-item></el-form></el-card>
    <el-card shadow="never"><template #header><strong>Code Graph</strong></template><el-form label-position="top"><el-form-item label="启用"><el-switch v-model="form.code_graph.enabled" /></el-form-item><el-form-item label="命令"><el-input v-model="form.code_graph.command" /></el-form-item><el-form-item label="数据目录"><el-input v-model="form.code_graph.data_dir" /></el-form-item><el-form-item label="超时（分钟）"><el-input-number v-model="form.code_graph.timeout_minutes" :min="1" /></el-form-item></el-form></el-card>
    <el-card shadow="never"><template #header><strong>账户与会话</strong></template><el-form label-position="top"><el-form-item label="启用账户体系"><el-switch v-model="form.auth.enabled" /></el-form-item><el-form-item label="会话时长（小时）"><el-input-number v-model="form.auth.session_hours" :min="1" /></el-form-item><el-form-item label="初始超管账户名"><el-input v-model="form.auth.bootstrap_admin.username" /></el-form-item><el-form-item label="初始超管邮箱"><el-input v-model="form.auth.bootstrap_admin.email" /></el-form-item><el-form-item label="初始超管密码"><el-input v-model="form.auth.bootstrap_admin.password" type="password" show-password :placeholder="configQuery.data.value?.secrets.bootstrap_admin_password ? '已配置，留空不修改' : '至少 10 位'" /></el-form-item></el-form></el-card>
    <el-card shadow="never"><template #header><strong>OIDC</strong></template><el-form label-position="top"><el-form-item label="启用"><el-switch v-model="form.auth.oidc.enabled" /></el-form-item><el-form-item label="Issuer URL"><el-input v-model="form.auth.oidc.issuer_url" /></el-form-item><el-form-item label="Client ID"><el-input v-model="form.auth.oidc.client_id" /></el-form-item><el-form-item label="Client Secret"><el-input v-model="form.auth.oidc.client_secret" type="password" show-password :placeholder="configQuery.data.value?.secrets.oidc_client_secret ? '已配置，留空不修改' : ''" /></el-form-item><el-form-item label="Redirect URL"><el-input v-model="form.auth.oidc.redirect_url" /></el-form-item><el-form-item label="Scopes（逗号分隔）"><el-input :model-value="form.auth.oidc.scopes.join(', ')" @update:model-value="form.auth.oidc.scopes = String($event).split(',').map(v=>v.trim()).filter(Boolean)" /></el-form-item><el-form-item label="首次登录自动注册"><el-switch v-model="form.auth.oidc.auto_register" /></el-form-item></el-form></el-card>
    <el-card shadow="never"><template #header><strong>服务</strong></template><el-form label-position="top"><el-form-item label="监听地址"><el-input v-model="form.server.addr" /></el-form-item></el-form></el-card>
  </div>
</template>

<style scoped>
.page-heading{display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:22px}.page-heading h1{margin:0 0 7px;font-size:25px}.page-heading p{margin:0;color:#9499ad;font-size:13px}.notice{margin-bottom:18px}.config-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:18px;align-items:start}.config-grid .el-card{border:1px solid #ebedf4;border-radius:12px}.row{display:grid;grid-template-columns:1fr 1fr;gap:14px}.el-input-number{width:100%}@media(max-width:900px){.config-grid{grid-template-columns:1fr}}
</style>
