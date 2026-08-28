import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, expect, test, vi } from 'vitest'
import { ProjectDetailPage, ProjectsPage, type Project } from './projects'

const id = '10000000-0000-4000-8000-000000000001'
const otherID = '20000000-0000-4000-8000-000000000002'
const item: Project = { id, organization: { id: 'org', name: '研发团队' }, provider: 'github', owner: 'team', name: 'first', default_branch: 'main', connection_status: 'not_connected' }
const other: Project = { ...item, id: otherID, name: 'second' }
const status = { mode: 'managed', initialized: true, configured: true }
const session = { user_id: 'fixture-user', display_name: 'tester', csrf_token: 'fixture' }
function reply(body: unknown, code = 200) { return new Response(JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } }) }
function mock(read: (path: string) => Response | Promise<Response>, mode = status) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    calls.push(path)
    if (path.endsWith('/auth/status')) return reply(mode)
    if (path.endsWith('/session')) return reply(session)
    return read(path)
  }))
  return calls
}
const caches: QueryClient[] = []
function mount(path = '/projects') {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  caches.push(cache)
  render(<QueryClientProvider client={cache}><MemoryRouter initialEntries={[path]}><Routes>
    <Route path="/projects" element={<ProjectsPage />} />
    <Route path="/projects/:projectID" element={<ProjectDetailPage />} />
    <Route path="/login" element={<p>登录入口</p>} />
  </Routes></MemoryRouter></QueryClientProvider>)
  return cache
}
afterEach(() => { cleanup(); caches.splice(0).forEach(cache => cache.clear()); vi.unstubAllGlobals() })

test('project list pages and literal searches are scoped and reset the cursor', async () => {
  const calls = mock(path => {
    const url = new URL(path, 'http://localhost')
    if (url.searchParams.get('q')) return reply({ items: [] })
    return url.searchParams.get('after') ? reply({ items: [other] }) : reply({ items: [item], next_cursor: id })
  })
  mount()
  expect(await screen.findByRole('link', { name: 'team/first' })).toHaveAttribute('href', `/projects/${id}`)
  expect(screen.getByText('仓库连接待接入')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '下一页' }))
  await screen.findByRole('link', { name: 'team/second' })
  expect(screen.queryByRole('link', { name: 'team/first' })).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('仓库名称或所属账号'), { target: { value: '%_仓库' } })
  fireEvent.submit(screen.getByRole('search'))
  await screen.findByRole('heading', { name: '没有匹配的项目' })
  const query = new URL(calls.at(-1)!, 'http://localhost').searchParams
  expect(query.get('q')).toBe('%_仓库')
  expect(query.has('after')).toBe(false)
  expect(calls.some(path => path.startsWith('/api/v1/runs'))).toBe(false)
  fireEvent.click(screen.getByRole('button', { name: '清除筛选并回到首页' }))
  await screen.findByRole('link', { name: 'team/first' })
})

test('evaluation mode never requests sessions, projects or runs', async () => {
  const calls = mock(() => { throw new Error('must not fetch protected data') }, { ...status, mode: 'evaluation', configured: false })
  mount()
  await screen.findByRole('heading', { name: '项目浏览需要登录' })
  expect(calls).toEqual(['/api/v1/auth/status'])
})

test('empty installation explains import limitations without fake connect controls', async () => {
  mock(() => reply({ items: [] }))
  mount()
  await screen.findByRole('heading', { name: '还没有可见项目' })
  expect(screen.getByText(/本轮不提供仓库导入/)).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /连接|导入/ })).not.toBeInTheDocument()
})

test('detail fetches only project-scoped summaries and pages them', async () => {
  const run = { id: 'run-1', pipeline_name: 'unit-tests', event: 'push', ref: 'refs/heads/main', status: 'succeeded', created_at: '2026-08-28T00:00:00Z' }
  const calls = mock(path => path.includes('/runs?') ? reply({ items: [run], next_cursor: path.includes('after=') ? undefined : 'fixture-cursor' }) : reply(item))
  mount(`/projects/${id}`)
  await screen.findByRole('heading', { name: 'team/first' })
  expect(screen.getByText('成功')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /构建|发布|重跑/ })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '下一页' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled())
  expect(calls).toContain(`/api/v1/projects/${id}/runs?limit=20&after=fixture-cursor`)
  expect(calls.every(path => !path.startsWith('/api/v1/runs'))).toBe(true)
})

test('revocation during refresh hides previously displayed project and run data', async () => {
  let denied = false
  mock(path => denied ? reply({ detail: 'denied' }, 404) : path.includes('/runs?') ? reply({ items: [] }) : reply(item))
  const cache = mount(`/projects/${id}`)
  await screen.findByRole('heading', { name: 'team/first' })
  denied = true
  await cache.invalidateQueries({ queryKey: ['project', session.user_id, id] })
  expect(await screen.findByRole('alert')).toHaveTextContent('项目不可用')
  expect(screen.queryByRole('heading', { name: 'team/first' })).not.toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: '运行记录' })).not.toBeInTheDocument()
})

test('session expiry on a project query offers login and no cached data', async () => {
  mock(() => reply({ detail: 'expired' }, 401))
  mount()
  expect(await screen.findByRole('alert')).toHaveTextContent('登录已过期')
  fireEvent.click(screen.getByRole('link', { name: '重新登录' }))
  await screen.findByText('登录入口')
})

test('network errors are not presented as empty projects and retry works', async () => {
  let fail = true
  mock(() => fail ? Promise.reject(new Error('offline')) : reply({ items: [item] }))
  mount()
  expect(await screen.findByRole('alert')).toHaveTextContent('旧数据已隐藏')
  expect(screen.queryByText('还没有可见项目')).not.toBeInTheDocument()
  fail = false
  fireEvent.click(screen.getByRole('button', { name: '重试' }))
  await screen.findByRole('link', { name: 'team/first' })
})

test('malformed project route does not issue detail requests', async () => {
  const calls = mock(() => { throw new Error('unexpected fetch') })
  mount('/projects/not-an-id')
  expect(await screen.findByRole('alert')).toHaveTextContent('项目不可用')
  expect(calls.some(path => path.includes('/api/v1/projects'))).toBe(false)
})

test('switching project discards the previous detail while the next loads', async () => {
  let finish!: (value: Response) => void
  mock(path => path.startsWith(`/api/v1/projects/${id}`) ? path.includes('/runs?') ? reply({ items: [] }) : reply(item)
    : path.startsWith(`/api/v1/projects/${otherID}`) ? new Promise<Response>(resolve => { if (!path.includes('/runs?')) finish = resolve; else resolve(reply({ items: [] })) })
      : reply({ items: [other] }))
  mount(`/projects/${id}`)
  await screen.findByRole('heading', { name: 'team/first' })
  fireEvent.click(screen.getByRole('link', { name: '返回项目列表' }))
  fireEvent.click(await screen.findByRole('link', { name: 'team/second' }))
  await screen.findByText('正在校验项目权限并读取运行记录…')
  expect(screen.queryByRole('heading', { name: 'team/first' })).not.toBeInTheDocument()
  finish(reply(other))
  await screen.findByRole('heading', { name: 'team/second' })
})
