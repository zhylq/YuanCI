export type SystemInfo = {
  name: string
  version: string
  commit: string
  go_version: string
  started_at: string
  capabilities: string[]
}

export type Run = {
  id: string
  pipeline_name: string
  event: string
  ref?: string
  commit_sha?: string
  status: 'queued' | 'running' | 'waiting_approval' | 'succeeded' | 'failed' | 'canceled'
  config_sha256: string
  created_at: string
  started_at?: string
  finished_at?: string
}

export type PlanJob = {
  name: string
  image?: string
  depends_on?: string[]
  timeout: number
  retry: number
  steps: Array<{ name: string; image?: string; commands: string[] }>
}

export type PipelinePlan = {
  version: string
  name: string
  config_sha256: string
  compiled_at: string
  stages: Array<{ name: string; depends_on?: string[]; jobs: PlanJob[] }>
}

export type ValidationError = { path?: string; message: string }
export type ValidationResult = { valid: boolean; plan?: PipelinePlan; errors: ValidationError[] }

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    credentials: 'same-origin',
    headers: { Accept: 'application/json', ...options?.headers },
    ...options,
  })
  const body = await response.json().catch(() => null)
  if (!response.ok) {
    const detail = body && typeof body.detail === 'string' ? body.detail : `请求失败（HTTP ${response.status}）`
    throw new Error(detail)
  }
  return body as T
}

export function getSystemInfo() {
  return request<SystemInfo>('/api/v1/system/info')
}

export function getRuns() {
  return request<{ items: Run[] }>('/api/v1/runs?limit=20')
}

export async function validatePipeline(content: string): Promise<ValidationResult> {
  const response = await fetch('/api/v1/pipelines/validate', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ content }),
  })
  const body = (await response.json()) as ValidationResult & { detail?: string }
  if (response.status === 422 && Array.isArray(body.errors)) return body
  if (!response.ok) throw new Error(body.detail ?? `校验失败（HTTP ${response.status}）`)
  return body
}
