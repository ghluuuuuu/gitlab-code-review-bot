<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getJSON } from '../adminApi'

type MCPConfig = { server_name: string; url: string; token: string; authorization: string; config: { mcpServers: Record<string, { url: string; headers: Record<string, string> }> } }
const generated = ref<MCPConfig | null>(null)
const loading = ref(false)
const revealToken = ref(false)
const configJSON = computed(() => generated.value ? JSON.stringify(generated.value.config, null, 2) : '')
const maskedToken = computed(() => {
  const token = generated.value?.token ?? ''
  if (revealToken.value || token.length < 16) return token
  return `${token.slice(0, 8)}${'•'.repeat(20)}${token.slice(-6)}`
})
const generate = async () => {
  if (generated.value) {
    try { await ElMessageBox.confirm('重新生成后，旧 MCP Token 会立即失效。是否继续？', '更新连接凭据', { type: 'warning', confirmButtonText: '重新生成', cancelButtonText: '取消' }) }
    catch { return }
  }
  loading.value = true
  try {
    generated.value = await getJSON<MCPConfig>('/api/v1/auth/mcp-config', { method: 'POST', headers: { 'X-Requested-With': 'XMLHttpRequest' } })
    revealToken.value = false
    ElMessage.success('MCP 配置已生成；请立即保存到本地 coding agent')
  } catch (error) { ElMessage.error(error instanceof Error ? error.message : String(error)) } finally { loading.value = false }
}
const copy = async (value: string, label: string) => {
  try { await navigator.clipboard.writeText(value); ElMessage.success(`${label}已复制`) }
  catch { ElMessage.error('复制失败，请手动选择文本') }
}
</script>

<template>
  <div class="mcp-page">
    <section class="hero">
      <div class="hero-content">
        <div class="eyebrow"><span class="pulse" />Coding Agent Integration</div>
        <h1>MCP 接入</h1>
        <p>把代码审查上下文直接带进本地 coding agent。Agent 可按当前 Git 仓库、分支和提交哈希定位缺陷，并读取对应文件的行号与修复意见。</p>
        <div class="hero-tags"><span>Streamable HTTP</span><span>Bearer Token</span><span>GitLab 权限隔离</span></div>
      </div>
      <div class="hero-action"><div class="connection-orb"><i>⌘</i><span>{{ generated ? '连接凭据已就绪' : '等待生成连接凭据' }}</span></div><el-button type="primary" size="large" :loading="loading" @click="generate">{{ generated ? '重新生成配置' : '生成连接配置' }}</el-button></div>
    </section>

    <div class="workflow">
      <div><b>1</b><span><strong>生成凭据</strong><small>创建当前账户专属 Token</small></span></div><i>→</i><div><b>2</b><span><strong>配置 Agent</strong><small>复制 JSON 到 MCP 配置</small></span></div><i>→</i><div><b>3</b><span><strong>开始修复</strong><small>Agent 自动读取 Git 信息和缺陷</small></span></div>
    </div>

    <el-alert type="warning" show-icon :closable="false" title="安全提示：Token 只在本次生成后展示；重新生成会立即撤销旧 Token。" class="notice" />

    <section v-if="generated" class="credential-panel">
      <div class="panel-heading"><div><span class="section-kicker">CONNECTION</span><h2>连接信息</h2><p>服务名称 <code>{{ generated.server_name }}</code></p></div><el-tag type="success" effect="light" round>已就绪</el-tag></div>
      <div class="field-grid">
        <div class="field-card"><div class="field-title"><span>服务地址</span><small>Streamable HTTP Endpoint</small></div><div class="value-row"><code>{{ generated.url }}</code><button type="button" @click="copy(generated.url, '服务地址')">复制</button></div></div>
        <div class="field-card token-card"><div class="field-title"><span>访问令牌</span><small>Authorization: Bearer</small></div><div class="value-row"><code>{{ maskedToken }}</code><button type="button" @click="revealToken = !revealToken">{{ revealToken ? '隐藏' : '显示' }}</button><button type="button" @click="copy(generated.token, 'Token')">复制</button></div></div>
      </div>
      <div class="json-card"><div class="json-header"><div><span class="window-dot red"/><span class="window-dot yellow"/><span class="window-dot green"/><strong>mcp.json</strong></div><button type="button" @click="copy(configJSON, '配置 JSON')">复制完整配置</button></div><pre><code>{{ configJSON }}</code></pre></div>
    </section>

    <section v-else class="empty-credential"><div class="empty-icon">⌘</div><div><h2>还没有连接配置</h2><p>点击“生成连接配置”，获得当前账户专属的 MCP 地址和 Token。</p></div><el-button type="primary" plain @click="generate">立即生成</el-button></section>

    <section class="tool-section"><div class="section-heading"><span class="section-kicker">TOOLS</span><h2>提供给 Agent 的工具</h2><p>工具要求 Agent 先在本地工作区执行 Git 命令，再使用真实仓库信息调用。</p></div><div class="tool-grid"><article><div class="tool-icon branch">⑂</div><div><code>get_current_branch_issues</code><p>获取当前仓库、分支和提交对应的最新质量问题，包括严重度、代码位置和建议修复。</p><div class="commands"><span>git remote get-url origin</span><span>git branch --show-current</span><span>git rev-parse HEAD</span></div></div></article><article><div class="tool-icon file">▤</div><div><code>get_file_issues</code><p>针对仓库内单个文件读取缺陷、行号、现有代码和修复意见，适合 Agent 逐文件快速修复。</p><div class="commands"><span>git ls-files --full-name</span><span>git rev-parse HEAD</span></div></div></article></div></section>

    <div class="permission-note"><span>●</span><div><strong>权限与数据隔离</strong><p>普通用户只能读取账户邮箱在 GitLab 中拥有成员权限的项目；超管可以读取全部项目。MCP 不返回原始仓库内容，只返回审查问题及必要的修复上下文。</p></div></div>
  </div>
