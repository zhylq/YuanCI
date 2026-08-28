import { useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { ApiError, request } from '../lib/api'
import { navigateToAuthorization, post, useAuthStatus, useSession, type Session } from '../lib/auth'
import { buttonClass, linkClass, Pending } from '../components/auth-boundary'

export type ImportSettings = {
  app: { id: string; app_id: string; client_id: string; slug: string } | null
  needs_verification: boolean
  callback_url: string
  setup_url: string
  install_url?: string
  authorized_until?: string
}
type Installation = { id: string; account_id: string; account: string }
type Repository = { id: string; owner: string; name: string; default_branch: string }
type RepoPage = { items: Repository[]; next_page?: number }
const base = '/api/v1/integrations/github'
const panel = 'rounded-xl border border-slate-200 bg-white p-5 sm:p-6'
const field = 'mt-2 min-h-11 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-base focus:outline-2 focus:outline-blue-600'
const fresh = { retry: false, gcTime: 0, staleTime: 0, refetchOnWindowFocus: true }

function importError(error: unknown) {
  if (error instanceof ApiError && error.status === 401) return '请重新登录。配置私钥和发起授权要求登录时间不超过 10 分钟。'
  if (error instanceof ApiError && error.status === 403) return '权限不足。需要 YuanCI 实例管理员身份；发现仓库还需当前 GitHub 账号的仓库管理员权限。'
  if (error instanceof ApiError && error.status === 409) return '配置、授权已变化或过期，或项目归属存在冲突。请刷新设置并重新授权；系统不会自动迁移已有项目。'
  return error instanceof ApiError ? error.message : '请求未完成，请检查网络后重试。'
}
function ReadError({ error, retry }: { error: unknown; retry: () => void }) {
  return <section className={panel}><p role="alert" className="text-pretty leading-7 text-red-800">{importError(error)}</p><div className="mt-4 flex flex-wrap items-center gap-4"><button className={buttonClass} onClick={retry}>重新读取</button><Link className={linkClass} to="/login">重新登录</Link></div></section>
}
export function RepositorySettingsPage() {
  const status = useAuthStatus()
  const session = useSession(status.data?.mode === 'managed' && status.data.configured)
  if (status.isPending) return <Pending />
  if (status.isError) return <ReadError error={status.error} retry={() => void status.refetch()} />
  if (status.data.mode !== 'managed') return <section className={panel}><h1 className="text-balance text-2xl font-semibold">仓库接入需要受保护配置模式</h1><p className="mt-3 text-pretty leading-7 text-slate-600">免登录 Quickstart 和文件登录模式不允许在网页保存 App 私钥。请按 docs/managed-setup.zh-CN.md 启动独立的 managed Compose；不需要安装 Go。</p><Link className={`${linkClass} mt-4 inline-flex min-h-11 items-center`} to="/settings/auth">查看登录设置</Link></section>
  if (session.isPending) return <Pending label="正在验证管理员会话…" />
  if (session.isError || !session.data) return <ReadError error={session.error ?? new ApiError('', 401)} retry={() => void session.refetch()} />
  return <ManagedImport key={session.data.user_id} session={session.data} />
}
function ManagedImport({ session }: { session: Session }) {
  const settings = useQuery({ ...fresh, queryKey: ['import-settings', session.user_id], queryFn: ({ signal }) => request<ImportSettings>(base, { signal }), refetchInterval: 30_000 })
  return <div className="space-y-6"><header><p className="text-sm font-medium text-blue-700">设置 / 仓库接入</p><h1 className="mt-2 text-balance text-3xl font-semibold">把 GitHub 仓库接入 YuanCI</h1><p className="mt-3 max-w-3xl text-pretty leading-7 text-slate-600">使用已配置登录的同一个 GitHub App。先验证应用，再安装并授权，最后选择仓库。本阶段只导入仓库资料，不会开始构建或部署。</p></header>
    <ol aria-label="仓库接入步骤" className="grid gap-3 rounded-xl border border-slate-200 bg-white p-4 text-sm sm:grid-cols-3"><li>1. 验证 App 私钥</li><li>2. 安装并授权发现</li><li>3. 选择并导入仓库</li></ol>
    {settings.isPending ? <Pending /> : settings.isError ? <ReadError error={settings.error} retry={() => void settings.refetch()} /> : <>
      <Instructions settings={settings.data} />
      <AppForm key={settings.data.app?.id ?? 'new'} settings={settings.data} csrf={session.csrf_token} onSaved={() => void settings.refetch()} />
      {settings.data.app && !settings.data.needs_verification ? <Discovery key={`${settings.data.app.id}:${settings.data.authorized_until ?? 'pending'}`} settings={settings.data} session={session} /> : <p className="text-pretty text-sm leading-6 text-slate-600">完成私钥验证后，将出现安装应用与授权发现入口。</p>}
    </>}
    <p className="text-pretty text-sm leading-6 text-slate-600">Gitee、GitLab、Gitea 仍待接入；登录和导入都不会自动给其他成员授予项目权限。<Link className={linkClass} to="/projects">查看已导入项目</Link></p>
  </div>
}
function Instructions({ settings }: { settings: ImportSettings }) {
  return <section className={panel}><h2 className="text-balance text-xl font-semibold">先在 GitHub 中完成这些设置</h2><ol className="mt-4 list-decimal space-y-3 pl-5 text-pretty leading-7 text-slate-700">
    <li>打开 GitHub → Settings → Developer settings → GitHub Apps，编辑已用于 YuanCI 登录的应用。无需创建第二个应用。</li>
    <li>在 Callback URL 列表中保留原登录回调，并新增下方“仓库授权回调”。Setup URL 可填下方地址；不要开启“Redirect on update”。</li>
    <li>Repository permissions 本轮只需 Metadata: Read-only。关闭 Webhook 的 Active，保留用户 Token 过期设置，不申请仓库写入或组织管理权限。</li>
    <li>在 General 中复制 App ID，使用 Generate a private key 生成 PEM 私钥。私钥不能提交到 Git 仓库，也不要发给其他成员。</li>
  </ol><div className="mt-5 grid min-w-0 gap-4"><label className="text-sm font-semibold" htmlFor="import-callback">仓库授权回调（新增 Callback URL）<input id="import-callback" className={`${field} font-mono text-sm`} value={settings.callback_url} readOnly /></label><label className="text-sm font-semibold" htmlFor="import-setup">安装后返回地址（Setup URL，可选）<input id="import-setup" className={`${field} font-mono text-sm`} value={settings.setup_url} readOnly /></label></div><p className="mt-3 text-pretty text-sm leading-6 text-slate-600">安装回调中的 installation_id 不作为授权依据；返回后仍须主动授权发现仓库。</p><a className={`${linkClass} mt-3 inline-flex min-h-11 items-center`} href="https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app" target="_blank" rel="noreferrer">GitHub App 官方设置教程</a></section>
}
function AppForm({ settings, csrf, onSaved }: { settings: ImportSettings; csrf: string; onSaved: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [invalid, setInvalid] = useState(false)
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const app = form.elements.namedItem('app_id') as HTMLInputElement
    const key = form.elements.namedItem('private_key') as HTMLTextAreaElement
    const id = app.value.trim(); const privateKey = key.value
    const invalid = !/^[1-9][0-9]{0,18}$/.test(id) || !privateKey.trim()
    setInvalid(invalid); setError('')
    if (invalid) { setError('请填写正整数 App ID 和完整 PEM 私钥。'); return }
    // Do not retain the key in React state, query cache or browser storage.
    key.value = ''; setBusy(true)
    try { await post(base, { app_id: id, private_key: privateKey, expected_revision: settings.app?.id ?? null }, csrf); onSaved() }
    catch (error) { setError(importError(error)) }
    finally { setBusy(false) }
  }
  return <section className={panel}><h2 id="app-form-title" className="text-balance text-xl font-semibold">1. {settings.app ? 'App 已配置 · 可重新验证私钥' : '验证 App 私钥'}</h2>{settings.app ? <p className="mt-3 break-all text-pretty text-sm leading-6 text-slate-600">应用：{settings.app.slug} · Client ID：{settings.app.client_id} · 私钥已加密，不回显。重新保存会让已有仓库发现授权失效，不删除项目。</p> : null}{settings.needs_verification ? <p role="alert" className="mt-3 text-pretty text-sm text-red-800">登录配置已更换，请重新验证私钥后再授权发现仓库。</p> : null}
    <form onSubmit={event => void save(event)} aria-labelledby="app-form-title" aria-busy={busy} noValidate className="mt-5"><fieldset disabled={busy} className="space-y-4"><div><label htmlFor="app-id" className="text-sm font-semibold">App ID</label><input id="app-id" name="app_id" defaultValue={settings.app?.app_id ?? ''} required inputMode="numeric" autoComplete="off" maxLength={19} className={field} aria-invalid={invalid} aria-describedby="app-key-help app-error" /></div><div><label htmlFor="app-key" className="text-sm font-semibold">RSA 私钥（PEM）</label><textarea id="app-key" name="private_key" required rows={5} maxLength={16384} autoComplete="off" spellCheck={false} className={`${field} font-mono text-sm`} aria-invalid={invalid} aria-describedby="app-key-help app-error" /></div><p id="app-key-help" className="text-pretty text-sm leading-6 text-slate-600">支持 2048–4096 位 RSA。提交后输入框清空；验证失败重试需重新粘贴。要求最近 10 分钟内登录。</p><p id="app-error" role={error ? 'alert' : undefined} className="text-pretty text-sm leading-6 text-red-800">{error}</p><button className={buttonClass} disabled={busy}>{busy ? '正在向 GitHub 验证…' : '验证并加密保存'}</button></fieldset></form>
  </section>
}
function Discovery({ settings, session }: { settings: ImportSettings; session: Session }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // The server omits expired proofs; settings refresh periodically and every
  // discovery/import request independently rechecks the database deadline.
  const authorized = Boolean(settings.authorized_until)
  async function authorize() {
    setBusy(true); setError('')
    try { const reply = await post<{ authorization_url: string }>(`${base}/authorize`, {}, session.csrf_token); navigateToAuthorization(reply.authorization_url) }
    catch (error) { setError(importError(error)); setBusy(false) }
  }
  return <><section className={panel}><h2 className="text-balance text-xl font-semibold">2. 安装应用，授权发现仓库</h2><p className="mt-3 text-pretty leading-7 text-slate-600">先在 GitHub 选择安装账号与仓库范围，再回到本页授权。必须使用当前 YuanCI 管理员已绑定的 GitHub 账号；仅显示你拥有管理员权限的仓库。</p><div className="mt-4 flex flex-wrap items-center gap-4">{settings.install_url ? <a className={linkClass} href={settings.install_url} target="_blank" rel="noreferrer">在 GitHub 安装或调整仓库范围</a> : null}<button className={buttonClass} onClick={() => void authorize()} disabled={busy}>{busy ? '正在准备授权…' : authorized ? '重新授权发现仓库' : '授权发现仓库'}</button></div><p className="mt-3 text-pretty text-sm leading-6 text-slate-600">授权最多保留 10 分钟，仅供当前浏览器会话使用，不会修改登录身份。到期后可重新授权。</p>{authorized ? <p role="status" className="mt-3 text-sm text-blue-800">已授权至 <time className="tabular-nums" dateTime={settings.authorized_until}>{new Date(settings.authorized_until!).toLocaleTimeString()}</time></p> : null}{error ? <p role="alert" className="mt-3 text-pretty text-sm text-red-800">{error}</p> : null}</section>
    {authorized && !busy ? <InstallationPicker session={session} authorization={settings.authorized_until!} /> : null}</>
}
function InstallationPicker({ session, authorization }: { session: Session; authorization: string }) {
  const [selected, setSelected] = useState('')
  const installations = useQuery({ ...fresh, queryKey: ['import-installations', session.user_id, authorization], queryFn: ({ signal }) => request<{ items: Installation[] }>(`${base}/installations`, { signal }) })
  if (installations.isFetching || installations.isPending) return <Pending label="正在验证可访问的 GitHub 安装…" />
  if (installations.isError) return <ReadError error={installations.error} retry={() => void installations.refetch()} />
  if (!installations.data.items.length) return <section className={panel}><h2 className="text-balance text-xl font-semibold">尚未找到可访问的安装</h2><p className="mt-3 text-pretty leading-7 text-slate-600">请先在 GitHub 安装应用，确认账号与仓库范围。</p><button className={`${buttonClass} mt-4`} onClick={() => void installations.refetch()}>安装后重新读取</button></section>
  const installation = installations.data.items.find(item => item.id === selected)
  return <section className={panel}><h2 className="text-balance text-xl font-semibold">3. 选择并导入仓库</h2><label htmlFor="installation" className="mt-5 block text-sm font-semibold">GitHub 安装账号</label><select id="installation" className={field} value={installation?.id ?? ''} onChange={event => setSelected(event.target.value)}><option value="">请选择安装账号</option>{installations.data.items.map(item => <option key={item.id} value={item.id}>{item.account} · 安装 {item.id}</option>)}</select>{installation ? <RepositoryPicker key={installation.id} session={session} installation={installation} authorization={authorization} /> : null}</section>
}
function RepositoryPicker({ session, installation, authorization }: { session: Session; installation: Installation; authorization: string }) {
  const [page, setPage] = useState(1)
  const repos = useQuery({ ...fresh, queryKey: ['import-repositories', session.user_id, authorization, installation.id, page], queryFn: ({ signal }) => request<RepoPage>(`${base}/installations/${encodeURIComponent(installation.id)}/repositories?page=${page}`, { signal }) })
  if (repos.isFetching || repos.isPending) return <div className="mt-5"><Pending label="正在读取有管理员权限的仓库…" /></div>
  if (repos.isError) return <div className="mt-5"><ReadError error={repos.error} retry={() => void repos.refetch()} /></div>
  return <div className="mt-5"><Selection key={`${page}:${repos.dataUpdatedAt}`} items={repos.data.items} installation={installation} csrf={session.csrf_token} /><nav aria-label="GitHub 仓库分页" className="mt-5 flex flex-wrap items-center gap-3"><button className={buttonClass} disabled={page === 1} onClick={() => setPage(1)}>回到首页</button><span className="tabular-nums text-sm text-slate-600">第 {page} 页</span><button className={buttonClass} disabled={!repos.data.next_page} onClick={() => setPage(repos.data.next_page!)}>下一页</button></nav></div>
}
function Selection({ items, installation, csrf }: { items: Repository[]; installation: Installation; csrf: string }) {
  const client = useQueryClient()
  const [selected, setSelected] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<Array<{ id: string; name: string; created: boolean }>>([])
  async function importSelected() {
    setBusy(true); setError(''); setResult([])
    try {
      const reply = await post<{ items: typeof result }>(`${base}/import`, { installation_id: installation.id, repository_ids: selected }, csrf)
      setResult(reply.items); setSelected([]); void client.invalidateQueries({ queryKey: ['projects'] })
    } catch (error) { setError(importError(error)) }
    finally { setBusy(false) }
  }
  return <div aria-busy={busy}>{items.length ? <fieldset disabled={busy}><legend className="text-sm font-semibold">本页可导入的仓库</legend><ul className="mt-3 divide-y divide-slate-200 rounded-lg border border-slate-200">{items.map(repo => <li key={repo.id}><label className="flex min-h-11 cursor-pointer items-start gap-3 p-3"><input type="checkbox" className="mt-1 size-5 shrink-0 accent-blue-700" checked={selected.includes(repo.id)} disabled={!selected.includes(repo.id) && selected.length >= 20} onChange={event => setSelected(current => event.target.checked ? [...current, repo.id] : current.filter(id => id !== repo.id))} /><span className="min-w-0 break-all"><strong className="text-sm">{repo.owner}/{repo.name}</strong><span className="mt-1 block text-sm text-slate-600">默认分支：{repo.default_branch || '未设置'}</span></span></label></li>)}</ul></fieldset> : <p role="status" className="text-pretty leading-7 text-slate-600">本页没有你拥有管理员权限的仓库。检查安装范围、账号权限，或继续下一页。</p>}
    <p id="selection-help" className="mt-4 text-pretty text-sm leading-6 text-slate-600">每次最多选择 20 个当前页仓库，翻页会清空选择。导入到按 GitHub 账号隔离的组织；重复导入不重复创建，也不会迁移、启用已有项目或自动授权成员。</p><button className={`${buttonClass} mt-4`} disabled={busy || !selected.length} aria-describedby="selection-help" onClick={() => void importSelected()}>{busy ? '正在重新校验权限并导入…' : `导入所选仓库（${selected.length}）`}</button>
    {error ? <p role="alert" className="mt-3 text-pretty text-sm leading-6 text-red-800">{error}</p> : null}
    {result.length ? <div className="mt-4 rounded-lg bg-blue-50 p-4"><p role="status" className="text-pretty text-sm text-blue-900">导入完成。仅登记仓库资料，Webhook 与自动构建尚未接入。</p><ul className="mt-3 space-y-2">{result.map(item => <li key={item.id} className="break-all text-sm"><Link className={linkClass} to={`/projects/${item.id}`}>{item.name}</Link> · {item.created ? '已创建' : '已存在，保留原项目'}</li>)}</ul></div> : null}
  </div>
}
