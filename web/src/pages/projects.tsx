import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { ApiError, request, type Run, type PlanJob } from '../lib/api'
import { post, useAuthStatus, useSession } from '../lib/auth'
import { buttonClass, linkClass, Pending } from '../components/auth-boundary'
import { StatusBadge } from '../components/status-badge'
import { ProjectAutomation } from './project-automation'

export type Project = {
  id: string
  organization: { id: string; name: string }
  provider: string
  owner: string
  name: string
  default_branch: string
  connection_status: 'not_connected' | 'metadata_verified'
}
type Page<T> = { items: T[]; next_cursor?: string }
type RunSummary = Omit<Run, 'config_sha256'>
const panel = 'rounded-xl border border-slate-200 bg-white p-5 sm:p-6'
const refreshPolicy = { retry: false, staleTime: 0, gcTime: 0, refetchOnMount: 'always' as const, refetchOnWindowFocus: true, refetchInterval: 30_000 }

// Protected data is never requested by the evaluation console. The outer
// AuthBoundary controls routing; this gate also protects isolated page mounts.
function ProjectAccess({ children }: { children: (userID: string) => ReactNode }) {
  const status = useAuthStatus()
  const protectedMode = status.data?.mode === 'file' || status.data?.mode === 'managed'
  const session = useSession(protectedMode && status.data?.configured === true)
  if (status.isPending) return <Pending />
  if (status.isError) return <ReadError error={status.error} retry={() => void status.refetch()} />
  if (!protectedMode) return <section className={panel}><h1 className="text-balance text-2xl font-semibold">项目浏览需要登录</h1><p className="mt-3 text-pretty leading-7 text-slate-600">当前是免登录体验模式。请按部署教程启动受保护预览；不会将体验运行记录当作已授权项目。</p><Link to="/settings/auth" className={`${linkClass} mt-4 inline-flex min-h-11 items-center`}>查看登录配置说明</Link></section>
  if (!status.data?.configured) return <ReadError error={new ApiError('', 401)} />
  if (session.isPending) return <Pending label="正在验证登录状态…" />
  if (session.isError) return <ReadError error={session.error} retry={() => void session.refetch()} />
  if (!session.data) return <ReadError error={new ApiError('', 401)} />
  return children(session.data.user_id)
}

function ReadError({ error, retry }: { error: unknown; retry?: () => void }) {
  const code = error instanceof ApiError ? error.status : 0
  const message = code === 401 ? '登录已过期，请重新登录后查看项目。'
    : code === 403 || code === 404 ? '项目不可用：可能已停用，或你没有访问权限。'
      : code === 400 ? '搜索或分页参数无效，请返回项目列表重新查询。'
        : '暂时无法读取项目数据。旧数据已隐藏，请检查网络后重试。'
  return <section className={panel}><h2 className="text-balance text-xl font-semibold">无法加载项目数据</h2><p role="alert" className="mt-3 text-pretty leading-7 text-red-800">{message}</p><div className="mt-4 flex flex-wrap items-center gap-4">
    {code === 401 ? <Link className={linkClass} to="/login">重新登录</Link> : <Link className={linkClass} to="/projects">返回项目列表</Link>}
    {retry && code !== 401 && code !== 403 && code !== 404 && code !== 400 ? <button className={buttonClass} onClick={retry}>重试</button> : null}
  </div></section>
}

function Pager({ after, next, navigate }: { after: string; next?: string; navigate: (cursor: string) => void }) {
  return <nav aria-label="结果分页" className="mt-5 flex flex-wrap items-center justify-between gap-3">
    <p className="text-pretty text-sm leading-6 text-slate-600">每页最多 20 条，仅包含你有权限查看的数据。</p>
    <div className="flex gap-3"><button className={buttonClass} disabled={!after} onClick={() => navigate('')}>回到首页</button><button className={buttonClass} disabled={!next} onClick={() => navigate(next!)}>下一页</button></div>
  </nav>
}

export function ProjectsPage() {
  return <ProjectAccess>{userID => <ProjectList key={userID} userID={userID} />}</ProjectAccess>
}

