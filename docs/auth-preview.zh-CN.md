# GitHub 登录预览部署（开发阶段）

这一增量实现了 GitHub 用户登录、一次性管理员初始化、账号绑定和受保护的
浏览器 API。**不是完整 CI/CD，也不是生产可用版本。** 当前仅支持 GitHub.com
登录，已有登录/退出、账号首页和只读项目浏览，尚未完成仓库导入、成员管理及 Runner mTLS。
如果希望在网页中填写应用配置，请使用新的[网页设置向导部署](managed-setup.zh-CN.md)；
本篇保留的文件配置模式只读，不会被网页修改。

不需要在主机上安装 Go。Docker 会编译后端与前端。现有 Quickstart 仍可用于
可信任务体验；以下步骤另建 `yuanci-auth-preview` 项目、数据库和端口，
不升级或替换 Quickstart。不要合并两份 Compose 文件，不要共用数据卷。

## 1. 准备 GitHub App 和 HTTPS

在 GitHub 的开发者设置中创建一个专门用于测试的 GitHub App：

- Homepage URL：你的 HTTPS 地址，例如 `https://ci.example.com`。
- Callback URL：`https://ci.example.com/api/v1/auth/github/callback`，必须完全一致。
- 登录功能不需要仓库写入、组织管理等权限。不要为登录申请无关权限；
  App 安装和仓库访问会在后续阶段单独实现。
- 获取 **Client ID** 和 **Client secret**，不要把 App ID 或私钥填到这里。
- 创建一个文件保存 Client secret，文件只含密钥（允许末尾换行）。不要把密钥
  放进命令行、提交记录或聊天内容。建议路径：`.secrets/github-client-secret`。

授权流程按照 [GitHub App 用户授权文档](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)
实现 code + state + S256 PKCE。登录取得的用户 Token 仅用于读取身份，不落库；
不等于完成 GitHub App 安装、私有仓库拉取或 Checks 集成。

使用 GitHub 公开用户 API `https://api.github.com/users/你的用户名` 查看 `id`。
把这个**数字用户 ID**配置为首次管理员，不能填写用户名。首次初始化前其他
账号均不能完成登录；初始化后新账号可以登录，但不会自动获得项目或管理权限。

主机的 HTTPS 反向代理需把该域名转发至 `127.0.0.1:8081`，使用浏览器信任的
证书。本 Compose 不配置 DNS 或证书。如果代理本身在另一个容器内，不能把它的
`127.0.0.1`当成宿主机；需另行设计受限网络连接。不要把预览端口公开到公网。
代理也必须避免记录 `/api/v1/auth/github/*` 的查询参数、Cookie 或认证 Header，
因为回调地址中包含短期授权码。应用自身日志只记录请求路径，不记录查询串。

## 2. 准备独立配置

在仓库根目录创建 `.secrets` 文件夹，把
`deploy/auth-preview.env.example` 复制为 `.secrets/auth-preview.env`，按注释填写：

- `YUANCI_POSTGRES_PASSWORD`：独立的长随机密码，建议 64 位十六进制字符，
  避免数据库 URL 中出现需要转义的特殊字符。
- `YUANCI_PUBLIC_ORIGIN`：HTTPS 域名，可含端口，不含路径、查询参数或账号密码。
- `YUANCI_GITHUB_CLIENT_ID`：GitHub App 的 Client ID。
- `YUANCI_BOOTSTRAP_GITHUB_USER_ID`：管理员的数字 GitHub 用户 ID。
- `YUANCI_GITHUB_CLIENT_SECRET_PATH`：上一步密钥文件的绝对路径。
- `YUANCI_AUTH_PREVIEW_PORT`：默认 8081，与 Quickstart 的 8080 分开。

`.secrets` 已从 Git 和 Docker 构建上下文排除。限制宿主机上目录和文件的读取
权限；Server 容器使用 UID 10001，需确保它能读取挂载文件。Compose 将该文件
只读挂载到 `/run/secrets/github_client_secret`。它不是自动加密的密钥管理服务。
不要为了排障把密钥设为全局可写，也不要在日志中输出文件内容。

