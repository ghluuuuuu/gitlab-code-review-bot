<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import { ElMessage, ElMessageBox } from 'element-plus'
import { formatDate, getJSON } from '../adminApi'
import { authState } from '../auth'

type User = { id: number; username: string; email: string; role: 'superadmin' | 'user'; enabled: boolean; auth_source: 'local' | 'oidc'; created_at: string; last_login_at?: string }
const users = useQuery({ queryKey: ['users'], queryFn: () => getJSON<User[]>('/api/v1/admin/users') })
const visible = ref(false)
const editingID = ref<number | null>(null)
const saving = ref(false)
const form = reactive({ username: '', email: '', password: '', role: 'user' as 'superadmin' | 'user', enabled: true })
const reset = () => { editingID.value = null; Object.assign(form, { username: '', email: '', password: '', role: 'user', enabled: true }); visible.value = true }
const edit = (user: User) => { editingID.value = user.id; Object.assign(form, { username: user.username, email: user.email, password: '', role: user.role, enabled: user.enabled }); visible.value = true }
const save = async () => {
  saving.value = true
  try {
    await getJSON(editingID.value ? `/api/v1/admin/users/${editingID.value}` : '/api/v1/admin/users', { method: editingID.value ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(form) })
    ElMessage.success(editingID.value ? '用户已更新' : '用户已创建')
    visible.value = false
    await users.refetch()
  } catch (error) { ElMessage.error(error instanceof Error ? error.message : String(error)) } finally { saving.value = false }
}
const remove = async (user: User) => {
  try {
    await ElMessageBox.confirm(`确认删除用户 ${user.username}？`, '删除用户', { type: 'warning' })
    await getJSON(`/api/v1/admin/users/${user.id}`, { method: 'DELETE' })
    ElMessage.success('用户已删除')
    await users.refetch()
  } catch (error) { if (error !== 'cancel' && error !== 'close') ElMessage.error(error instanceof Error ? error.message : String(error)) }
}
</script>

<template>
  <div class="page-heading"><div><h1>用户管理</h1><p>管理本地账户、角色和登录状态；OIDC 用户首次登录后自动创建。</p></div><el-button type="primary" @click="reset">创建用户</el-button></div>
  <el-card shadow="never" class="table-card"><el-table :data="users.data.value ?? []" v-loading="users.isLoading.value" stripe>
    <el-table-column label="账户" min-width="180"><template #default="scope"><b>{{ scope.row.username }}</b><small>{{ scope.row.email }}</small></template></el-table-column>
    <el-table-column label="角色" width="120"><template #default="scope"><el-tag :type="scope.row.role === 'superadmin' ? 'danger' : 'primary'">{{ scope.row.role === 'superadmin' ? '超管' : '普通用户' }}</el-tag></template></el-table-column>
    <el-table-column label="来源" width="90"><template #default="scope">{{ scope.row.auth_source === 'oidc' ? 'OIDC' : '本地' }}</template></el-table-column>
    <el-table-column label="状态" width="90"><template #default="scope"><el-tag :type="scope.row.enabled ? 'success' : 'info'" effect="light">{{ scope.row.enabled ? '启用' : '禁用' }}</el-tag></template></el-table-column>
    <el-table-column label="最近登录" width="175"><template #default="scope">{{ formatDate(scope.row.last_login_at) }}</template></el-table-column>
    <el-table-column label="创建时间" width="175"><template #default="scope">{{ formatDate(scope.row.created_at) }}</template></el-table-column>
    <el-table-column label="操作" width="130"><template #default="scope"><el-button link type="primary" @click="edit(scope.row)">编辑</el-button><el-button link type="danger" :disabled="scope.row.id === authState.user?.id" @click="remove(scope.row)">删除</el-button></template></el-table-column>
  </el-table></el-card>
  <el-dialog v-model="visible" :title="editingID ? '编辑用户' : '创建用户'" width="520px" destroy-on-close>
    <el-form label-position="top"><el-form-item label="账户名"><el-input v-model="form.username" /></el-form-item><el-form-item label="邮箱"><el-input v-model="form.email" type="email" /></el-form-item><el-form-item :label="editingID ? '新密码（留空则不修改）' : '密码（至少 10 位）'"><el-input v-model="form.password" type="password" show-password /></el-form-item><el-form-item label="角色"><el-select v-model="form.role"><el-option label="普通用户" value="user" /><el-option label="超管" value="superadmin" /></el-select></el-form-item><el-form-item label="账户状态"><el-switch v-model="form.enabled" active-text="启用" inactive-text="禁用" /></el-form-item></el-form>
    <template #footer><el-button @click="visible=false">取消</el-button><el-button type="primary" :loading="saving" @click="save">保存</el-button></template>
  </el-dialog>
</template>

<style scoped>
.page-heading{display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:22px}.page-heading h1{margin:0 0 7px;font-size:25px}.page-heading p{margin:0;color:#9499ad;font-size:13px}.table-card{border:1px solid #ebedf4;border-radius:12px}.el-table b,.el-table small{display:block}.el-table small{margin-top:5px;color:#9298aa}.el-select{width:100%}
</style>
