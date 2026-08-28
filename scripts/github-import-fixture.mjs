// UI fixture ONLY: synthetic API responses, no real OAuth, database or credentials.
// Build web first; run `node scripts/github-import-fixture.mjs` from the repo root.
import http from 'node:http'
import { readFile } from 'node:fs/promises'

const root = new URL('../internal/webui/dist/', import.meta.url)
const port = 18084
const base = '/api/v1/integrations/github'
const modes = { ready: '已授权', unconfigured: '未配置', denied: '无权限', expired: '授权过期', limited: 'GitHub 限流', evaluation: '体验模式' }
let mode = 'ready'
const authorizedUntil = new Date(Date.now() + 600_000).toISOString()
function json(res, value, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); res.end(JSON.stringify(value))
}
const server = http.createServer(async (req, res) => {
  try {
    if (req.headers.host !== `127.0.0.1:${port}`) { res.writeHead(403); res.end(); return }
    const url = new URL(req.url, `http://127.0.0.1:${port}`)
    if (req.method === 'POST') {
      // Only a simulated import is supported. Refuse credential save/OAuth.
      if (url.pathname !== `${base}/import` || mode !== 'ready') return json(res, { detail: '视觉夹具不接收真实凭据或 OAuth 操作。' }, 403)
      req.resume()
      return json(res, { items: [{ id: '10000000-0000-4000-8000-000000000001', name: 'fixture-team/yuanci-demo', created: true }] })
    }
    if (req.method !== 'GET') { res.writeHead(405); res.end(); return }
    if (url.pathname.startsWith('/__fixture/')) {
      const next = url.pathname.slice('/__fixture/'.length)
      if (!Object.hasOwn(modes, next)) { res.writeHead(404); res.end(); return }
      mode = next; res.writeHead(303, { Location: '/settings/repositories' }); res.end(); return
    }
    if (url.pathname === '/api/v1/auth/status') return json(res, { mode: mode === 'evaluation' ? 'evaluation' : 'managed', configured: true, initialized: true })
    if (url.pathname === '/api/v1/session') return json(res, { user_id: 'fixture-admin', display_name: '视觉夹具管理员', csrf_token: 'not-a-real-credential' })
    if (url.pathname.startsWith(base)) {
      if (mode === 'denied') return json(res, { detail: '测试无权限' }, 403)
      if (url.pathname === base) return json(res, { app: mode === 'unconfigured' ? null : { id: 'fixture-revision', app_id: '12', client_id: 'Iv1.fixture', slug: 'fixture-app' }, needs_verification: false, callback_url: 'https://ci.example.test/api/v1/integrations/github/callback', setup_url: 'https://ci.example.test/settings/repositories', authorized_until: ['ready', 'limited'].includes(mode) ? authorizedUntil : undefined })
      if (mode === 'limited') return json(res, { detail: 'GitHub 请求受限，请稍后重试。' }, 429)
      if (url.pathname.endsWith('/installations')) return json(res, { items: [{ id: '34', account_id: '56', account: 'fixture-team' }] })
      if (url.pathname.endsWith('/repositories')) return json(res, { items: [{ id: '70', owner: 'fixture-team', name: 'yuanci-demo', default_branch: 'main' }, { id: '71', owner: 'fixture-team', name: 'service-with-a-long-name-for-layout-verification', default_branch: 'main' }] })
    }
    if (url.pathname.startsWith('/api/')) return json(res, { detail: '视觉夹具未实现此接口' }, 404)
    if (/^\/assets\/[A-Za-z0-9_-]+\.(js|css)$/.test(url.pathname)) {
      const bytes = await readFile(new URL(url.pathname.slice(1), root)); res.writeHead(200, { 'Content-Type': url.pathname.endsWith('.js') ? 'text/javascript' : 'text/css' }); res.end(bytes); return
    }
    const html = await readFile(new URL('index.html', root), 'utf8')
    const notice = `<aside style="padding:12px;background:#fef3c7;color:#78350f;font:14px sans-serif">视觉夹具 · 虚构数据 · 不验证真实 GitHub 授权<nav aria-label="测试状态">${Object.entries(modes).map(([key,label])=>`<a style="display:inline-block;padding:10px" href="/__fixture/${key}">${label}</a>`).join('')}</nav></aside>`
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' }); res.end(html.replace('<body>', `<body>${notice}`))
  } catch { res.writeHead(500); res.end('Build web before using the fixture.') }
})
server.listen(port, '127.0.0.1', () => console.log(`UI fixture only: http://127.0.0.1:${port}/settings/repositories`))
