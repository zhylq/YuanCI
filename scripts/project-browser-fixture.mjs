// Visual testing only. No database, credentials, external requests, or real auth.
// Run after `cd web && npm run build`, then `node scripts/project-browser-fixture.mjs`.
import http from 'node:http'
import { readFile } from 'node:fs/promises'

const root = new URL('../internal/webui/dist/', import.meta.url)
const port = 18083
let mode = 'ready'
const modes = { ready: '正常数据', empty: '空列表', denied: '权限撤销', expired: '会话过期', offline: '服务故障', evaluation: '体验模式' }
const projects = Array.from({ length: 23 }, (_, n) => ({
  id: `10000000-0000-4000-8000-${String(n + 1).padStart(12, '0')}`,
  organization: { id: '20000000-0000-4000-8000-000000000001', name: '视觉测试团队' },
  provider: 'github', owner: 'fixture-team', name: n === 0 ? 'yuanci-demo' : `service-${String(n).padStart(2, '0')}`,
  default_branch: 'main', connection_status: 'not_connected',
}))
const runs = Array.from({ length: 23 }, (_, n) => ({
  id: `30000000-0000-4000-8000-${String(n + 1).padStart(12, '0')}`,
  pipeline_name: n === 0 ? '测试与镜像构建' : `CI pipeline ${n}`, event: 'push',
  ref: 'refs/heads/feature/long-branch-name-for-responsive-testing',
  commit_sha: 'abcdef0123456789abcdef0123456789abcdef0123',
  status: ['succeeded', 'failed', 'running'][n % 3], created_at: '2026-08-28T08:00:00Z',
}))
function json(res, value, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' })
  res.end(JSON.stringify(value))
}
const server = http.createServer(async (req, res) => {
  try {
    if (req.headers.host !== `127.0.0.1:${port}` && req.headers.host !== `localhost:${port}`) { res.writeHead(403); res.end(); return }
    if (req.method !== 'GET') { res.writeHead(405); res.end(); return }
    const url = new URL(req.url, `http://127.0.0.1:${port}`)
    if (url.pathname.startsWith('/__fixture/')) {
      const next = url.pathname.slice('/__fixture/'.length)
      if (!Object.hasOwn(modes, next)) { res.writeHead(404); res.end(); return }
      mode = next
      res.writeHead(303, { Location: '/projects' }); res.end(); return
    }
    if (url.pathname === '/api/v1/auth/status') return json(res, { mode: mode === 'evaluation' ? 'evaluation' : 'file', configured: true, initialized: true })
    if (url.pathname === '/api/v1/session') return json(res, { user_id: 'visual-fixture-user', display_name: '测试用户', expires_at: '2030-01-01T00:00:00Z', csrf_token: 'not-a-real-credential' })
    if (url.pathname.startsWith('/api/v1/projects')) {
      if (mode === 'denied') return json(res, { detail: 'fixture denied' }, 404)
      if (mode === 'expired') return json(res, { detail: 'fixture expired' }, 401)
      if (mode === 'offline') return json(res, { detail: 'fixture unavailable' }, 503)
      if (url.pathname === '/api/v1/projects') {
        const search = (url.searchParams.get('q') ?? '').toLowerCase()
        const items = mode === 'empty' ? [] : projects.filter(item => `${item.owner}/${item.name}`.toLowerCase().includes(search) && item.id > (url.searchParams.get('after') ?? ''))
        return json(res, { items: items.slice(0, 20), next_cursor: items.length > 20 ? items[19].id : undefined })
      }
      const parts = url.pathname.split('/')
      const project = projects.find(item => item.id === parts[4])
      if (!project) return json(res, { detail: 'fixture unavailable' }, 404)
      if (parts.length === 5) return json(res, project)
      if (parts.length === 6 && parts[5] === 'runs') {
        const items = mode === 'empty' ? [] : url.searchParams.has('after') ? runs.slice(20) : runs.slice(0, 20)
        return json(res, { items, next_cursor: !url.searchParams.has('after') && items.length ? 'visual-fixture-next-page' : undefined })
      }
    }
    if (url.pathname.startsWith('/api/')) return json(res, { detail: 'fixture endpoint unavailable' }, 404)
    if (/^\/assets\/[A-Za-z0-9_-]+\.(js|css)$/.test(url.pathname)) {
      const bytes = await readFile(new URL(url.pathname.slice(1), root))
      res.writeHead(200, { 'Content-Type': url.pathname.endsWith('.js') ? 'text/javascript' : 'text/css' }); res.end(bytes); return
    }
    const html = await readFile(new URL('index.html', root), 'utf8')
    const notice = `<aside style="padding:12px;background:#fef3c7;color:#78350f;font:14px sans-serif">视觉测试夹具 · 全部为虚构数据 · 不验证真实登录 <nav aria-label="测试状态">${Object.entries(modes).map(([key,label])=>`<a style="display:inline-block;padding:10px" href="/__fixture/${key}">${label}</a>`).join('')}</nav></aside>`
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' })
    res.end(html.replace('<body>', `<body>${notice}`))
  } catch {
    res.writeHead(500); res.end('Build the console before running the visual fixture.')
  }
})
server.listen(port, '127.0.0.1', () => console.log(`Visual fixture ONLY: http://127.0.0.1:${port}/projects`))
