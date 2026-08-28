import { useQuery } from '@tanstack/react-query'
import { ApiError, request } from './api'

export type AuthStatus = { mode: 'evaluation' | 'file' | 'managed'; configured: boolean; initialized: boolean; callback_url: string }
export type Session = { user_id: string; display_name: string; expires_at: string; csrf_token: string }
export type LoginConfig = { id: string; client_id: string; bootstrap_subject: string; status: string; expires_at: string }
export type LoginSettings = { active: LoginConfig | null; candidate: LoginConfig | null; csrf_token: string; callback_url: string }

export function useAuthStatus() {
  return useQuery({ queryKey: ['auth-status'], queryFn: () => request<AuthStatus>('/api/v1/auth/status'), retry: false })
}
export function useSession(enabled: boolean) {
  return useQuery({ queryKey: ['session'], enabled, retry: false, queryFn: async () => {
    try { return await request<Session>('/api/v1/session') }
    catch (error) { if (error instanceof ApiError && error.status === 401) return null; throw error }
  } })
}
export function settingsPath(setup: boolean) { return setup ? '/api/v1/setup/settings' : '/api/v1/settings/auth' }
export function useLoginSettings(setup: boolean, enabled = true) {
  return useQuery({ queryKey: ['login-settings', setup], enabled, retry: false, gcTime: 0,
    queryFn: () => request<LoginSettings>(settingsPath(setup)) })
}
export function post<T>(path: string, value: unknown, csrf?: string) {
  return request<T>(path, { method: 'POST', headers: { 'Content-Type': 'application/json', ...(csrf ? { 'X-CSRF-Token': csrf } : {}) }, body: JSON.stringify(value) })
}
export function errorMessage(error: unknown) {
  if (error instanceof ApiError && error.status === 401) return '会话无效或超过安全操作时限，请重新登录；首次设置请重新获取设置码。'
  if (error instanceof ApiError && error.status === 403) return '权限不足：只有实例管理员可以管理登录配置。'
  return error instanceof ApiError ? error.message : '请求未完成，请检查网络后重试。'
}
export function navigateToAuthorization(value: string) {
  const url = new URL(value)
  if (url.origin !== 'https://github.com' || url.pathname !== '/login/oauth/authorize') throw new Error('Unexpected authorization URL')
  window.location.assign(url.href)
}
