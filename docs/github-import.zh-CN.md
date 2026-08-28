# GitHub App 安装与仓库导入

本阶段支持管理员在网页完成 App 配置、安装授权、仓库发现与导入。
仅登记仓库资料；**不代表 Webhook、私有拉取、自动构建、发布已完成**。
不需要在宿主机安装 Go。Gitee、GitLab、Gitea 暂未接入此流程。

## 1. 启动或升级

首次安装请先完成 [managed 模式部署与登录初始化](managed-setup.zh-CN.md)。
这套配置与免登录 Quickstart 独立；文件登录模式也不能在网页保存 App 私钥。
不要改动已有 Quickstart 的数据库，也不要通过关闭登录保护绕过限制。

如果已使用 managed 预览，先备份数据库和对应主密钥，再在仓库根目录运行：

```sh
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml config --quiet
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml up -d --build --wait server
docker compose --env-file .secrets/managed.env -p yuanci-managed -f deploy/compose.managed.yml ps
```

本次包含 `000005`、`000006` 两个向前迁移，Server 启动时在迁移锁保护下执行。
新增 App/安装/授权表和仓库关联字段；`000006` 清除旧的短期仓库发现授权，要求
重新授权，不删除项目、登录配置或成员。不要重复运行 key-init，不要使用 `down -v`。
没有经过验证的自动降级；若需要回退，使用升级前配套数据库、主密钥备份和旧镜像，
不要手工删除列。完整恢复演练仍未达到 v1 发布门槛。

代码的本地提交不会自动更新你正在运行的容器。本轮开发未替你重启现有实例。

## 2. 修改同一个 GitHub App

使用实例管理员登录，点击顶部“仓库接入”。打开 GitHub → Settings → Developer
settings → GitHub Apps，编辑**已经用于 YuanCI 登录的同一个 App**。

| GitHub 设置项 | 填写内容 |
| --- | --- |
| 原登录 Callback URL | 保留 `https://你的域名/api/v1/auth/github/callback` |
| 新增 Callback URL | 复制页面中的 `https://你的域名/api/v1/integrations/github/callback` |
| Setup URL（可选） | 复制页面中的 `https://你的域名/settings/repositories` |
| Redirect on update | 不开启 |
| Repository permissions | 本阶段只需 Metadata: Read-only |
| Webhook Active | 关闭，等待后续接入 |
| Expire user authorization tokens | 保持开启 |

不要用新回调覆盖原登录回调。地址必须与配置的 HTTPS Origin 完全一致，证书应受
浏览器信任。GitHub 的 [App 注册教程](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app)
和 [Setup URL 安全说明](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-setup-url)
解释了这些字段；仅访问安装后返回地址不能证明安装归属。

在 App 的 General 页面复制 **App ID**，生成 RSA 私钥并将 PEM 内容粘贴到 YuanCI。
这里是 App ID + 私钥，不是登录设置里的 Client ID + Client Secret。
点击“验证并加密保存”，系统会向 GitHub 验证私钥、App ID 与当前登录应用是否一致。

- 要求最近 10 分钟内登录，超时可重新登录后再保存。
- 支持单个 2048–4096 位 RSA PEM，PKCS1/PKCS8；不接收带密码的 PEM。
- 输入框提交后清空；失败重试需重新粘贴。API 不返回明文或密文。
- 私钥使用实例主密钥信封加密保存，不进入浏览器缓存或日志。
- 不要提交私钥到 Git 仓库，不要粘贴进聊天、错误报告或代理日志。
- 登录配置发生替换时，需重新验证 App 私钥。私钥替换会让旧发现授权失效，但不删除项目。

## 3. 安装并授权

1. 点击“在 GitHub 安装或调整仓库范围”，选择个人或组织账号，建议只授权需要的仓库。
2. 回到 YuanCI，点击“授权发现仓库”。此操作要求最近 10 分钟内登录。
3. 使用已绑定到当前 YuanCI 管理员的 GitHub 账号授权；不要换用其他人的账号。
4. 在同一浏览器完成跳转，不清除 Cookie，不同时启动多个授权流程。
5. 成功后返回仓库接入页，选择 GitHub 安装账号。

