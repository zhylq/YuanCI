import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { StatusBadge } from '../components/status-badge'
import { getRuns, getSystemInfo } from '../lib/api'

function SkeletonRows() {
  return <div aria-label="正在加载运行记录" className="space-y-3">{[1, 2, 3].map((item) => <div key={item} className="h-16 rounded-lg bg-slate-100" />)}</div>
}

export function DashboardPage() {
  const system = useQuery({ queryKey: ['system-info'], queryFn: getSystemInfo, staleTime: 60_000 })
  const runs = useQuery({ queryKey: ['runs'], queryFn: getRuns, refetchInterval: 10_000 })
  const error = system.error ?? runs.error

  return (
    <div className="space-y-8">
      <section className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <p className="text-sm font-medium text-blue-700">控制面</p>
          <h1 className="mt-1 text-balance text-3xl font-semibold text-slate-950">构建状态一目了然</h1>
          <p className="mt-2 max-w-2xl text-pretty text-sm leading-6 text-slate-600">当前里程碑已连接 Pipeline 编译、事务队列和独立 Runner 协议。</p>
        </div>
        <Link to="/pipelines/new" className="rounded-md bg-blue-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600">创建 Pipeline</Link>
      </section>

      {error ? <div role="alert" className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-800">无法加载控制面数据：{error.message}</div> : null}

      <section aria-labelledby="health-title" className="grid gap-4 sm:grid-cols-3">
        <h2 id="health-title" className="sr-only">系统状态</h2>
        <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm"><p className="text-sm text-slate-500">服务状态</p><p className="mt-2 text-xl font-semibold text-emerald-700">{system.isPending ? '检查中' : '就绪'}</p></div>
        <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm"><p className="text-sm text-slate-500">版本</p><p className="mt-2 truncate text-xl font-semibold text-slate-950 tabular-nums">{system.data?.version ?? '—'}</p></div>
        <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm"><p className="text-sm text-slate-500">最近运行</p><p className="mt-2 text-xl font-semibold text-slate-950 tabular-nums">{runs.data?.items.length ?? 0}</p></div>
      </section>

      <section aria-labelledby="runs-title" className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <div className="flex items-center justify-between gap-4"><div><h2 id="runs-title" className="text-balance text-lg font-semibold">最近运行</h2><p className="mt-1 text-pretty text-sm text-slate-500">每 10 秒自动刷新</p></div></div>
        <div className="mt-5" aria-busy={runs.isPending}>
          {runs.isPending ? <SkeletonRows /> : runs.data?.items.length ? (
            <ul className="divide-y divide-slate-200">
              {runs.data.items.map((run) => (
                <li key={run.id} className="flex flex-wrap items-center justify-between gap-3 py-4 first:pt-0 last:pb-0">
                  <div className="min-w-0"><p className="truncate font-medium text-slate-950">{run.pipeline_name}</p><p className="mt-1 truncate text-xs text-slate-500"><span className="tabular-nums">{run.config_sha256.slice(0, 10)}</span> · {run.event} · {new Date(run.created_at).toLocaleString()}</p></div>
                  <StatusBadge status={run.status} />
                </li>
              ))}
            </ul>
          ) : (
            <div className="rounded-lg border border-dashed border-slate-300 p-8 text-center"><h3 className="font-medium text-slate-900">还没有运行记录</h3><p className="mt-1 text-sm text-slate-500">先校验并创建第一个 Pipeline。</p><Link to="/pipelines/new" className="mt-4 inline-block rounded-md bg-blue-600 px-3.5 py-2 text-sm font-semibold text-white focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600">打开编辑器</Link></div>
          )}
        </div>
      </section>
    </div>
  )
}
