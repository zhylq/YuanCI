import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, test, vi } from 'vitest'
import { AuthSettingsPage } from './auth-settings'

const status = { mode: 'managed', initialized: false, configured: false, callback_url: 'https://ci.example.test/api/v1/auth/github/callback' }
const baseSettings = { active: null, candidate: null, csrf_token: 'fixture-csrf', callback_url: status.callback_url }
const candidate = { id: 'fixture-id', client_id: 'Iv1.fixture', bootstrap_subject: '100', status: 'candidate', expires_at: '2030-01-01T00:00:00Z' }
function reply(body: unknown, code = 200) { return new Response(JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } }) }
function mount(setup = true) {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  render(<QueryClientProvider client={cache}><MemoryRouter><AuthSettingsPage setup={setup} /></MemoryRouter></QueryClientProvider>)
  return cache
}
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

test('Gitee setup selects its callback and saves without GitHub credentials', async () => {
  const writes: Array<Record<string, unknown>> = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, options?: RequestInit) => {
    if (path.endsWith('/auth/status')) return reply(status)
    if (options?.method === 'POST') { writes.push(JSON.parse(String(options.body))); return reply({ candidate_id: 'fixture-id' }, 201) }
    return reply({ ...baseSettings, callback_urls: { github: status.callback_url, gitee: 'https://ci.example.test/api/v1/auth/gitee/callback' } })
  }))
  mount()
  fireEvent.change(await screen.findByLabelText('登录平台'), { target: { value: 'gitee' } })
  expect(screen.getByLabelText('登录回调地址（Callback URL）')).toHaveValue('https://ci.example.test/api/v1/auth/gitee/callback')
  expect(screen.getByRole('heading', { name: '创建 Gitee OAuth 应用' })).toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('Client ID'), { target: { value: 'gitee-client' } })
  fireEvent.change(screen.getByLabelText('Client Secret'), { target: { value: 'private-test-secret-123456' } })
  fireEvent.change(screen.getByLabelText('首次管理员 Gitee 数字 ID'), { target: { value: '100' } })
  fireEvent.submit(screen.getByRole('form', { name: '填写应用信息' }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(writes[0].provider).toBe('gitee')
  expect(screen.getByLabelText('Client Secret')).toHaveValue('')
})

test('setup unlocks, saves encrypted candidate intent and clears secret without caching it', async () => {
  let unlocked = false
  let saved = false
  const calls: Array<{ path: string; options?: RequestInit }> = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, options?: RequestInit) => {
    calls.push({ path, options })
    if (path.endsWith('/auth/status')) return reply(status)
    if (path.endsWith('/setup/exchange')) { unlocked = true; return reply({ csrf_token: 'fixture-csrf' }) }
    if (options?.method === 'POST') { saved = true; return reply({ candidate_id: 'fixture-id' }, 201) }
    return unlocked ? reply({ ...baseSettings, candidate: saved ? candidate : null }) : reply({ detail: 'locked' }, 401)
  }))
  const cache = mount()
  const code = await screen.findByLabelText('一次性设置码')
  fireEvent.change(code, { target: { value: 'A'.repeat(43) } })
  fireEvent.submit(screen.getByRole('button', { name: '验证设置码' }).closest('form')!)
  const clientID = await screen.findByLabelText('Client ID')
  fireEvent.change(clientID, { target: { value: 'Iv1.fixture' } })
  fireEvent.change(screen.getByLabelText('Client Secret'), { target: { value: 'private-test-secret-123456' } })
  fireEvent.change(screen.getByLabelText('首次管理员 GitHub 数字 ID'), { target: { value: '100' } })
  fireEvent.submit(screen.getByRole('form', { name: '填写应用信息' }))
  await screen.findByRole('heading', { name: '配置已保存，等待授权验证' })
  expect(screen.getByLabelText('Client Secret')).toHaveValue('')
  const write = calls.find(call => call.path.endsWith('/setup/settings') && call.options?.method === 'POST')!
  expect(write.options?.headers).toMatchObject({ 'X-CSRF-Token': 'fixture-csrf' })
  expect(write.options?.credentials).toBe('same-origin')
  expect(JSON.parse(String(write.options?.body)).client_secret).toBe('private-test-secret-123456')
  expect(JSON.stringify(cache.getQueryCache().getAll().map(q => q.state.data))).not.toContain('private-test-secret')
  expect(cache.getMutationCache().getAll()).toHaveLength(0)
  expect(screen.getByRole('link', { name: 'Gitee 官方教程' })).toHaveAttribute('href', 'https://gitee.com/api/v5/oauth_doc')
  expect(screen.queryByRole('button', { name: /Gitee/ })).not.toBeInTheDocument()
})

test('unauthorized administrator settings expose no editable credential fields', async () => {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => path.endsWith('/auth/status') ? reply({ ...status, initialized: true, configured: true }) : reply({ detail: 'denied' }, 403)))
  mount(false)
  expect(await screen.findByRole('alert')).toHaveTextContent('只有实例管理员')
  expect(screen.queryByLabelText('Client Secret')).not.toBeInTheDocument()
})

test('failed save clears the password and reports the error beside the form', async () => {
  vi.stubGlobal('fetch', vi.fn(async (path: string, options?: RequestInit) => {
    if (path.endsWith('/auth/status')) return reply(status)
    if (options?.method === 'POST') return reply({ detail: '配置版本已更新，请刷新。' }, 409)
    return reply(baseSettings)
  }))
  mount()
  fireEvent.change(await screen.findByLabelText('Client ID'), { target: { value: 'Iv1.fixture' } })
  fireEvent.change(screen.getByLabelText('Client Secret'), { target: { value: 'private-test-secret-123456' } })
  fireEvent.change(screen.getByLabelText('首次管理员 GitHub 数字 ID'), { target: { value: '100' } })
  fireEvent.submit(screen.getByRole('form', { name: '填写应用信息' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('配置版本已更新')
  expect(screen.getByLabelText('Client Secret')).toHaveValue('')
  expect(screen.getByRole('button', { name: '保存待验证配置' })).toBeEnabled()
})

test('required input errors are associated with fields and prevent writes', async () => {
  const fetch = vi.fn(async (path: string) => reply(path.endsWith('/auth/status') ? status : baseSettings))
  vi.stubGlobal('fetch', fetch)
  mount()
  await screen.findByLabelText('Client ID')
  fireEvent.submit(screen.getByRole('form', { name: '填写应用信息' }))
  expect(screen.getByLabelText('Client Secret')).toHaveAttribute('aria-invalid', 'true')
  expect(screen.getByLabelText('Client Secret')).toHaveAttribute('aria-describedby', 'client_secret-help client_secret-error')
  expect(fetch.mock.calls).toHaveLength(2)
})

test('copy failure gives a manual-copy instruction and evaluation never fetches settings', async () => {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => reply(path.endsWith('/auth/status') ? status : baseSettings)))
  mount()
  await screen.findByLabelText('登录回调地址（Callback URL）')
  fireEvent.click(screen.getByRole('button', { name: '复制地址' }))
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('请手动选择'))
  cleanup()
  const fetch = vi.fn(async () => reply({ ...status, mode: 'evaluation' }))
  vi.stubGlobal('fetch', fetch)
  mount(false)
  expect(await screen.findByText('网页配置需要受保护模式')).toBeInTheDocument()
  expect(screen.queryByLabelText('Client Secret')).not.toBeInTheDocument()
  expect(fetch).toHaveBeenCalledTimes(1)
})
