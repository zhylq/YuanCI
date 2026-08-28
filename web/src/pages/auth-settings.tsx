import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { ApiError } from '../lib/api'
import { errorMessage, navigateToAuthorization, post, useAuthStatus, useLoginSettings, type LoginSettings } from '../lib/auth'
import { buttonClass, linkClass, Pending } from '../components/auth-boundary'
import { cn } from '../lib/cn'

const fieldClass = 'mt-2 min-h-11 w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-base text-slate-950 focus:border-blue-600 focus:outline-2 focus:outline-blue-600 disabled:bg-slate-100'
const githubDocs = 'https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app'
const providers = [
  { name: 'GitHub', hint: 'GitHub.com · App 用户登录', href: githubDocs, supported: true },
  { name: 'Gitee', hint: 'OAuth 应用 · 尚未接入', href: 'https://gitee.com/api/v5/oauth_doc', supported: false },
  { name: 'GitLab', hint: 'OAuth 应用 · 尚未接入', href: 'https://docs.gitlab.com/integration/oauth_provider/', supported: false },
  { name: 'Gitea', hint: '自托管 OAuth · 尚未接入', href: 'https://docs.gitea.com/development/oauth2-provider/', supported: false },
]

function ProviderList() {
  return <aside aria-labelledby="providers-title">
    <h2 id="providers-title" className="mb-3 text-balance text-sm font-semibold text-slate-600">代码托管平台</h2>
    <ul className="space-y-3">{providers.map(provider => <li key={provider.name} className={cn('rounded-lg border p-4', provider.supported ? 'border-blue-300 bg-blue-50' : 'border-slate-200 bg-white')}>
      <div className="flex items-center justify-between gap-2"><strong className="text-base">{provider.name}</strong><span className="rounded bg-white px-2 py-1 text-xs font-medium text-slate-700">{provider.supported ? '本轮支持' : '待接入'}</span></div>
      <p className="mt-2 text-sm leading-6 text-slate-600">{provider.hint}</p>
      <a className={`${linkClass} mt-3 inline-flex min-h-11 items-center text-sm`} href={provider.href} target="_blank" rel="noreferrer">{provider.name} 官方教程</a>
    </li>)}</ul>
    <p className="mt-4 text-pretty text-sm leading-6 text-slate-600">登录授权与仓库安装、Webhook 配置相互独立。未接入的平台暂不能保存凭据。</p>
  </aside>
}