系统通过用户授权与 App 权限的交集发现安装，并用 App JWT 校验 App、稳定账号 ID
和暂停状态；不接受 URL 或表单自行声称的安装归属。只有你在 GitHub 拥有仓库管理员
权限的仓库才可导入。组织安装可能需要组织所有者批准，这不是 YuanCI 能代替的操作。

授权流程 5 分钟内有效、只能完成一次。发现授权在 YuanCI 中最多可用 10 分钟，
绑定当前浏览器会话、已验证 GitHub 身份和配置版本。短期用户 Token 加密保存，不保存
refresh token，不用于创建登录会话或提升权限。服务每分钟清理过期/失效授权密文；
清理失败不延长可用期，重启后继续清理。数据库备份/WAL 中的旧密文仍受备份保留策略约束。
这个本地时限并不改变 GitHub 侧 Token 的有效期；如怀疑泄漏，应在 GitHub 撤销授权。

## 4. 选择导入

每次最多选择当前页 20 个仓库。翻页会清空选择。导入时再次检查 GitHub 权限，以及
YuanCI 会话、管理员权限、身份绑定、配置版本和授权有效期。

- 按 GitHub 稳定账号 ID 创建独立的 YuanCI 组织，不按显示名称合并已有组织。
- 同一仓库多次或并发导入只创建一个项目；返回现有项目，不重复产生创建审计。
- 已存在的不同归属项目不会被自动接管或移动，已停用项目不会被重新启用。
- 重复导入保留现有元数据；仓库重命名、转移与持续同步将在后续实现。
- 不自动创建成员权限，不自动运行 Pipeline，不分发私钥给 Runner。
- 导入和审计在同一事务中完成，任一失败都会回滚本批导入。

点击结果中的仓库名称可打开项目详情。状态“导入时已验证仓库资料”是历史校验记录，
不是当前连接可用性或持续权限同步承诺。普通成员仍需要显式授权；成员管理页面待完善。

## 错误与验收

| 提示/状态码 | 处理方式 |
| --- | --- |
| 401 会话无效或超时 | 重新登录；保存配置、发起授权要求 10 分钟内登录 |
| 403 权限不足 | 检查 YuanCI 实例管理员、GitHub 仓库管理员、App 安装账号及授权账号 |
| 409 配置或授权已变化 | 刷新设置，必要时重新验证私钥、重新授权；归属冲突需人工核对，不修改数据库绕过 |
| 422 配置无效 | 检查 App ID、同一 App 的私钥、PEM 格式和长度 |
| 429 GitHub 限流 | 等待后重试，不连续点击；过期则重新授权 |
| 502 上游不可用 | 检查 Server 到 GitHub 的网络；回调失败需重新开始授权 |
| 无安装/无仓库 | 确认已安装、仓库被包含、你拥有管理员权限；只有 Metadata 权限不会授予你原本没有的仓库权限 |

每次远程发现/导入有 25 秒总超时，单请求 10 秒；安装最多十页、仓库最多一百页，
每页一百项，响应最大 2 MiB。超过边界会明确失败，不静默导入未检查的仓库。
Token 失效不会自动刷新，重新授权即可。安装、OAuth 和 API 的真实性仍需你的沙箱验收。

当前已进行模拟 GitHub 客户端测试、真实 PostgreSQL 事务测试和 UI 测试，**没有使用
你的真实 App 凭据完成外部端到端验收**。建议用测试仓库验证正常导入、重复导入、错误账号、
取消授权、只读仓库与过期授权，然后再继续 Webhook 和 Runner 的后续开发。

开发者视觉夹具：构建网页后运行 `node scripts/github-import-fixture.mjs`，访问
`http://127.0.0.1:18084/settings/repositories`。黄色提示明确标注虚构数据，不连接数据库、
不进行真实授权、拒绝保存凭据，也不包含在 Server 镜像中。用 Ctrl+C 停止。
