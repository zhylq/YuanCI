import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../lib/api'
import { post, useSession } from '../lib/auth'
import { buttonClass, Pending } from '../components/auth-boundary'
import { GiteeWebhookControls } from './gitee-webhook'

type Settings = { enabled: boolean; pipeline_path: string; trigger_push: boolean; trigger_tag: boolean; trigger_pull_request: boolean; cancel_older_commits: boolean; revision: number }
const panel = 'rounded-xl border border-slate-200 bg-white p-5 sm:p-6'
export function ProjectAutomation({ projectID, userID, provider = 'github' }: { projectID: string; userID: string; provider?: string }) {
 const [hookRevision, setHookRevision] = useState(0)
 const session = useSession(true)
 const settings = useQuery({ queryKey: ['automation', userID, projectID], queryFn: ({ signal }) => request<Settings>(`/api/v1/projects/${projectID}/automation`, { signal }), retry: false, gcTime: 0 })
 if (settings.isPending || session.isPending) return <Pending label="正在读取自动构建设置…" />
 if (settings.isError || session.isError || !session.data) return <p role="alert">{settings.error?.message ?? '无法读取自动构建设置，请重新登录或刷新。'}</p>
 return <>{provider === 'gitee' ? <GiteeWebhookControls projectID={projectID} csrf={session.data.csrf_token} onSaved={() => setHookRevision(value => value + 1)} /> : null}<AutomationForm key={`${settings.data.revision}:${hookRevision}`} provider={provider} settings={settings.data} csrf={session.data.csrf_token} projectID={projectID} userID={userID} /></>
}
function AutomationForm({ settings, csrf, projectID, userID, provider }: { settings: Settings; csrf: string; projectID: string; userID: string; provider: string }) {
 const [draft, setDraft] = useState(settings)
 const [proof, setProof] = useState('')
 const [busy, setBusy] = useState(false)
 const [error, setError] = useState('')
 const client = useQueryClient()
 const base = `/api/v1/projects/${projectID}`
 const dirty = JSON.stringify(draft) !== JSON.stringify(settings)
 function change(next: Settings) { setDraft(next); setProof(''); setError('') }
 async function act(action: 'save' | 'validate' | 'enable' | 'disable') {
  setBusy(true); setError('')
  try {
   if (action === 'validate') {
    const result = await post<{ valid: boolean; commit_sha: string }>(`${base}/pipeline/validate`, { expected_revision: settings.revision }, csrf)
    if (!result.valid) throw new Error('配置未通过验证。')
    setProof(result.commit_sha)
   } else {
    const { revision, ...values } = action === 'save' ? draft : settings
    const result = await request<Settings>(`${base}/automation`, { method: 'PUT', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: JSON.stringify({ ...values, enabled: action === 'enable', expected_revision: revision }) })
    client.setQueryData(['automation', userID, projectID], result)
   }
  } catch (cause) { setProof(''); setError(cause instanceof Error ? cause.message : '请求失败，请刷新设置后重试。') }
  finally { setBusy(false) }
 }
 return <section className={panel} aria-labelledby="automation-title" aria-busy={busy}>
  <h2 id="automation-title" className="text-balance text-xl font-semibold">{provider === 'gitee' ? 'Gitee' : 'GitHub'} 自动构建</h2>
  <p className="mt-3 text-pretty text-sm leading-6">{settings.enabled ? '已启用' : '已停用'} · 修改需要项目维护权限。保存修改会停用自动构建，重新验证后才能启用。</p>
  <fieldset disabled={busy} className="mt-4 space-y-4">
   <label className="block text-sm font-semibold">配置文件路径<input className="mt-2 min-h-11 w-full rounded-md border border-slate-300 px-3" value={draft.pipeline_path} maxLength={256} onChange={e => change({ ...draft, pipeline_path: e.target.value })} aria-describedby="automation-error" /></label>
   <div className="flex flex-wrap gap-5">{([['trigger_push', '分支推送'], ['trigger_tag', '标签推送'], ['trigger_pull_request', 'Pull Request']] as const).map(([key, label]) => <label key={key} className="flex min-h-11 items-center gap-2"><input type="checkbox" checked={draft[key]} onChange={e => change({ ...draft, [key]: e.target.checked })} />{label}</label>)}</div>
   <p className="text-pretty text-sm text-slate-600">Fork 事件会被拒绝。更换 App 或修改配置后需要重新验证。</p>
   <div className="flex flex-wrap gap-3">
    <button className={buttonClass} disabled={!dirty} onClick={() => void act('save')}>保存设置</button>
    <button className={buttonClass} disabled={dirty || settings.enabled} onClick={() => void act('validate')}>验证当前配置</button>
    {settings.enabled ? <button className={buttonClass} onClick={() => void act('disable')}>停用自动构建</button> : <button className={buttonClass} disabled={dirty || !proof} onClick={() => void act('enable')}>启用自动构建</button>}
   </div>
  </fieldset>
  <p className="mt-3 text-pretty text-sm text-slate-600">启用前请先保存并验证当前配置。</p>
  {proof ? <p role="status" className="mt-3 break-all font-mono text-sm">已验证提交：{proof}</p> : null}
  <p id="automation-error" role={error ? 'alert' : undefined} className="mt-3 text-pretty text-sm text-red-800">{error}</p>
 </section>
}