export function AuthSettingsPage({ setup = false }: { setup?: boolean }) {
  const status = useAuthStatus()
  const enabled = status.data?.mode === 'managed'
  const settings = useLoginSettings(setup, enabled)
  return <div className="space-y-7">
    <header className="flex flex-wrap items-start justify-between gap-4"><div><p className="text-sm font-medium text-blue-700">{setup ? '首次初始化 · 仅部署管理员' : '设置 / 身份与访问'}</p><h1 className="mt-2 text-balance text-3xl font-semibold">{setup ? '为团队配置安全登录' : 'Git 平台设置'}</h1><p className="mt-3 max-w-3xl text-pretty leading-7 text-slate-600">应用由管理员创建一次，团队成员只需授权登录。密钥加密保存，验证通过后才启用。</p></div><span className="rounded-full border border-slate-300 bg-white px-3 py-1 text-sm text-slate-600">开发预览</span></header>
    {setup ? <ol aria-label="初始化步骤" className="grid gap-3 rounded-xl border border-slate-200 bg-white p-4 text-sm sm:grid-cols-3"><li>1. 验证部署权限</li><li>2. 创建并配置应用</li><li>3. 授权验证与启用</li></ol> : null}
    <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_16rem]">
      <div className="min-w-0 space-y-5">
        {!enabled ? <section className="rounded-xl border border-slate-200 bg-white p-6"><h2 className="text-balance text-xl font-semibold">{status.data?.mode === 'file' ? '当前由部署文件管理' : '网页配置需要受保护模式'}</h2><p className="mt-3 text-pretty leading-7 text-slate-600">{status.data?.mode === 'file' ? '现有登录配置来自只读文件，网页不会覆盖它。请按部署文档使用独立 managed 配置模式。' : '免登录 Quickstart 不允许保存应用密钥。请使用独立的 managed Compose，初始化主密钥后从服务器获取设置码。'}</p><p className="mt-3 text-sm leading-6 text-slate-600">仓库中的 docs/managed-setup.zh-CN.md 提供 Docker 操作步骤，不需要安装 Go。</p><AppInstructions callback={status.data?.mode === 'file' ? status.data.callback_url : ''} /></section>
          : settings.isPending ? <Pending />
          : settings.isError ? setup && settings.error instanceof ApiError && settings.error.status === 401 ? <UnlockSetup onUnlocked={() => void settings.refetch()} />
            : <section className="rounded-xl border border-slate-200 bg-white p-6"><h2 className="text-balance text-xl font-semibold">无法读取设置</h2><p role="alert" className="mt-3 text-red-800">{errorMessage(settings.error)}</p><div className="mt-5 flex flex-wrap items-center gap-4"><button className={buttonClass} onClick={() => void settings.refetch()}>重试</button><Link to="/login" className={linkClass}>重新登录</Link></div></section>
          : settings.data ? <>
            {settings.data.active ? <section className="rounded-xl border border-blue-200 bg-blue-50 p-5"><h2 className="text-balance text-lg font-semibold">GitHub 登录已启用</h2><p className="mt-2 break-all text-sm text-slate-700">Client ID：<code>{settings.data.active.client_id}</code></p><p className="mt-2 text-sm text-slate-700">Client Secret：已配置 · 不回显</p><p className="mt-3 text-pretty text-sm leading-6 text-slate-600">替换配置时，当前登录方式会一直保留到新版本验证成功。首次管理员身份不能在此修改。</p></section> : null}
            <section className="rounded-xl border border-slate-200 bg-white p-6 shadow-sm"><AppInstructions callback={settings.data.callback_url} /></section>
            {settings.data.candidate ? <VerifyCandidate settings={settings.data} setup={setup} /> : null}
            <ConfigurationForm key={settings.data.candidate?.id ?? settings.data.active?.id ?? 'new'} settings={settings.data} setup={setup} onSaved={() => void settings.refetch()} />
          </> : null}
      </div>
      <ProviderList />
    </div>
  </div>
}

