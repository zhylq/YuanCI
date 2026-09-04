import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, expect, test, vi } from 'vitest'
import { RepositorySettingsPage } from './repository-settings'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })
function reply(value: unknown, status = 200) { return new Response(JSON.stringify(value), { status }) }
function mount() { render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter><RepositorySettingsPage /></MemoryRouter></QueryClientProvider>) }
test('Gitee repository selection imports exact identity without GitHub API requests', async () => {
  const writes: RequestInit[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, options?: RequestInit) => {
    expect(path).not.toContain('/integrations/github')
    if (path.endsWith('/auth/status')) return reply({ mode: 'managed', configured: true, provider: 'gitee' })
    if (path.endsWith('/session')) return reply({ user_id: 'user', csrf_token: 'csrf' })
    if (path.endsWith('/import')) { writes.push(options!); return reply({ items: [{ id: 'project', name: 'owner/repo', created: true }] }) }
    if (path.includes('/repositories?')) return reply({ items: [{ id: '42', owner: 'owner', name: 'repo', default_branch: 'main', private: true }] })
    return reply({ authorization: { id: 'grant', status: 'active', expires_at: '2030-01-01T00:00:00Z' }, callback_url: 'https://ci.test/api/v1/auth/gitee/callback' })
  }))
  mount()
  fireEvent.click(await screen.findByRole('checkbox', { name: /owner\/repo/ }))
  fireEvent.click(screen.getByRole('button', { name: '导入选中仓库' }))
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(JSON.parse(String(writes[0].body))).toEqual({ repositories: [{ id: '42', owner: 'owner', name: 'repo' }] })
  expect(writes[0].headers).toMatchObject({ 'X-CSRF-Token': 'csrf' })
  expect(await screen.findByRole('link', { name: 'owner/repo' })).toHaveAttribute('href', '/projects/project')
})
test('denied Gitee settings do not expose repository controls', async () => {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    if (path.endsWith('/auth/status')) return reply({ mode: 'managed', configured: true, provider: 'gitee' })
    if (path.endsWith('/session')) return reply({ user_id: 'user', csrf_token: 'csrf' })
    return reply({ detail: '需要管理员权限' }, 403)
  }))
  mount()
  expect(await screen.findByRole('alert')).toHaveTextContent('需要管理员权限')
  expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
})
