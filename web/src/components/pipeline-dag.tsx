import type { PipelinePlan } from '../lib/api'

export function PipelineDag({ plan }: { plan: PipelinePlan }) {
  return (
    <section aria-labelledby="dag-title" className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="dag-title" className="text-balance text-lg font-semibold text-slate-950">执行计划</h2>
          <p className="mt-1 text-pretty text-sm text-slate-600">配置摘要 {plan.config_sha256.slice(0, 12)}</p>
        </div>
        <span className="rounded-md bg-blue-50 px-2.5 py-1 text-xs font-medium text-blue-800">{plan.stages.length} 个阶段</span>
      </div>
      <ol className="mt-5 grid gap-4 lg:grid-cols-2">
        {plan.stages.map((stage, stageIndex) => (
          <li key={stage.name} className="rounded-lg border border-slate-200 bg-slate-50 p-4">
            <div className="flex items-center gap-3">
              <span aria-hidden="true" className="grid size-7 place-items-center rounded-full bg-slate-900 text-xs font-semibold text-white tabular-nums">{stageIndex + 1}</span>
              <div className="min-w-0">
                <h3 className="truncate font-medium text-slate-950">{stage.name}</h3>
                <p className="truncate text-xs text-slate-500">{stage.depends_on?.length ? `依赖 ${stage.depends_on.join('、')}` : '起始阶段'}</p>
              </div>
            </div>
            <ul className="mt-4 space-y-2">
              {stage.jobs.map((job) => (
                <li key={job.name} className="rounded-md border border-slate-200 bg-white px-3 py-2">
                  <p className="truncate text-sm font-medium text-slate-900">{job.name}</p>
                  <p className="truncate text-xs text-slate-500">{job.image ?? '步骤独立镜像'} · {job.steps.length} 个步骤</p>
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ol>
    </section>
  )
}