function ProjectList({ userID }: { userID: string }) {
  const [params, setParams] = useSearchParams()
  const search = params.get('q') ?? ''
  const after = params.get('after') ?? ''
  const query = new URLSearchParams({ limit: '20' })
  if (search) query.set('q', search)
  if (after) query.set('after', after)
  const projects = useQuery({ ...refreshPolicy, queryKey: ['projects', userID, search, after], queryFn: ({ signal }) => request<Page<Project>>(`/api/v1/projects?${query}`, { signal }) })
  function navigate(cursor: string, term = search) {
    const next = new URLSearchParams()
    if (term) next.set('q', term)
    if (cursor) next.set('after', cursor)
    setParams(next)
  }
  function searchProjects(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const term = String(new FormData(event.currentTarget).get('search') ?? '').trim()
    if (term === search && !after) { void projects.refetch(); return }
    navigate('', term)
  }
  return <div className="space-y-6">
    <header><p className="text-sm font-medium text-blue-700">工作区 / 项目</p><h1 className="mt-2 text-balance text-3xl font-semibold">你的项目</h1><p className="mt-3 text-pretty leading-7 text-slate-600">按仓库选择项目，查看仓库信息与运行记录。登录不会自动授予仓库访问权限。</p></header>
    <form role="search" aria-label="搜索项目" onSubmit={searchProjects} className={`${panel} flex flex-wrap items-end gap-3`}>
      <div className="min-w-0 flex-1"><label htmlFor="project-search" className="text-sm font-semibold">仓库名称或所属账号</label><input key={search} id="project-search" name="search" defaultValue={search} maxLength={100} type="search" className="mt-2 min-h-11 w-full rounded-md border border-slate-300 px-3 text-base focus:outline-2 focus:outline-blue-600" aria-describedby="search-help" /><p id="search-help" className="mt-2 text-sm text-slate-600">按名称搜索；百分号和下划线也按普通文字匹配。</p></div>
      <button className={buttonClass} type="submit" disabled={projects.isFetching}>搜索</button>
    </form>
    {projects.isFetching || projects.isPending ? <Pending label="正在校验权限并读取项目…" /> : projects.isError ? <ReadError error={projects.error} retry={() => void projects.refetch()} /> : <>
      {projects.data.items.length === 0 ? <section className={panel}><h2 className="text-balance text-xl font-semibold">{search ? '没有匹配的项目' : after ? '没有更多项目' : '还没有可见项目'}</h2><p className="mt-3 text-pretty leading-7 text-slate-600">{search ? '尝试其他名称；搜索结果仅限当前授权范围。' : '可能尚未导入仓库，或管理员还未授予你项目权限。实例管理员可在受保护配置模式中接入 GitHub 仓库；不会自动创建演示项目。'}</p>{search || after ? <button className={`${buttonClass} mt-4`} onClick={() => navigate('', '')}>清除筛选并回到首页</button> : <Link className={`${linkClass} mt-4 inline-flex min-h-11 items-center`} to="/settings/repositories">查看仓库接入</Link>}</section>
        : <ul aria-label="可访问项目" className="grid min-w-0 gap-4 md:grid-cols-2">{projects.data.items.map(item => <li key={item.id} className={`${panel} min-w-0`}>
          <p className="break-words text-sm text-slate-600">{item.organization.name} · {item.provider}</p><h2 className="mt-3 break-all text-balance text-xl font-semibold"><Link className={linkClass} to={`/projects/${item.id}`}>{item.owner}/{item.name}</Link></h2>
          <p className="mt-3 break-all text-sm text-slate-600">默认分支：{item.default_branch}</p><p className="mt-4 text-sm text-slate-700">{item.connection_status === 'metadata_verified' ? '导入时已验证仓库资料' : '仓库连接待接入'}</p>
        </li>)}</ul>}
      <Pager after={after} next={projects.data.next_cursor} navigate={navigate} />
    </>}
    <p className="text-pretty text-sm leading-6 text-slate-600">项目登记与自动构建相互独立。导入时验证过权限不代表持续授权同步；自动构建需在项目页单独验证和启用。</p>
  </div>
}

export function ProjectDetailPage() {
  const { projectID = '' } = useParams()
  return <ProjectAccess>{userID => <ProjectDetail key={`${userID}:${projectID}`} userID={userID} projectID={projectID} />}</ProjectAccess>
}

