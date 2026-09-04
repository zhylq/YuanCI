import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, test, vi } from 'vitest'
import { RepositorySettingsPage, type ImportSettings } from './repository-settings'

const base = '/api/v1/integrations/github'
const session = { user_id: 'fixture-user', csrf_token: 'fixture-csrf', display_name: '测试管理员' }
const settings: ImportSettings = { app: { id: 'fixture-revision', app_id: '12', client_id: 'Iv1.fixture', slug: 'fixture-app' }, callback_url: 'https://ci.example.test/api/v1/integrations/github/callback', setup_url: 'https://ci.example.test/settings/repositories', install_url: 'https://github.com/apps/fixture-app/installations/new', needs_verification: false }
function reply(value: unknown, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } }) }
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={client}><MemoryRouter><RepositorySettingsPage /></MemoryRouter></QueryClientProvider>)
  return client
}
function setupFetch(rest: (path: string, options?: RequestInit) => Response, mode = 'managed') {
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    if (path.endsWith('/auth/status')) return reply({ mode, configured: true, initialized: true })
    if (path.endsWith('/session')) return reply(session)
    return rest(path, options)
  })
  vi.stubGlobal('fetch', fetch); return fetch
}
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

test('evaluation and file mode never request secrets or installations', async () => {
  for (const mode of ['evaluation', 'file']) {
    const fetch = setupFetch(() => { throw new Error('unexpected protected request') }, mode); mount()
    expect(await screen.findByRole('heading', { name: '仓库接入需要受保护配置模式' })).toBeInTheDocument()
    expect(screen.queryByLabelText('RSA 私钥（PEM）')).not.toBeInTheDocument()
    expect(fetch).toHaveBeenCalledTimes(1); cleanup()
  }
})
test('non-admin cannot see configuration fields', async () => {
  setupFetch(() => reply({ detail: 'denied' }, 403)); mount()
  expect(await screen.findByRole('alert')).toHaveTextContent('实例管理员')
  expect(screen.queryByLabelText('App ID')).not.toBeInTheDocument()
})
test('save sends revision and CSRF, clears key on failure and never caches it', async () => {
  const fetch = setupFetch((_path, options) => options?.method === 'POST' ? reply({ detail: 'expired' }, 409) : reply(settings))
  const client = mount(); await screen.findByLabelText('App ID')
  const key = screen.getByLabelText('RSA 私钥（PEM）')
  fireEvent.change(key, { target: { value: 'fixture-private-key' } })
  fireEvent.submit(screen.getByRole('form', { name: /App 已配置/ }))
  expect(await screen.findByRole('alert')).toHaveTextContent('授权已变化或过期')
  expect(key).toHaveValue('')
  const [, options] = fetch.mock.calls.find(([, options]) => options?.method === 'POST')!
  expect(JSON.parse(String(options?.body))).toEqual({ app_id: '12', private_key: 'fixture-private-key', expected_revision: 'fixture-revision', webhook_enabled: false })
  expect(options?.headers).toMatchObject({ 'X-CSRF-Token': 'fixture-csrf' })
  expect(JSON.stringify(client.getQueryCache().getAll().map(q => q.state.data))).not.toContain('fixture-private-key')
  expect(client.getMutationCache().getAll()).toHaveLength(0)
})
test('validation is associated with fields and makes no write; stale login hides discovery', async () => {
  const fetch = setupFetch(() => reply({ ...settings, needs_verification: true })); mount()
  await screen.findByLabelText('App ID'); fireEvent.submit(screen.getByRole('form', { name: /App 已配置/ }))
  expect(screen.getByLabelText('RSA 私钥（PEM）')).toHaveAttribute('aria-invalid', 'true')
  expect(screen.getByLabelText('RSA 私钥（PEM）')).toHaveAttribute('aria-describedby', 'app-key-help app-error')
  expect(screen.queryByRole('button', { name: '授权发现仓库' })).not.toBeInTheDocument()
  expect(fetch.mock.calls.every(([, options]) => options?.method !== 'POST')).toBe(true)
})
test('discovery imports only explicit IDs, bounds selection and reports actual outcome', async () => {
  const until = new Date(Date.now() + 600_000).toISOString()
  const fetch = setupFetch((path, options) => {
    if (path === base) return reply({ ...settings, authorized_until: until })
    if (path.endsWith('/installations')) return reply({ items: [{ id: '34', account_id: '56', account: 'team' }] })
    if (options?.method === 'POST') return reply({ items: [{ id: 'local-id', name: 'team/repo-0', created: true }] })
    return reply({ items: Array.from({ length: 21 }, (_, n) => ({ id: String(n + 1), owner: 'team', name: `repo-${n}`, default_branch: 'main' })), next_page: 2 })
  }); mount()
  fireEvent.change(await screen.findByLabelText('GitHub 安装账号'), { target: { value: '34' } })
  const boxes = await screen.findAllByRole('checkbox', { name: /team\// })
  for (const box of boxes.slice(0, 20)) fireEvent.click(box)
  expect(boxes[20]).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: '导入所选仓库（20）' }))
  const link = await screen.findByRole('link', { name: 'team/repo-0' }); expect(link).toHaveAttribute('href', '/projects/local-id')
  const [, options] = fetch.mock.calls.find(([path]) => path.endsWith('/import'))!
  expect(JSON.parse(String(options?.body))).toEqual({ installation_id: '34', repository_ids: Array.from({ length: 20 }, (_, n) => String(n + 1)) })
  expect(screen.getByText(/仅登记仓库资料/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '导入所选仓库（0）' })).toBeDisabled()
})
test('page navigation resets selection and repository API failures hide stale metadata', async () => {
  setupFetch(path => {
    if (path === base) return reply({ ...settings, authorized_until: new Date(Date.now() + 600_000).toISOString() })
    if (path.endsWith('/installations')) return reply({ items: [{ id: '34', account_id: '56', account: 'team' }] })
    if (path.includes('page=2')) return reply({ detail: 'GitHub 请求受限' }, 429)
    return reply({ items: [{ id: '70', owner: 'team', name: 'secret-repo', default_branch: 'main' }], next_page: 2 })
  }); mount()
  fireEvent.change(await screen.findByLabelText('GitHub 安装账号'), { target: { value: '34' } })
  fireEvent.click(await screen.findByRole('checkbox', { name: /team\/secret-repo/ })); fireEvent.click(screen.getByRole('button', { name: '下一页' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('请求受限')
  await waitFor(() => expect(screen.queryByText('team/secret-repo')).not.toBeInTheDocument())
})

test('webhook replacement is write-only and health distinguishes local readiness', async () => {
 const fetch = setupFetch((_path, options) => options?.method === 'POST' ? reply({ detail: 'changed' }, 409) : reply({ ...settings, app: { ...settings.app, webhook_enabled: true }, webhook_url: 'https://ci.example.test/api/v1/webhooks/github', webhook_secret_configured: true, webhook_secret_version: 3 }))
 const client = mount()
 expect(await screen.findByText('本地配置就绪，仍需真实 Webhook 验证。')).toBeInTheDocument()
 expect(screen.getByText('已配置 · 版本 3')).toBeInTheDocument()
 expect(screen.getByLabelText('Webhook URL')).toHaveValue('https://ci.example.test/api/v1/webhooks/github')
 const secret = screen.getByLabelText('Webhook Secret（留空保留原值）')
 fireEvent.change(secret, { target: { value: 'synthetic-webhook-replacement' } })
 fireEvent.change(screen.getByLabelText('RSA 私钥（PEM）'), { target: { value: 'synthetic-key' } })
 fireEvent.submit(screen.getByRole('form', { name: /App 已配置/ }))
 await screen.findByRole('alert')
 expect(secret).toHaveValue('')
 const [, options] = fetch.mock.calls.find(([, opts]) => opts?.method === 'POST')!
 expect(JSON.parse(String(options?.body))).toMatchObject({ webhook_secret: 'synthetic-webhook-replacement', webhook_enabled: true })
 expect(JSON.stringify(client.getQueryCache().getAll().map(q => q.state.data))).not.toContain('synthetic-webhook-replacement')
})