</template>

<style scoped>
.mcp-page{max-width:1180px;margin:0 auto}.hero{position:relative;display:flex;min-height:230px;justify-content:space-between;align-items:center;gap:38px;margin-bottom:18px;padding:34px 38px;overflow:hidden;border:1px solid #deddf7;border-radius:18px;background:linear-gradient(135deg,#fbfaff 0%,#f1f0ff 56%,#eef6ff 100%);box-shadow:0 12px 30px #464c7a0c}.hero::after{position:absolute;right:-80px;bottom:-110px;width:320px;height:320px;border-radius:50%;background:radial-gradient(circle,#7167e824 0,transparent 68%);content:""}.hero-content{position:relative;z-index:1;max-width:710px}.eyebrow{display:flex;align-items:center;gap:8px;color:#6c63d7;font-size:11px;font-weight:700;letter-spacing:1.3px;text-transform:uppercase}.pulse{width:8px;height:8px;border-radius:50%;background:#43c994;box-shadow:0 0 0 5px #43c9941c}.hero h1{margin:12px 0 10px;color:#292e46;font-size:31px;letter-spacing:-.7px}.hero p{margin:0;color:#737a91;font-size:13px;line-height:1.85}.hero-tags{display:flex;flex-wrap:wrap;gap:8px;margin-top:18px}.hero-tags span{padding:5px 9px;border:1px solid #d9d7f5;border-radius:7px;color:#665ec5;background:#ffffffa8;font-size:10px}.hero-action{position:relative;z-index:1;display:grid;min-width:210px;gap:15px}.connection-orb{display:flex;align-items:center;gap:10px;color:#7d8298;font-size:11px}.connection-orb i{display:grid;width:38px;height:38px;place-items:center;border-radius:11px;color:#fff;background:linear-gradient(135deg,#7468e9,#4d8ee8);box-shadow:0 8px 18px #665fd23d;font-size:19px;font-style:normal}.workflow{display:flex;align-items:center;justify-content:center;gap:18px;margin-bottom:18px;padding:14px 18px;border:1px solid #ebecef;border-radius:13px;background:#fff}.workflow>div{display:flex;align-items:center;gap:9px}.workflow b{display:grid;width:26px;height:26px;place-items:center;border-radius:8px;color:#6258d8;background:#eeecff;font-size:11px}.workflow span strong,.workflow span small{display:block}.workflow span strong{color:#50566d;font-size:11px}.workflow span small{margin-top:2px;color:#a0a5b4;font-size:9px}.workflow>i{color:#c3c6d1;font-style:normal}.notice{margin-bottom:18px;border-radius:10px}.credential-panel,.tool-section,.empty-credential{margin-bottom:18px;border:1px solid #e5e7ee;border-radius:16px;background:#fff;box-shadow:0 7px 22px #3b40640a}.credential-panel,.tool-section{padding:25px}.panel-heading,.section-heading{display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:18px}.panel-heading h2,.section-heading h2{margin:4px 0;color:#33394f;font-size:18px}.panel-heading p,.section-heading p{margin:0;color:#969bad;font-size:11px}.section-kicker{color:#7168d6;font-size:9px;font-weight:700;letter-spacing:1.5px}.field-grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.field-card{padding:16px;border:1px solid #e8e9ef;border-radius:11px;background:#fafbfe}.field-title{display:flex;justify-content:space-between;margin-bottom:12px}.field-title span{color:#555c72;font-size:12px;font-weight:600}.field-title small{color:#a1a6b5;font-size:9px}.value-row{display:flex;min-width:0;align-items:center;gap:7px}.value-row code{min-width:0;flex:1;overflow:hidden;color:#4f5871;font-size:10px;text-overflow:ellipsis;white-space:nowrap}.value-row button,.json-header button{padding:5px 8px;border:1px solid #dfe1ea;border-radius:6px;color:#6258d8;background:#fff;font-size:9px;cursor:pointer}.json-card{margin-top:15px;overflow:hidden;border:1px solid #dfe2e8;border-radius:11px;background:#202532}.json-header{display:flex;justify-content:space-between;align-items:center;padding:10px 13px;border-bottom:1px solid #343b4b;background:#292f3d}.json-header>div{display:flex;align-items:center;gap:5px}.json-header strong{margin-left:7px;color:#b8c0d2;font:10px ui-monospace,monospace}.window-dot{width:7px;height:7px;border-radius:50%}.red{background:#ff6b6b}.yellow{background:#f4bf4f}.green{background:#54c983}.json-header button{border-color:#495267;color:#d5dceb;background:#343b4b}.json-card pre{max-height:300px;margin:0;padding:17px;overflow:auto}.json-card code{color:#d6deeb;font:11px/1.7 ui-monospace,SFMono-Regular,Consolas,monospace;white-space:pre}.empty-credential{display:flex;align-items:center;gap:17px;padding:24px 28px}.empty-icon{display:grid;width:48px;height:48px;flex:0 0 auto;place-items:center;border-radius:13px;color:#6d63d8;background:#f0efff;font-size:22px}.empty-credential>div:nth-child(2){flex:1}.empty-credential h2{margin:0 0 5px;color:#43495f;font-size:15px}.empty-credential p{margin:0;color:#989dae;font-size:11px}.section-heading{display:block}.tool-grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.tool-grid article{display:flex;gap:13px;padding:17px;border:1px solid #e8e9ef;border-radius:12px;background:#fafbfe}.tool-icon{display:grid;width:38px;height:38px;flex:0 0 auto;place-items:center;border-radius:10px;font-size:18px}.tool-icon.branch{color:#6258d8;background:#efedff}.tool-icon.file{color:#2789b6;background:#eaf7fc}.tool-grid code{color:#4e54a2;font-size:11px;font-weight:700}.tool-grid p{margin:8px 0;color:#777e93;font-size:10px;line-height:1.65}.commands{display:flex;flex-wrap:wrap;gap:4px}.commands span{padding:3px 6px;border-radius:4px;color:#6a7187;background:#eceef4;font:8px ui-monospace,monospace}.permission-note{display:flex;gap:12px;padding:16px 18px;border:1px solid #dfece7;border-radius:12px;color:#4d7969;background:#f3fbf7}.permission-note>span{color:#39b985;font-size:10px}.permission-note strong{font-size:11px}.permission-note p{margin:4px 0 0;color:#739185;font-size:10px;line-height:1.6}@media(max-width:850px){.hero{align-items:flex-start;flex-direction:column}.hero-action{width:100%}.field-grid,.tool-grid{grid-template-columns:1fr}.workflow{align-items:flex-start;flex-direction:column}.workflow>i{display:none}}@media(max-width:560px){.hero{padding:25px}.credential-panel,.tool-section{padding:18px}.field-title{gap:7px;flex-direction:column}.empty-credential{align-items:flex-start;flex-direction:column}}
</style>