## 3. 启动与检查

在仓库根目录执行（PowerShell 与 Linux 终端命令相同）：

```sh
docker compose --env-file .secrets/auth-preview.env -p yuanci-auth-preview -f deploy/compose.auth-preview.yml config --quiet
docker compose --env-file .secrets/auth-preview.env -p yuanci-auth-preview -f deploy/compose.auth-preview.yml up -d --build
docker compose --env-file .secrets/auth-preview.env -p yuanci-auth-preview -f deploy/compose.auth-preview.yml ps
docker compose --env-file .secrets/auth-preview.env -p yuanci-auth-preview -f deploy/compose.auth-preview.yml logs --tail=100 server
```

`config --quiet` 只检查配置，不打印包含数据库密码的展开结果。
数据库迁移在启动时执行，只针对此预览数据库。不要将数据库 URL 指向现有实例。

浏览器直接打开 `https://ci.example.com/api/v1/auth/github/start`，使用指定管理员
完成 GitHub 授权；会话有效期为 8 小时。登录成功返回首页，然后可打开
`https://ci.example.com/api/v1/session` 验证用户身份。未登录访问该接口应返回 401。

**首页现在显示账号与会话信息。** 已接入登录与退出，不会再请求缺少项目的运行列表。
文件模式的 Git 平台设置显示只读提示。[项目浏览](project-browser.zh-CN.md)已接入，
但新实例在仓库导入功能完成前可能没有项目。受保护模式的运行 API
要求显式项目和权限，旧 Runner 接口返回 404，所以此模式不能执行构建。
请继续使用独立 Quickstart 体验当前构建能力，切勿为消除页面错误而关闭鉴权。

退出接口为 `DELETE /api/v1/session`，需要会话对应的 `X-CSRF-Token` 和正确
Origin；接口契约见 `api/openapi.yaml`。Session API 返回的 CSRF 值不是登录凭据，
真实会话仅保存在 HttpOnly Cookie 中。不要将这些信息公开分享。

## 4. 常见问题和安全边界

- 无法启动：确认预览模式没有同时启用内存模式、免登录开关或旧 Runner Token。
  Secret 文件必须可读、非空，且不超过 4096 字节。
- GitHub 回调不匹配：检查 HTTPS Origin、Callback URL 和 Client ID；不能使用
  `http://localhost:8081` 代替已配置域名。不要移除 Cookie 的 Secure 属性。
- 回调 400：可能是授权被拒绝、超过五分钟、Cookie 缺失或流程已消费。重新打开
  登录入口，不要反复刷新旧回调。一次只完成一个浏览器登录流程。
- 回调 403：未完成首次初始化时登录了非指定账号。
- 回调 502：检查 Server 到 GitHub 的网络、App 配置和凭据。上游错误正文不会
  返回给浏览器；重新发起登录，不要重试旧授权码。
- 修改 bootstrap ID 后启动失败：这是保护机制。首次配置会持久化，不能通过
  改环境变量转移管理员身份。撤销管理员权限后重新登录也不会自动恢复权限。
- 账号绑定要求十分钟内的登录会话，使用 CSRF 保护的
  `POST /api/v1/auth/github/link`，然后在同一浏览器导航至返回的授权 URL；
  回调必须保留原会话。不能合并已经属于另一个用户的外部身份。

暂缺最后管理员保护、应急管理员恢复、会话管理 UI 和身份异常后的全会话撤销。
不要用此预览保存重要数据或授予生产仓库权限。互联网部署还需代理限流；
当前只做了全局待处理登录数量上限，并非完整的恶意流量防护。

停止预览（保留数据库卷）：

```sh
docker compose --env-file .secrets/auth-preview.env -p yuanci-auth-preview -f deploy/compose.auth-preview.yml down
```

不要加 `-v`，它会删除预览数据。已有 OAuth 协议 Mock、真实 PostgreSQL 回调
集成测试；实际 GitHub App 沙箱登录仍需配置真实 App 后验收。单元测试通过不代表
四个平台集成、生产安全审计或完整 v1 发布门槛已完成。
