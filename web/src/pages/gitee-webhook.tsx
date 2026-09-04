import { useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { request } from '../lib/api'
import { buttonClass, Pending } from '../components/auth-boundary'

export function GiteeWebhookControls({ projectID, csrf, onSaved }: { projectID: string; csrf: string; onSaved: () => void }) {
  const base = `/api/v1/projects/${projectID}/gitee/webhook`
  const settings = useQuery({ queryKey: ['gitee-webhook', projectID], queryFn: ({ signal }) => request<{ webhook_url: string; revision: number; configured: boolean }>(base, { signal }), retry: false, gcTime: 0 })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const input = event.currentTarget.elements.namedItem('secret') as HTMLInputElement
    const secret = input.value; input.value = ''
    if (new TextEncoder().encode(secret).length < 32) { setError('请使用至少 32 字节的随机密码。'); return }
    setBusy(true); setError('')
    try { await request(base, { method: 'PUT', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: JSON.stringify({ secret, expected_revision: settings.data!.revision }) }); await settings.refetch(); onSaved() }
    catch (error) { setError(error instanceof Error ? error.message : '保存失败，请重试。') }
    finally { setBusy(false) }
  }
  if (settings.isPending) return <Pending label="正在读取 Gitee Webhook 配置…" />
  if (settings.isError) return <p role="alert">{settings.error.message}</p>
  return <section className="rounded-xl border border-slate-200 bg-white p-5 sm:p-6"><h2 className="text-xl font-semibold">Gitee Webhook</h2><p className="mt-3 leading-7 text-slate-600">在 Gitee 仓库管理 → WebHooks 中添加以下地址，选择密码验证模式，订阅 Push、Tag Push 和 Merge Request。两边填写相同随机密码；更换后需要重新验证流水线。</p><label htmlFor="gitee-hook-url" className="mt-4 block text-sm font-semibold">Webhook 地址</label><input id="gitee-hook-url" readOnly value={settings.data.webhook_url} className="mt-2 min-h-11 w-full rounded border border-slate-300 px-3 font-mono text-sm" /><p className="mt-3 text-sm">{settings.data.configured ? `密码已配置 · 版本 ${settings.data.revision}` : '尚未配置密码'}</p><form onSubmit={event => void save(event)} className="mt-4" aria-busy={busy}><label htmlFor="gitee-hook-secret" className="text-sm font-semibold">Gitee Webhook 密码</label><input id="gitee-hook-secret" name="secret" type="password" autoComplete="new-password" maxLength={4096} required disabled={busy} aria-describedby="gitee-hook-error" className="mt-2 min-h-11 w-full rounded border border-slate-300 px-3" /><button className={`${buttonClass} mt-3`} disabled={busy}>保存 Webhook 密码</button><p id="gitee-hook-error" role={error ? 'alert' : undefined} className="mt-3 text-red-800">{error}</p></form></section>
}
