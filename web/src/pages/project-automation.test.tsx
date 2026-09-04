import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'
import { ProjectAutomation } from './project-automation'

afterEach(() => { cleanup(); vi.unstubAllGlobals() })
test('enabling requires immutable validation and sends revision plus CSRF', async () => {
 const settings = { enabled: false, pipeline_path: '.yuanci.yml', trigger_push: true, trigger_tag: true, trigger_pull_request: false, cancel_older_commits: true, revision: 4 }
 const fetch = vi.fn(async (path: string, options?: RequestInit) => new Response(JSON.stringify(path.endsWith('/session') ? { user_id: 'u', csrf_token: 'csrf' } : path.endsWith('/validate') ? { valid: true, commit_sha: 'a'.repeat(40), config_sha256: 'b'.repeat(64) } : options?.method === 'PUT' ? { ...settings, enabled: true, revision: 5 } : settings), { headers: { 'Content-Type': 'application/json' } }))
 vi.stubGlobal('fetch', fetch)
 render(<QueryClientProvider client={new QueryClient()}><ProjectAutomation projectID="p" userID="u" /></QueryClientProvider>)
 expect(await screen.findByRole('button', { name: '启用自动构建' })).toBeDisabled()
 fireEvent.click(screen.getByRole('button', { name: '验证当前配置' }))
 await screen.findByText(/已验证提交/)
 fireEvent.click(screen.getByRole('button', { name: '启用自动构建' }))
 await screen.findByRole('button', { name: '停用自动构建' })
 const [, options] = fetch.mock.calls.find(([, opts]) => opts?.method === 'PUT')!
 expect(options?.headers).toMatchObject({ 'X-CSRF-Token': 'csrf' })
 expect(JSON.parse(String(options?.body))).toMatchObject({ expected_revision: 4, enabled: true })
})

test('access failure hides controls', async () => {
 vi.stubGlobal('fetch', vi.fn(async (path: string) => new Response(JSON.stringify(path.endsWith('/session') ? { user_id: 'u', csrf_token: 'csrf' } : { detail: '无项目权限' }), { status: path.endsWith('/session') ? 200 : 403 })))
 render(<QueryClientProvider client={new QueryClient()}><ProjectAutomation projectID="p" userID="u" /></QueryClientProvider>)
 expect(await screen.findByRole('alert')).toHaveTextContent('无项目权限')
 expect(screen.queryByRole('button', { name: '启用自动构建' })).not.toBeInTheDocument()
})
