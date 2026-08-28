import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStatus, useSession, errorMessage } from '../lib/auth'
import { request } from '../lib/api'
import { buttonClass, linkClass, Pending } from '../components/auth-boundary'
import { DashboardPage } from './dashboard'
import { PipelineEditorPage } from './pipeline-editor'

export function HomePage() {
  const status = useAuthStatus()
  return status.data?.mode === 'evaluation' ? <DashboardPage /> : <AccountPage />
}
export function PipelinePage() {
  const status = useAuthStatus()
  const session = useSession(status.data?.mode !== 'evaluation')
  return <PipelineEditorPage csrfToken={session.data?.csrf_token} />
}
function AccountPage() {
  const session = useSession(true)
  const cache = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!session.data) return <Pending />
  async function logout() {
    setBusy(true); setError('')
    try {
      await request('/api/v1/session', { method: 'DELETE', headers: { 'X-CSRF-Token': session.data!.csrf_token } })
      cache.clear(); window.location.assign('/login')
    } catch (error) { setError(errorMessage(error)); setBusy(false) }
  }
  return <div className="space-y-6">
    <header><p className="text-sm font-medium text-blue-700">安全连接已建立</p><h1 className="mt-2 text-balance text-3xl font-semibold">你好，{session.data.display_name}</h1><p className="mt-3 text-pretty text-slate-600">已通过 GitHub 验证身份。登录成功不代表自动获得项目权限。</p></header>
    <section className="rounded-xl border border-slate-200 bg-white p-6 shadow-sm"><h2 className="text-balance text-lg font-semibold">账号与登录配置</h2><p className="mt-2 text-sm leading-6 text-slate-600">实例管理员可以维护第三方应用；普通成员无需填写 Client ID 或密钥。</p><Link className={`${linkClass} mt-4 inline-block`} to="/settings/auth">查看 Git 平台设置</Link><p className="mt-6 text-sm text-slate-600">会话到期：<time className="tabular-nums" dateTime={session.data.expires_at}>{new Date(session.data.expires_at).toLocaleString()}</time></p><button className={`${buttonClass} mt-4`} onClick={() => void logout()} disabled={busy}>{busy ? '正在安全退出…' : '退出登录'}</button>{error ? <p role="alert" className="mt-3 text-sm text-red-800">{error}</p> : null}</section>
    <section className="rounded-xl border border-dashed border-slate-300 p-6"><h2 className="text-balance text-lg font-semibold">下一站：连接项目</h2><p className="mt-2 text-pretty leading-7 text-slate-600">项目选择、仓库安装授权和 Runner mTLS 正在开发。当前受保护预览不会调用免登录 Runner，也不会展示未归属项目的运行记录。</p><Link className={`${linkClass} mt-4 inline-block`} to="/pipelines/new">先校验一份 Pipeline 配置</Link></section>
  </div>
}
