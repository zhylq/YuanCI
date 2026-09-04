import type { ReactNode } from 'react'
import { Link, Navigate, useLocation } from 'react-router-dom'
import { useAuthStatus, useSession } from '../lib/auth'

export const buttonClass = 'inline-flex min-h-11 cursor-pointer items-center justify-center rounded-md bg-blue-700 px-4 py-2 text-sm font-semibold text-white hover:bg-blue-800 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:bg-slate-200 disabled:text-slate-600'
export const linkClass = 'rounded font-medium text-blue-700 underline underline-offset-4 hover:text-blue-900 focus-visible:outline-2 focus-visible:outline-blue-600'
export function Pending({ label = '正在读取安全配置…' }: { label?: string }) {
  return <div role="status" aria-busy="true" className="rounded-xl border border-slate-200 bg-white p-8 text-slate-600">{label}</div>
}
export function AuthBoundary({ children }: { children: ReactNode }) {
  const status = useAuthStatus()
  const location = useLocation()
  const session = useSession(status.data?.mode !== 'evaluation' && status.data?.configured === true)
  if (status.isPending) return <Pending />
  if (status.isError) return <div role="alert" className="space-y-4 p-8"><p>无法确认服务端安全状态，页面已暂停加载。</p><button className={buttonClass} onClick={() => void status.refetch()}>重新连接</button></div>
  if (status.data.mode === 'evaluation') return children
  if (location.pathname === '/setup') return status.data.mode === 'managed' && !status.data.initialized ? children : <Navigate to="/settings/auth" replace />
  if (status.data.mode === 'managed' && !status.data.initialized) return <Navigate to="/setup" replace />
  if (location.pathname === '/login') return children
  if (!status.data.configured) return <Navigate to="/login" replace />
  if (session.isPending) return <Pending label="正在验证登录状态…" />
  if (session.isError) return <div role="alert" className="p-8">登录服务暂不可用。<button className={linkClass} onClick={() => void session.refetch()}>重新验证</button></div>
  if (!session.data) return <Navigate to="/login" replace />
  return children
}
export function LoginPage() {
  const status = useAuthStatus()
  const provider = status.data?.provider === 'gitee' ? 'gitee' : 'github'
  const label = provider === 'gitee' ? 'Gitee' : 'GitHub'
  return <section className="mx-auto max-w-xl rounded-xl border border-slate-200 bg-white p-8 shadow-sm">
    <p className="text-sm font-medium text-blue-700">YuanCI · 团队登录</p>
    <h1 className="mt-2 text-balance text-3xl font-semibold">连接你的开发工作流</h1>
    <p className="mt-3 text-pretty leading-7 text-slate-600">使用团队管理员配置的 {label} 应用登录。普通成员无需创建应用，项目权限由管理员分配。</p>
    {status.data?.configured ? <a className={`${buttonClass} mt-6 w-full`} href={`/api/v1/auth/${provider}/start`}>使用 {label} 登录</a> : <p role="status" className="mt-6 rounded-lg bg-slate-100 p-4">尚未配置可用的登录方式，请联系部署管理员。</p>}
    <p className="mt-6 text-sm leading-6 text-slate-600">当前为开发预览；GitLab 与 Gitea 登录尚未接入。</p>
    {status.data?.mode === 'evaluation' ? <Link to="/" className={linkClass}>返回本地体验控制台</Link> : null}
  </section>
}