function ProjectDetail({ userID, projectID }: { userID: string; projectID: string }) {
  const [params, setParams] = useSearchParams()
  const after = params.get('after') ?? ''
  const base = `/api/v1/projects/${encodeURIComponent(projectID)}`
  const validID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(projectID)
  const detail = useQuery({ ...refreshPolicy, enabled: validID, queryKey: ['project', userID, projectID], queryFn: ({ signal }) => request<Project>(base, { signal }) })
  const runParams = new URLSearchParams({ limit: '20' })
  if (after) runParams.set('after', after)
  // Independent reads: each endpoint checks permissions, so neither relies on
  // the other's response for authorization. No waterfall or unscoped fallback.
  const runs = useQuery({ ...refreshPolicy, enabled: validID, queryKey: ['project-runs', userID, projectID, after], queryFn: ({ signal }) => request<Page<RunSummary>>(`${base}/runs?${runParams}`, { signal }) })
  if (!validID) return <ReadError error={new ApiError('', 404)} />
  if (detail.isFetching || runs.isFetching || detail.isPending || runs.isPending) return <Pending label="正在校验项目权限并读取运行记录…" />
  if (detail.isError || runs.isError) return <ReadError error={detail.error ?? runs.error} retry={() => { void detail.refetch(); void runs.refetch() }} />
  const item = detail.data
  return <div className="space-y-6">
    <Link className={`${linkClass} inline-flex min-h-11 items-center`} to="/projects">返回项目列表</Link>
    <header><p className="break-words text-sm text-slate-600">{item.organization.name} / {item.provider}</p><h1 className="mt-2 break-all text-balance text-3xl font-semibold">{item.owner}/{item.name}</h1><p className="mt-3 text-pretty leading-7 text-slate-600">仓库资料、自动构建与运行记录</p></header>
    <section className={panel}><h2 className="text-balance text-xl font-semibold">仓库信息</h2><dl className="mt-4 grid gap-5 sm:grid-cols-2"><div><dt className="text-sm text-slate-600">默认分支</dt><dd className="mt-1 break-all font-medium">{item.default_branch}</dd></div><div><dt className="text-sm text-slate-600">连接状态</dt><dd className="mt-1 font-medium">{item.connection_status === 'metadata_verified' ? '导入时已验证仓库资料' : '仓库连接待接入'}</dd></div></dl><p className="mt-4 text-pretty text-sm leading-6 text-slate-600">在下方验证 Pipeline 配置并启用 GitHub 自动构建。</p></section>
    {item.provider === 'github' ? <ProjectAutomation projectID={projectID} userID={userID} /> : null}
    <section aria-labelledby="project-runs-title" className={panel}><h2 id="project-runs-title" className="text-balance text-xl font-semibold">运行记录</h2>
      {runs.data.items.length === 0 ? <div className="mt-4"><p role="status" className="text-pretty leading-7 text-slate-600">{after ? '没有更多运行记录。' : '暂无运行记录。仓库触发与受保护 Runner 接入后，这里会显示实际运行结果。'}</p><Link className={`${linkClass} mt-3 inline-flex min-h-11 items-center`} to="/pipelines/new">先校验 Pipeline 配置</Link></div>
        : <ul aria-label="项目运行记录" className="mt-4 divide-y divide-slate-200">{runs.data.items.map(run => <li key={run.id} className="min-w-0 py-4 first:pt-0">
          <div className="flex flex-wrap items-start justify-between gap-3"><h3 className="break-all text-balance text-base font-semibold">{run.pipeline_name}</h3><StatusBadge status={run.status} /></div>
          <dl className="mt-3 grid min-w-0 gap-3 text-sm sm:grid-cols-2 lg:grid-cols-3"><div><dt className="text-slate-600">触发事件 / 引用</dt><dd className="mt-1 break-all">{run.event} · {run.ref || '无引用'}</dd></div><div><dt className="text-slate-600">提交</dt><dd className="mt-1 break-all font-mono">{run.commit_sha || '未记录'}</dd></div><div><dt className="text-slate-600">创建时间</dt><dd className="mt-1 tabular-nums"><time dateTime={run.created_at}>{new Date(run.created_at).toLocaleString()}</time></dd></div></dl>
          <Link className={`${linkClass} mt-3 inline-flex min-h-11 items-center break-all text-sm`} to={`/projects/${projectID}/runs/${run.id}`}>查看运行详情 · {run.id}</Link>
        </li>)}</ul>}
      <Pager after={after} next={runs.data.next_cursor} navigate={cursor => setParams(cursor ? { after: cursor } : {})} />
    </section>
  </div>
}

