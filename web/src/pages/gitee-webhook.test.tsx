import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'
import { GiteeWebhookControls } from './gitee-webhook'
afterEach(() => { cleanup(); vi.unstubAllGlobals() })
test('webhook password is write-only and revision bound', async () => {
  const writes: RequestInit[] = []
  vi.stubGlobal('fetch', vi.fn(async (_: string, options?: RequestInit) => {
    if (options?.method === 'PUT') { writes.push(options); return new Response(null, { status: 204 }) }
    return new Response(JSON.stringify({ revision: 0, configured: false, webhook_url: 'https://ci.test/api/v1/webhooks/gitee/42' }))
  }))
  render(<QueryClientProvider client={new QueryClient()}><GiteeWebhookControls projectID="project" csrf="csrf" onSaved={() => {}} /></QueryClientProvider>)
  fireEvent.change(await screen.findByLabelText('Gitee Webhook 密码'), { target: { value: 's'.repeat(32) } })
  fireEvent.submit(screen.getByRole('button', { name: '保存 Webhook 密码' }).closest('form')!)
  await waitFor(() => expect(writes).toHaveLength(1))
  expect(JSON.parse(String(writes[0].body))).toEqual({ secret: 's'.repeat(32), expected_revision: 0 })
  expect(writes[0].headers).toMatchObject({ 'X-CSRF-Token': 'csrf' })
  expect(await screen.findByLabelText('Gitee Webhook 密码')).toHaveValue('')
})
