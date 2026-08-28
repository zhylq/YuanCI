# 通过网页配置 Git 平台登录

适合不熟悉 Go 的部署管理员。只需 Docker/Compose；第三方应用由你在 Git 平台
创建，再按网页提示填写配置。团队其他成员只需授权登录，不需要各自创建应用。

当前 GitHub.com 登录配置流程已经实现；Gitee、GitLab、Gitea 提供官方教程入口，
明确显示“待接入”，不能保存凭据。仓库安装授权、Webhook、构建执行、CD 不等于
登录配置，仍按开发路线推进。本模式仍为开发预览，不用于生产发布。

登录后可使用[项目浏览与仓库详情](project-browser.zh-CN.md)。仓库导入尚未接入，
因此新实例可能没有可见项目；本轮不会自动创建演示仓库或开放构建执行。

## 1. 准备独立实例与 HTTPS

不要把这份 Compose 叠加到 Quickstart 上。默认项目 `yuanci-managed` 拥有独立
数据库和主密钥卷，端口是 `8082`，不会直接迁移现有 Quickstart 数据。
原文件配置模式仍保留；本轮不提供文件模式到网页模式的原地迁移。

在仓库根目录创建 `.secrets` 文件夹，把 `deploy/managed.env.example` 复制成
`.secrets/managed.env`，填写：

```dotenv
YUANCI_POSTGRES_PASSWORD=替换为独立的长随机十六进制密码
YUANCI_PUBLIC_ORIGIN=https://ci.example.com
YUANCI_MANAGED_PORT=8082
```

密码建议 64 位随机十六进制字符，避免数据库 URL 转义问题。`.secrets` 已被 Git
和 Docker 构建上下文排除，但仍须限制宿主机的文件权限，不能公开分享或提交。

HTTPS 反向代理应将该域名转发到宿主机的 `127.0.0.1:8082`。证书必须受浏览器
信任；不要关闭证书验证，也不要取消 Cookie 的 Secure 属性。代理容器的 localhost
并不是宿主机，需要另行配置受限网络。控制面本身不挂载 Docker Socket。

代理不得记录 OAuth 回调的查询串、Cookie 或认证 Header；也不要记录设置请求正文。
本应用会隐藏敏感错误，并对 API 使用 no-store，但无法控制代理的日志配置。

## 2. 初始化主密钥并启动

以下命令都在仓库根目录执行，PowerShell 和 Linux 相同。

```sh
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml config --quiet
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml run --rm --build key-init
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml up -d --build --wait
```

`key-init` 只执行一次。它以 UID 10001 在独立卷中创建权限为 0600 的主密钥文件，
不打印密钥，不覆盖已存在的文件。Server 以同一 UID 只读挂载该卷。主密钥还会与
数据库绑定，误换密钥时拒绝启动，避免悄悄写入无法解密的混合数据。

日后重启或升级只执行 `up`，不要重复生成主密钥，不要删除密钥卷。
`config --quiet` 只校验配置，不会打印展开后的数据库密码。

```sh
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml ps
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml logs --tail=100 server
```

## 3. 获取一次性设置码

```sh
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml exec -T server yuancictl setup-code
```

命令输出一行设置码。在浏览器打开 `https://ci.example.com/setup`，粘贴并验证。

- 设置码 15 分钟内有效，只能兑换一次；数据库仅存摘要。
- 兑换后建立 30 分钟的 HttpOnly 设置会话，不是管理员登录。
- 如果过期或遗失，可再次执行命令；旧码、旧设置会话和旧候选配置随即失效。
- 初始化完成后不能再签发设置码。普通访客不会成为“第一个管理员”。
- 设置码属于短期敏感凭据，不放进 URL，不发到聊天或问题单中。

## 4. 按网页教程创建 GitHub App

1. 打开 GitHub Settings → Developer settings → GitHub Apps，创建个人或组织持有
   的 App。网页提供[官方注册教程](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app)。
2. Homepage URL 填写本实例 HTTPS 地址。复制页面中的 Callback URL：
   `https://ci.example.com/api/v1/auth/github/callback`，必须完全匹配。
