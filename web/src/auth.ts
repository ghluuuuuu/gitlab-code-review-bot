import { reactive } from 'vue'

export type AuthUser = {
  id: number
  username: string
  name: string
  email: string
  role: 'superadmin' | 'user'
  roles: string[]
  permissions: string[]
  enabled: boolean
  auth_source: 'local' | 'oidc'
}

type AuthConfig = { enabled: boolean; setup_required: boolean; oidc_enabled: boolean; oidc_label: string }

export const authState = reactive<{ ready: boolean; config: AuthConfig; user: AuthUser | null }>({
  ready: false,
  config: { enabled: false, setup_required: false, oidc_enabled: false, oidc_label: 'OIDC 登录' },
  user: null,
})

let initializePromise: Promise<void> | null = null

const responseError = async (response: Response, fallback: string) => {
  try {
    const body = await response.json() as { error?: { message?: string } }
    return body.error?.message || fallback
  } catch {
    return fallback
  }
}
export const initializeAuth = () => {
  if (authState.ready) return Promise.resolve()
  if (initializePromise) return initializePromise
  initializePromise = (async () => {
    try {
      const response = await fetch('/api/v1/auth/config', { credentials: 'same-origin' })
      if (response.ok) authState.config = await response.json() as AuthConfig
      const me = await fetch('/api/v1/admin/me', { credentials: 'same-origin' })
      if (me.ok) {
        const value = await me.json() as Partial<AuthUser> & { roles?: string[] }
        const role = value.role ?? (value.roles?.includes('admin') ? 'superadmin' : 'user')
        authState.user = { id: value.id ?? 0, username: value.username ?? value.name ?? 'admin', name: value.name ?? value.username ?? 'admin', email: value.email ?? '', role, roles: value.roles ?? [role], permissions: value.permissions ?? [], enabled: value.enabled ?? true, auth_source: value.auth_source ?? 'local' }
      } else {
        authState.user = null
      }
    } finally {
      authState.ready = true
    }
  })()
  return initializePromise
}

export const login = async (identifier: string, password: string) => {
  const response = await fetch('/api/v1/auth/login', {
    method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
    body: JSON.stringify({ identifier, password }),
  })
  if (!response.ok) throw new Error(await responseError(response, '账户名、邮箱或密码错误'))
  authState.user = await response.json() as AuthUser
}

export const setupSuperadmin = async (username: string, email: string, password: string) => {
  const response = await fetch('/api/v1/auth/setup', {
    method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
    body: JSON.stringify({ username, email, password }),
  })
  if (!response.ok) throw new Error(await responseError(response, '创建超管失败'))
  authState.user = await response.json() as AuthUser
}

export const logout = async () => {
  await fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'same-origin', headers: { 'X-Requested-With': 'XMLHttpRequest' } })
  authState.user = null
}

export const isSuperadmin = () => authState.user?.role === 'superadmin'
