import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { PipelineDag } from '../components/pipeline-dag'
import { validatePipeline, type ValidationResult } from '../lib/api'

const starterPipeline = `version: v1
name: build-and-test

triggers:
  - event: push
    branches: [main]

stages:
  - name: verify
    jobs:
      - name: unit-tests
        image: golang:1.27-alpine
        timeout: 15m
        steps:
          - name: test
            commands:
              - go test ./...

  - name: package
    depends_on: [verify]
    jobs:
      - name: image
        image: docker:28-cli
        steps:
          - name: build
            commands:
              - docker build .
`

export function PipelineEditorPage({ csrfToken }: { csrfToken?: string } = {}) {
  const [content, setContent] = useState(starterPipeline)
  const [result, setResult] = useState<ValidationResult | null>(null)
  const validation = useMutation({
    mutationFn: (value: string) => validatePipeline(value, csrfToken),
    onSuccess: setResult,
  })
  const fieldInvalid = result?.valid === false

  return (
    <div className="space-y-6">
      <header>
        <p className="text-sm font-medium text-blue-700">Pipeline v1</p>
        <h1 className="mt-1 text-balance text-3xl font-semibold text-slate-950">可视化执行计划从可靠配置开始</h1>
        <p className="mt-2 max-w-3xl text-pretty text-sm leading-6 text-slate-600">编辑仓库中的 <code className="rounded bg-slate-100 px-1.5 py-0.5">.yuanci.yml</code>，服务端会执行严格字段、依赖图和安全策略校验。</p>
      </header>

      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1.15fr)_minmax(340px,0.85fr)]">
        <section aria-labelledby="editor-title" className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
          <div className="flex flex-wrap items-center justify-between gap-3"><div><h2 id="editor-title" className="text-lg font-semibold">YAML 编辑器</h2><p id="pipeline-help" className="mt-1 text-sm text-slate-500">未知字段会被拒绝，特权模式不能通过普通 Pipeline 开启。</p></div><button type="button" onClick={() => validation.mutate(content)} disabled={validation.isPending || !content.trim()} className="rounded-md bg-blue-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-blue-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-600 disabled:cursor-not-allowed disabled:bg-slate-300">{validation.isPending ? '校验中…' : '校验并编译'}</button></div>
          <label htmlFor="pipeline-source" className="sr-only">Pipeline YAML</label>
          <textarea id="pipeline-source" value={content} onChange={(event) => { setContent(event.target.value); setResult(null) }} spellCheck={false} aria-describedby={fieldInvalid ? 'pipeline-help pipeline-errors' : 'pipeline-help'} aria-invalid={fieldInvalid} className="mt-5 min-h-[540px] w-full resize-y rounded-lg border border-slate-300 bg-slate-950 p-4 font-mono text-sm leading-6 text-slate-100 shadow-inner focus:border-blue-500 focus:outline-none focus:ring-2 focus:ring-blue-200" />
          {validation.error ? <p id="pipeline-request-error" role="alert" className="mt-3 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800">校验请求失败：{validation.error.message}</p> : null}
          {fieldInvalid ? <div id="pipeline-errors" role="alert" className="mt-3 rounded-md border border-red-200 bg-red-50 p-3"><p className="text-sm font-medium text-red-900">配置需要修正</p><ul className="mt-2 list-disc space-y-1 pl-5 text-sm text-red-800">{result.errors.map((error, index) => <li key={`${error.path}-${index}`}><code>{error.path || 'pipeline'}</code>：{error.message}</li>)}</ul></div> : null}
          {result?.valid ? <p role="status" className="mt-3 rounded-md border border-emerald-200 bg-emerald-50 p-3 text-sm text-emerald-800">配置有效，已生成不可变执行计划。</p> : null}
        </section>
        <div>{result?.valid && result.plan ? <PipelineDag plan={result.plan} /> : <section aria-labelledby="preview-title" className="rounded-xl border border-dashed border-slate-300 bg-white p-8 text-center"><h2 id="preview-title" className="text-balance font-semibold text-slate-900">等待编译预览</h2><p className="mt-2 text-pretty text-sm text-slate-500">点击“校验并编译”后，这里将显示阶段和 Job 依赖。</p></section>}</div>
      </div>
    </div>
  )
}