3. 这里只配置用户登录，不申请仓库写入、组织管理等无关权限；不用 Webhook 时
   关闭 Active。保留用户 Token 过期选项。仓库安装与 Checks 会单独实现。
4. 将 **Client ID** 和生成的 **Client secret** 填入 YuanCI，不能填写 App ID 或私钥。
5. 通过 `https://api.github.com/users/你的用户名` 查看 `id`，填写首位管理员的
   **数字用户 ID**，不是用户名。确认该账号由你或指定管理员实际控制。

点击“保存待验证配置”后，密钥输入框清空。此时配置只是候选版本，没有启用。
页面仅显示 Client ID、候选状态、有效期和“密钥已配置”，不会回显明文或密文。

点击“前往 GitHub 验证并启用”，用指定的账号完成真实授权。系统使用 state、
浏览器绑定和 PKCE 校验，确认身份后才同时启用配置、初始化管理员并创建会话。
成功后进入账号首页；失败则不会半初始化。用户 Token 仅用于读取身份，不落库。

## 5. 后续修改与成员登录

实例管理员登录后进入“Git 平台设置”，可以保存并验证替换配置。普通项目管理员
或成员不能读取这些设置。管理员保存/验证操作要求最近十分钟内登录；超时请重新登录。

新配置验证必须使用已绑定到当前管理员账号的 GitHub 身份，且保持同一浏览器会话。
旧配置在验证成功前继续工作；过期、被更新、账号不符或权限已撤销时拒绝启用。
每个 OAuth 流程绑定具体配置版本，不会拿另一版本的密钥交换授权码。

首次管理员数字 ID 不可通过配置页面变更。被撤销的管理员权限也不会因登录自动恢复。
普通成员点击“使用 GitHub 登录”即可；新账号没有默认项目权限。成员管理、最后管理员
保护和应急账号恢复尚未全部完成，勿在此预览中维护生产访问权限。

## 6. 常见问题

- **key-init 提示目标已存在**：正常保护行为，不要删除或覆盖。已有密钥时直接启动。
- **Server 提示 master key mismatch**：恢复与该数据库匹配的原密钥，不能重置摘要绕过。
- **设置码无法兑换**：检查是否过期、重复兑换或已经初始化，以及 HTTPS Origin 是否匹配。
- **回调 400 / 登录流程过期**：回到 `/setup` 或设置页重新发起验证，不刷新旧回调；
  一次只完成一个授权流程。授权码交换前 state 已消费，网络失败也需重新开始。
- **回调 403**：首次授权账号不是指定管理员，或替换时验证了另一个账号/权限已被撤销。
- **回调 409**：候选版本被替换、已过期或当前配置已变化。刷新设置后重试。
- **回调 502**：检查 Client ID/Secret、App 回调地址及 Server 到 GitHub 的网络。
  回调失败目前返回安全的 API 错误页，返回设置页重试；不会将上游正文回显。
- **保存失败后密钥为空**：安全设计如此，重试时重新填写。
- **无法启动构建**：此模式没有旧 Runner 接口，等待 Runner mTLS 与项目接入，不要开启免登录模式绕过。

不要混用 `YUANCI_GITHUB_CLIENT_ID`、旧 Secret 文件、bootstrap 环境变量或 Runner
共享 Token。Managed 模式只从受保护设置流程获取 GitHub 配置。

## 7. 备份与停止

数据库备份必须与主密钥一起保存。单独备份数据库不能恢复应用凭据。
可将只读挂载的主密钥复制到受限的备份目录：

```sh
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml cp server:/run/yuanci-keys/master-key .secrets/master-key.backup
```

此文件是高敏感备份，需加密保存并限制读取权限。正式恢复演练和主密钥轮换仍是后续
发布门槛；不要仅凭备份命令成功就认定具备完整恢复能力。

```sh
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml down
```

不要加 `-v`，否则数据库和主密钥卷会被删除。生产发布仍须通过四平台沙箱、
Runner 隔离、故障恢复、长时间并发运行和独立安全审计等验收。
