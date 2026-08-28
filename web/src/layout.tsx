import { NavLink, Outlet } from 'react-router-dom'
import { cn } from './lib/cn'
import { AuthBoundary } from './components/auth-boundary'

const navItems = [
  { to: '/', label: '概览', end: true },
  { to: '/projects', label: '项目', end: false },
  { to: '/pipelines/new', label: 'Pipeline 编辑器', end: false },
  { to: '/settings/auth', label: 'Git 平台设置', end: false },
  { to: '/settings/repositories', label: '仓库接入', end: false },
]

export function Layout() {
  return (
    <div className="min-h-dvh bg-slate-50 text-slate-950">
      <a href="#main-content" className="sr-only rounded bg-white px-3 py-2 text-sm font-medium text-blue-700 focus:not-sr-only focus:absolute focus:left-3 focus:top-3 focus:z-50">跳到主要内容</a>
      <header className="border-b border-slate-200 bg-slate-950 text-white">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center justify-between gap-4 px-4 py-4 sm:px-6 lg:px-8">
          <NavLink to="/" className="flex items-center gap-3 rounded focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-blue-400">
            <span aria-hidden="true" className="grid size-9 place-items-center rounded-lg bg-blue-600 text-sm font-bold">Y</span>
            <span><strong className="block text-base">YuanCI</strong><small className="block text-xs text-slate-400">轻量 CI/CD 控制台</small></span>
          </NavLink>
          <nav aria-label="主要导航">
            <ul className="flex flex-wrap items-center gap-1">
              {navItems.map((item) => (
                <li key={item.to}>
                  <NavLink end={item.end} to={item.to} className={({ isActive }) => cn('inline-flex min-h-11 items-center rounded-md px-3 py-2 text-sm font-medium text-slate-300 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-400', isActive && 'bg-slate-800 text-white')}>
                    {item.label}
                  </NavLink>
                </li>
              ))}
            </ul>
          </nav>
        </div>
      </header>
      <main id="main-content" className="mx-auto max-w-7xl px-4 py-8 sm:px-6 lg:px-8"><AuthBoundary><Outlet /></AuthBoundary></main>
    </div>
  )
}