type Job = { id: string; stage_name: string; job_name: string; status: string; spec: PlanJob; reused_from_job_id?: string }
type RunDetail = { run: Run; jobs: Job[] }
export function RunDetailPage() {
 const { projectID = '', runID = '' } = useParams()
 return <ProjectAccess>{userID => <RunView key={`${userID}:${runID}`} projectID={projectID} runID={runID} userID={userID} />}</ProjectAccess>
}
function RunView({ projectID, runID, userID }: { projectID: string; runID: string; userID: string }) {
 const session = useSession(true)
 const base = `/api/v1/projects/${encodeURIComponent(projectID)}/runs/${encodeURIComponent(runID)}`
 const detail = useQuery({ queryKey: ['run-detail', userID, projectID, runID], queryFn: ({ signal }) => request<RunDetail>(base, { signal }), retry: false, gcTime: 0, refetchInterval: q => q.state.data && terminal(q.state.data.run.status) ? false : 1000 })
 const [selected, setSelected] = useState('')
 const [busy, setBusy] = useState(false)
 const [error, setError] = useState('')
 const requestKey = useRef<{ mode: string; id: string } | null>(null)
 const navigate = useNavigate()
 async function action(mode: 'cancel' | 'full' | 'failed') {
  if (!session.data) return
  setBusy(true); setError('')
  try {
   if (mode === 'cancel') { await post(`${base}/cancel`, {}, session.data.csrf_token); await detail.refetch() }
   else {
    if (requestKey.current?.mode !== mode) requestKey.current = { mode, id: crypto.randomUUID() }
    const next = await request<Run>(`${base}/rerun`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': session.data.csrf_token, 'Idempotency-Key': requestKey.current.id }, body: JSON.stringify({ mode }) })
    navigate(`/projects/${projectID}/runs/${next.id}`)
   }
  } catch (cause) { setError(cause instanceof Error ? cause.message : '运行操作失败，请重试。') }
  finally { setBusy(false) }
 }
 if (detail.isPending || session.isPending) return <Pending label="正在读取运行详情…" />
 if (detail.isError || session.isError || !session.data) return <ReadError error={detail.error ?? session.error ?? new ApiError('', 401)} retry={() => void detail.refetch()} />
 const { run, jobs } = detail.data
 const job = jobs.find(j => j.id === selected) ?? jobs[0]
 const stages = new Map<string, Job[]>()
 for (const item of jobs) stages.set(item.stage_name, [...(stages.get(item.stage_name) ?? []), item])
 return <div className="min-w-0 space-y-6"><Link className={linkClass} to={`/projects/${projectID}`}>返回项目</Link>
  <header><h1 className="text-balance text-3xl font-semibold">{run.pipeline_name}</h1><div className="mt-3"><StatusBadge status={run.status} /></div></header>
  <section className={panel}><h2 className="text-balance text-xl font-semibold">运行来源</h2><dl className="mt-4 grid min-w-0 gap-4 text-sm sm:grid-cols-2"><div><dt>事件 / 引用</dt><dd className="mt-1 break-all">{run.event} · {run.ref || '无引用'}</dd></div><div><dt>提交 SHA</dt><dd className="mt-1 break-all font-mono">{run.commit_sha || '未记录'}</dd></div><div><dt>配置 SHA256</dt><dd className="mt-1 break-all font-mono">{run.config_sha256}</dd></div><div><dt>运行 ID</dt><dd className="mt-1 break-all font-mono">{run.id}</dd></div></dl>
   <div className="mt-5 flex flex-wrap gap-3" aria-busy={busy}>{!terminal(run.status) ? <button className={buttonClass} disabled={busy} onClick={() => void action('cancel')}>取消运行</button> : <><button className={buttonClass} disabled={busy || !jobs.length} onClick={() => void action('full')}>完整重跑</button>{run.status === 'failed' ? <button className={buttonClass} disabled={busy || !jobs.length} onClick={() => void action('failed')}>重跑失败任务</button> : null}</>}</div>
   <p className="mt-3 text-pretty text-sm text-slate-600">操作需要运行权限。重跑使用相同提交与配置；失败重跑保留成功任务的结果。</p>{error ? <p role="alert" className="mt-3 text-pretty text-red-800">{error}</p> : null}
  </section>
  {Array.from(stages, ([stage, items]) => <section className={panel} key={stage}><h2 className="text-balance text-xl font-semibold">阶段：{stage}</h2><ul className="mt-4 space-y-4">{items.map(item => <li key={item.id}><h3 className="text-balance font-semibold">{item.job_name} · {item.status}</h3>{item.reused_from_job_id ? <p className="mt-1 break-all text-sm text-slate-600">复用成功任务：{item.reused_from_job_id}</p> : null}<ol aria-label={`${item.job_name} 步骤`} className="mt-2 list-decimal space-y-2 pl-5">{item.spec.steps.map((step, index) => <li key={index}><details><summary className="min-h-11 cursor-pointer py-2 focus-visible:outline-2 focus-visible:outline-blue-600">{step.name} · {step.image || item.spec.image || '默认镜像'}</summary><pre className="max-w-full overflow-x-auto whitespace-pre-wrap break-all rounded bg-slate-50 p-3 text-sm">{step.commands.join('\n')}</pre></details></li>)}</ol></li>)}</ul></section>)}
  <section className={`${panel} min-w-0`}><h2 className="text-balance text-xl font-semibold">实时日志</h2>{job ? <><label className="mt-4 block text-sm font-semibold">选择任务<select className="mt-2 min-h-11 w-full rounded border border-slate-300 px-3" value={job.id} onChange={e => setSelected(e.target.value)}>{jobs.map(item => <option key={item.id} value={item.id}>{item.stage_name} / {item.job_name}</option>)}</select></label><LiveLogs key={job.id} path={`${base}/jobs/${job.id}/logs`} finished={terminal(job.status)} reused={Boolean(job.reused_from_job_id)} /></> : <p className="mt-3 text-pretty">此运行没有可执行任务，请检查配置。</p>}</section>
 </div>
}
function terminal(status: string) { return ['succeeded', 'failed', 'canceled', 'skipped'].includes(status) }
type Chunk = { sequence: number; step: number; stream: string; data: string; truncated: boolean }
function LiveLogs({ path, finished, reused }: { path: string; finished: boolean; reused: boolean }) {
 const [view, setView] = useState({ text: '', message: '正在读取日志…', error: '', clipped: false })
 useEffect(() => {
  if (reused) return
  const controller = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined
  let after = 0, text = '', clipped = false
  const decoders = new Map<string, TextDecoder>()
  async function poll() {
   try {
    const page = await request<{ items: Chunk[]; next_sequence: number; expired: boolean }>(`${path}?after=${after}`, { signal: controller.signal })
    if (controller.signal.aborted) return
    for (const c of page.items) {
     if (c.sequence <= after) continue
     const key = `${c.step}:${c.stream}`
     if (!decoders.has(key)) decoders.set(key, new TextDecoder())
     const bytes = Uint8Array.from(atob(c.data || ''), char => char.charCodeAt(0))
     text += decoders.get(key)!.decode(bytes, { stream: true })
     if (c.truncated) text += '\n[服务端日志已截断]\n'
    }
    if (finished && !page.items.length) { for (const decoder of decoders.values()) text += decoder.decode(); decoders.clear() }
    if (text.length > 262144) { text = text.slice(-262144); clipped = true }
    after = page.next_sequence
    if (page.expired) text = ''
    setView({ text, clipped, error: '', message: page.expired ? '日志保留期已结束。' : finished && !page.items.length ? '日志读取完成。' : '日志更新中…' })
    if (!page.expired && (!finished || page.items.length > 0)) timer = setTimeout(() => void poll(), page.items.length === 16 ? 50 : 1000)
   } catch (cause) { if (!controller.signal.aborted) setView({ text: '', clipped: false, message: '', error: cause instanceof Error ? cause.message : '日志读取失败，请刷新页面重试。' }) }
  }
  void poll()
  return () => { controller.abort(); clearTimeout(timer) }
 }, [path, finished, reused])
 if (reused) return <p className="mt-3 text-pretty">此任务复用先前成功结果，本次没有新日志。</p>
 return <div className="mt-4 min-w-0"><p role="status" className="text-sm text-slate-600">{view.message}</p>{view.error ? <p role="alert" className="mt-3 text-red-800">{view.error}</p> : <>{view.clipped ? <p className="mt-2 text-sm">页面仅显示最近 262144 个字符。</p> : null}<pre aria-label="任务日志" tabIndex={0} className="mt-3 max-h-96 max-w-full overflow-auto whitespace-pre-wrap break-all rounded-lg bg-slate-950 p-4 font-mono text-sm text-slate-100 focus-visible:outline-2 focus-visible:outline-blue-600">{view.text || '暂无日志。'}</pre></>}</div>
}
