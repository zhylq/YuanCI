import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, expect, test, vi } from 'vitest'
import { AuthBoundary, LoginPage } from './auth-boundary'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })
test('Gitee instance offers its own login without GitHub dependency', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ mode: 'managed', initialized: true, configured: true, provider: 'gitee' }))))
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><LoginPage /></MemoryRouter></QueryClientProvider>)
  expect(await screen.findByRole('link', { name: '使用 Gitee 登录' })).toHaveAttribute('href', '/api/v1/auth/gitee/start')
  expect(screen.queryByRole('link', { name: '使用 GitHub 登录' })).not.toBeInTheDocument()
})
test('anonymous authenticated mode navigates to login, not the dashboard', async () => {
  const fetch = vi.fn(async (path: string) => new Response(JSON.stringify(path.endsWith('/auth/status') ? { mode: 'managed', initialized: true, configured: true } : { detail: 'no session' }), { status: path.endsWith('/session') ? 401 : 200 }))
  vi.stubGlobal('fetch', fetch)
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={cache}><MemoryRouter><Routes><Route path="/" element={<AuthBoundary><p>private dashboard</p></AuthBoundary>} /><Route path="/login" element={<LoginPage />} /></Routes></MemoryRouter></QueryClientProvider>)
  expect(await screen.findByRole('link', { name: '使用 GitHub 登录' })).toHaveAttribute('href', '/api/v1/auth/github/start')
  expect(screen.queryByText('private dashboard')).not.toBeInTheDocument()
  expect(fetch.mock.calls.every(call => !call[0].includes('/runs'))).toBe(true)
})
test('status failure does not fall back to evaluation access', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => { throw new Error('offline') }))
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><AuthBoundary><p>private dashboard</p></AuthBoundary></MemoryRouter></QueryClientProvider>)
  expect(await screen.findByRole('alert')).toHaveTextContent('安全状态')
  expect(screen.queryByText('private dashboard')).not.toBeInTheDocument()
})
