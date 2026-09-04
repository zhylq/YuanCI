import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { request } from '../lib/api'
import { navigateToAuthorization, post, type Session } from '../lib/auth'
import { buttonClass, linkClass, Pending } from '../components/auth-boundary'

const base = '/api/v1/integrations/gitee'
const panel = 'rounded-xl border border-slate-200 bg-white p-5 sm:p-6'
type Settings = { authorization: { id: string; status: string; expires_at: string } | null; callback_url: string }
type Repository = { id: string; owner: string; name: string; default_branch: string; private: boolean }
type Page = { items: Repository[]; next_page?: number }
function message(error: unknown) { return error instanceof Error ? error.message : '请求未完成，请重试。' }

export function GiteeRepositorySettings({ session }: { session: Session }) {
  const cache = useQueryClient()
  const settings = useQuery({ queryKey: ['gitee-settings', session.user_id], queryFn: ({ signal }) => request<Settings>(base, { signal }), retry: false, gcTime: 0 })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function authorize() {
    setBusy(true); setError('')
    try { const result = await post<{ authorization_url: string }>(`${base}/authorize`, {}, session.csrf_token); navigateToAuthorization(result.authorization_url) }
    catch (error) { setError(message(error)); setBusy(false) }
  }
  async function revoke() {
    setBusy(true); setError('')
    try { await request(base, { method: 'DELETE', headers: { 'X-CSRF-Token': session.csrf_token } }); cache.removeQueries({ queryKey: ['gitee-repositories'] }); await settings.refetch() }
    catch (error) { setError(message(error)) }
    finally { setBusy(false) }
  }
  if (settings.isPending || settings.isFetching) return <Pending />
  if (settings.isError) return <section className={panel}><p role="alert">{message(settings.error)}</p><button className={`${buttonClass} mt-4`} onClick={() => void settings.refetch()}>重新读取</button></section>
  const active = settings.data.authorization?.status === 'active'
  return <div className="space-y-6"><header><p className="text-sm text-blue-700">设置 / 仓库接入</p><h1 className="mt-2 text-balance text-3xl font-semibold">把 Gitee 仓库接入 YuanCI</h1><p className="mt-3 leading-7 text-slate-600">使用当前管理员绑定的 Gitee 账号授权，仅列出你拥有管理权限的仓库。</p></header>
    <section className={panel}><h2 className="text-xl font-semibold">授权仓库访问</h2><p className="mt-3 leading-7 text-slate-600">在已配置的 Gitee OAuth 应用中启用 user_info 和 projects 权限。仓库授权独立于登录，回调地址与登录相同。</p><label htmlFor="gitee-callback" className="mt-4 block text-sm font-semibold">Gitee 回调地址</label><input id="gitee-callback" readOnly value={settings.data.callback_url} className="mt-2 min-h-11 w-full rounded border border-slate-300 px-3 font-mono text-sm" /><div className="mt-4 flex flex-wrap gap-3"><button className={buttonClass} disabled={busy} onClick={() => void authorize()}>{active ? '重新授权 Gitee 仓库' : '授权 Gitee 仓库'}</button>{active ? <button className={buttonClass} disabled={busy} onClick={() => void revoke()}>撤销本实例仓库授权</button> : null}</div><p className="mt-3 text-sm leading-6 text-slate-600">令牌加密保存并自动续期；撤销后停止使用凭据，已导入项目保留。要撤销平台授权，请同时在 Gitee 应用设置中操作。</p>{error ? <p role="alert" className="mt-3 text-red-800">{error}</p> : null}</section>
    {active && !busy ? <GiteePicker key={settings.data.authorization!.id} session={session} /> : null}
    <Link className={linkClass} to="/projects">查看已导入项目</Link>
  </div>
}

function GiteePicker({ session }: { session: Session }) {
  const [page, setPage] = useState(1)
  const result = useQuery({ queryKey: ['gitee-repositories', session.user_id, page], queryFn: ({ signal }) => request<Page>(`${base}/repositories?page=${page}`, { signal }), retry: false, gcTime: 0 })
  if (result.isPending || result.isFetching) return <Pending label="正在读取 Gitee 仓库…" />
  if (result.isError) return <section className={panel}><p role="alert">{message(result.error)}</p><button className={`${buttonClass} mt-4`} onClick={() => void result.refetch()}>重新读取仓库</button></section>
  return <section className={panel}><GiteeSelection key={`${page}:${result.dataUpdatedAt}`} items={result.data.items} csrf={session.csrf_token} /><nav aria-label="Gitee 仓库分页" className="mt-5 flex flex-wrap items-center gap-3"><button className={buttonClass} disabled={page === 1} onClick={() => setPage(page - 1)}>上一页</button><span className="tabular-nums">第 {page} 页</span><button className={buttonClass} disabled={!result.data.next_page} onClick={() => setPage(result.data.next_page!)}>下一页</button></nav></section>
}
function GiteeSelection({ items, csrf }: { items: Repository[]; csrf: string }) {
  const [selected, setSelected] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [imported, setImported] = useState<Array<{ id: string; name: string }>>([])
  async function save() {
    setBusy(true); setError('')
    try { const result = await post<{ items: Array<{ id: string; name: string }> }>(`${base}/import`, { repositories: items.filter(item => selected.includes(item.id)).map(({ id, owner, name }) => ({ id, owner, name })) }, csrf); setImported(result.items); setSelected([]) }
    catch (error) { setError(message(error)) }
    finally { setBusy(false) }
  }
  return <><h2 className="text-xl font-semibold">选择并导入仓库</h2><fieldset disabled={busy} className="mt-4 space-y-2"><legend className="sr-only">Gitee 仓库，最多选择 20 个</legend>{items.length ? items.map(item => <label key={item.id} className="flex min-h-11 items-center gap-3 break-all"><input type="checkbox" checked={selected.includes(item.id)} disabled={!selected.includes(item.id) && selected.length >= 20} onChange={event => setSelected(event.target.checked ? [...selected, item.id] : selected.filter(id => id !== item.id))} /><span>{item.owner}/{item.name} · {item.private ? '私有' : '公开'} · {item.default_branch}</span></label>) : <p>本页没有具备管理权限的仓库。</p>}<button className={`${buttonClass} mt-4`} disabled={busy || selected.length === 0} onClick={() => void save()}>{busy ? '正在导入…' : '导入选中仓库'}</button></fieldset>{error ? <p role="alert" className="mt-3 text-red-800">{error}</p> : null}{imported.length ? <div role="status" className="mt-4"><p>已导入：</p><ul>{imported.map(item => <li key={item.id}><Link className={linkClass} to={`/projects/${item.id}`}>{item.name}</Link></li>)}</ul></div> : null}</>
}