function UnlockSetup({ onUnlocked }: { onUnlocked: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function unlock(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const input = event.currentTarget.elements.namedItem('setup_code') as HTMLInputElement
    const code = input.value.trim(); input.value = ''
    if (!/^[A-Za-z0-9_-]{43}$/.test(code)) { setError('设置码应为服务器生成的 43 位字符。'); return }
    setBusy(true); setError('')
    try { await post('/api/v1/setup/exchange', { code }); onUnlocked() }
    catch (error) { setError(errorMessage(error)) }
    finally { setBusy(false) }
  }
  return <section className="rounded-xl border border-slate-200 bg-white p-6 shadow-sm"><h2 className="text-balance text-xl font-semibold">先验证你拥有部署权限</h2><p className="mt-3 text-pretty leading-7 text-slate-600">请在部署主机执行文档中的 setup-code 命令。设置码 15 分钟内有效，只能兑换一次；随后有 30 分钟完成设置。</p><form onSubmit={event => void unlock(event)} className="mt-6" aria-busy={busy}>
    <label htmlFor="setup-code" className="text-sm font-semibold">一次性设置码</label><input id="setup-code" name="setup_code" type="password" required autoComplete="off" maxLength={43} className={fieldClass} aria-describedby="setup-code-help setup-code-error" aria-invalid={Boolean(error)} disabled={busy} />
    <p id="setup-code-help" className="mt-2 text-sm leading-6 text-slate-600">允许粘贴。不要分享设置码，它不会出现在地址栏或浏览器本地存储。</p><p id="setup-code-error" role={error ? 'alert' : undefined} className="mt-2 text-sm text-red-800">{error}</p><button className={`${buttonClass} mt-5`} disabled={busy}>{busy ? '正在验证…' : '验证设置码'}</button>
    </form></section>
}

function AppInstructions({ callback }: { callback: string }) {
  const [copyState, setCopyState] = useState('')
  async function copy() {
    try { await navigator.clipboard.writeText(callback); setCopyState('回调地址已复制。') }
    catch { setCopyState('复制未完成，请手动选择上方地址并复制。') }
  }
  return <div><h2 className="text-balance text-xl font-semibold">创建 GitHub App</h2><ol className="mt-4 list-decimal space-y-3 pl-5 text-pretty leading-7 text-slate-700">
    <li>进入 GitHub 的 Settings → Developer settings → GitHub Apps，创建由你或组织持有的应用。<a href={githubDocs} className={`${linkClass} ml-1`} target="_blank" rel="noreferrer">查看创建教程</a></li>
    <li>Homepage URL 填写本实例的 HTTPS 地址；Callback URL 使用下方地址，不要填写 Webhook URL。</li>
    <li>本轮只验证登录身份，不申请仓库写入或组织管理权限。不使用 Webhook 时关闭其 Active 选项，保留用户 Token 过期选项。</li>
    <li>复制 Client ID，生成 Client secret。首次管理员使用 GitHub 用户 API 返回的数字 id，而不是用户名或 App ID。</li>
  </ol>{callback ? <div className="mt-5 rounded-lg bg-slate-50 p-4"><label htmlFor="callback-url" className="text-sm font-semibold">登录回调地址（Callback URL）</label><div className="mt-2 flex flex-col gap-2 sm:flex-row"><input id="callback-url" value={callback} readOnly className="min-h-11 min-w-0 flex-1 rounded border border-slate-300 bg-white px-3 font-mono text-sm" /><button type="button" onClick={() => void copy()} className={buttonClass}>复制地址</button></div><p role="status" className="mt-2 text-sm text-slate-600">{copyState || '地址由部署配置确定，必须与 GitHub 中填写的内容完全一致。'}</p></div> : <p className="mt-4 rounded-lg bg-slate-100 p-4 text-sm leading-6 text-slate-700">启动受保护模式并配置 HTTPS Origin 后，此处会显示真实回调地址，不会生成不可用的示例地址。</p>}</div>
}

function ConfigurationForm({ settings, setup, onSaved }: { settings: LoginSettings; setup: boolean; onSaved: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [invalid, setInvalid] = useState<Record<string, string>>({})
  const current = settings.candidate ?? settings.active
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const data = new FormData(form)
    const clientID = String(data.get('client_id') ?? '').trim()
    const secret = String(data.get('client_secret') ?? '')
    const subject = String(data.get('bootstrap_subject') ?? '').trim()
    const fields: Record<string, string> = {}
    if (!clientID) fields.client_id = '请填写 Client ID。'
    if (secret.length < 16) fields.client_secret = '请填写完整 Client Secret（至少 16 个字符）。'
    if (!/^[1-9][0-9]*$/.test(subject)) fields.bootstrap_subject = '请填写不含前导零的正整数用户 ID。'
    setInvalid(fields); setError('')
    if (Object.keys(fields).length) return
    // Never put a secret into React Query mutation variables or browser storage.
    ;(form.elements.namedItem('client_secret') as HTMLInputElement).value = ''
    setBusy(true)
    try {
      await post(setup ? '/api/v1/setup/settings' : '/api/v1/settings/auth/github', { client_id: clientID, client_secret: secret, bootstrap_subject: subject, expected_active: settings.active?.id ?? null }, settings.csrf_token)
      onSaved()
    } catch (error) { setError(errorMessage(error)) }
    finally { setBusy(false) }
  }
  return <section className="rounded-xl border border-slate-200 bg-white p-6 shadow-sm"><h2 id="config-form-title" className="text-balance text-xl font-semibold">{settings.active ? '准备替换配置' : '填写应用信息'}</h2><p className="mt-2 text-pretty text-sm leading-6 text-slate-600">保存只创建待验证版本，不会立即启用。再次保存会替换你之前的待验证版本。</p>
    <form onSubmit={event => void save(event)} aria-labelledby="config-form-title" aria-busy={busy} noValidate className="mt-6 space-y-5"><fieldset disabled={busy} className="space-y-5">
      <ConfigField name="client_id" label="Client ID" defaultValue={current?.client_id} help="填写应用的 Client ID，不是 App ID。" error={invalid.client_id} />
      <ConfigField name="client_secret" label="Client Secret" secret help="密钥仅加密保存，提交后输入框会清空；失败重试时需要重新填写。" error={invalid.client_secret} />
      <ConfigField name="bootstrap_subject" label="首次管理员 GitHub 数字 ID" defaultValue={current?.bootstrap_subject} readOnly={!setup} help={setup ? '例如通过 https://api.github.com/users/你的用户名 查看 id。只有此账号能完成首次授权。' : '首次管理员身份已固定，修改应用凭据不会转移或恢复管理员权限。'} error={invalid.bootstrap_subject} />
      {error ? <p role="alert" className="rounded-lg bg-red-50 p-3 text-sm leading-6 text-red-800">{error}</p> : null}
      <button className={buttonClass} disabled={busy}>{busy ? '正在加密保存…' : '保存待验证配置'}</button>
    </fieldset></form>
  </section>
}
function ConfigField({ name, label, help, error, secret, defaultValue, readOnly }: { name: string; label: string; help: string; error?: string; secret?: boolean; defaultValue?: string; readOnly?: boolean }) {
  return <div><label htmlFor={name} className="text-sm font-semibold">{label}</label><input id={name} name={name} type={secret ? 'password' : 'text'} defaultValue={defaultValue ?? ''} required readOnly={readOnly} autoComplete={secret ? 'new-password' : 'off'} inputMode={name === 'bootstrap_subject' ? 'numeric' : undefined} maxLength={secret ? 4096 : name === 'bootstrap_subject' ? 19 : 128} className={fieldClass} aria-describedby={`${name}-help${error ? ` ${name}-error` : ''}`} aria-invalid={Boolean(error)} /><p id={`${name}-help`} className="mt-2 break-words text-sm leading-6 text-slate-600">{help}</p>{error ? <p id={`${name}-error`} role="alert" className="mt-1 text-sm text-red-800">{error}</p> : null}</div>
}
function VerifyCandidate({ settings, setup }: { settings: LoginSettings; setup: boolean }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function verify() {
    setBusy(true); setError('')
    try {
      const result = await post<{ authorization_url: string }>(setup ? '/api/v1/setup/verify' : '/api/v1/settings/auth/github/verify', { candidate_id: settings.candidate!.id }, settings.csrf_token)
      navigateToAuthorization(result.authorization_url)
    } catch (error) { setError(errorMessage(error)); setBusy(false) }
  }
  return <section aria-labelledby="verify-title" className="rounded-xl border border-blue-300 bg-blue-50 p-6"><h2 id="verify-title" className="text-balance text-xl font-semibold">配置已保存，等待授权验证</h2><p className="mt-3 break-all text-sm text-slate-700">待验证 Client ID：<code>{settings.candidate!.client_id}</code></p><p className="mt-2 text-pretty text-sm leading-6 text-slate-700">{setup ? `请使用数字 ID 为 ${settings.candidate!.bootstrap_subject} 的 GitHub 账号授权。` : '请使用已绑定到当前管理员账号的 GitHub 身份授权。'} 验证成功后自动启用此版本；失败时保留现有登录配置。</p><p className="mt-2 text-sm text-slate-600">候选配置到期：<time className="tabular-nums" dateTime={settings.candidate!.expires_at}>{new Date(settings.candidate!.expires_at).toLocaleString()}</time></p><button className={`${buttonClass} mt-4`} onClick={() => void verify()} disabled={busy}>{busy ? '正在准备授权…' : '前往 GitHub 验证并启用'}</button>{error ? <p role="alert" className="mt-3 text-sm text-red-800">{error}</p> : null}<p className="mt-3 text-sm leading-6 text-slate-600">请在同一浏览器中完成，勿清除 Cookie 或同时开启多个授权流程。</p></section>
}
