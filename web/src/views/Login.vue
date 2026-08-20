<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { authState, login, setupSuperadmin } from '../auth'

const route = useRoute()
const router = useRouter()
const identifier = ref('')
const password = ref('')
const email = ref('')
const loading = ref(false)

const submit = async () => {
  if (!identifier.value.trim() || !password.value) return
  loading.value = true
  try {
    await login(identifier.value, password.value)
    const redirect = typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/') ? route.query.redirect : '/dashboard'
    await router.replace(redirect)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error))
  } finally {
    loading.value = false
  }
}
const setup = async () => {
  if (!identifier.value.trim() || !email.value.trim() || !password.value) return
  loading.value = true
  try {
    await setupSuperadmin(identifier.value, email.value, password.value)
    ElMessage.success('超管账户初始化完成')
    await router.replace('/dashboard')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : String(error))
  } finally {
    loading.value = false
  }
}

const oidcLogin = () => {
  const redirect = typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/') ? route.query.redirect : '/dashboard'
  window.location.href = `/api/v1/auth/oidc/login?return_to=${encodeURIComponent(redirect)}`
}
</script>

<template>
  <div class="login-page">
    <div class="login-panel">
      <div class="login-brand"><div class="brand-mark">O</div><div><strong>OCR Bot</strong><span>代码质量管理平台</span></div></div>
      <h1>{{ authState.config.setup_required ? '初始化超管' : '登录账户' }}</h1>
      <p>{{ authState.config.setup_required ? '首次启动需要创建超管账户，完成后才能进入控制台。' : '使用账户名或邮箱登录，访问已授权的 GitLab 项目。' }}</p>
      <el-form label-position="top" @submit.prevent="authState.config.setup_required ? setup() : submit()">
        <el-form-item :label="authState.config.setup_required ? '超管账户名' : '账户名或邮箱'"><el-input v-model="identifier" size="large" autocomplete="username" autofocus @keyup.enter="authState.config.setup_required ? setup() : submit()" /></el-form-item>
        <el-form-item v-if="authState.config.setup_required" label="超管邮箱"><el-input v-model="email" size="large" type="email" autocomplete="email" /></el-form-item>
        <el-form-item label="密码"><el-input v-model="password" size="large" type="password" show-password :autocomplete="authState.config.setup_required ? 'new-password' : 'current-password'" @keyup.enter="authState.config.setup_required ? setup() : submit()" /></el-form-item>
        <el-button type="primary" size="large" class="submit" :loading="loading" @click="authState.config.setup_required ? setup() : submit()">{{ authState.config.setup_required ? '创建超管并进入系统' : '登录' }}</el-button>
      </el-form>
      <div v-if="!authState.config.setup_required && authState.config.oidc_enabled" class="oidc"><span>或</span><el-button size="large" plain @click="oidcLogin">{{ authState.config.oidc_label }}</el-button></div>
    </div>
  </div>
</template>

<style scoped>
.login-page{display:grid;min-height:100vh;place-items:center;padding:24px;background:radial-gradient(circle at 20% 10%,#efedff 0,transparent 35%),#f6f7fb}.login-panel{width:min(430px,100%);padding:36px 38px;border:1px solid #e3e5ed;border-radius:18px;background:#fff;box-shadow:0 18px 55px #34365b18}.login-brand{display:flex;align-items:center;gap:12px;margin-bottom:30px}.login-brand strong,.login-brand span{display:block}.login-brand strong{font-size:18px}.login-brand span{margin-top:3px;color:#999fb1;font-size:11px}.brand-mark{display:grid;width:40px;height:40px;place-items:center;border-radius:11px;color:#fff;background:linear-gradient(135deg,#7267e8,#4e8de8);font-size:22px;font-weight:800}.login-panel h1{margin:0 0 8px;font-size:26px}.login-panel>p{margin:0 0 28px;color:#8d93a6;font-size:13px}.submit{width:100%}.oidc{display:grid;gap:12px;margin-top:22px}.oidc span{display:flex;align-items:center;gap:12px;color:#a4a9b8;font-size:11px}.oidc span::before,.oidc span::after{height:1px;flex:1;background:#e8eaf0;content:""}
</style>
