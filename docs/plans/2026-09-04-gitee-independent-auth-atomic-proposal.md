# Gitee 接入与独立登录：原子任务拆分提案

状态：待确认；不是 GE-01～GE-04 的完成记录。

## 为什么需要拆分

用户要求优先完成 Gitee，并明确客户不应依赖 GitHub 登录。这一目标正确；
但现有 GE-01 把 OAuth、首次管理员、仓库导入、令牌生命周期和页面接入压在
一个原子任务中，不能满足已批准的 3～8 个手写文件、独立验证和独立提交约束。
依据 `2026-09-03-token-efficient-task-design.md` 的超范围停止规则，本轮只提交
可审查的拆分提案，不将未实现的 Gitee 支持标成完成，也不改写原清单的完成状态。

定点核对发现：

- `internal/identity/oauth.go` 的 ExternalUser.Valid 只接受 github/github.com。
- `internal/provisioning/service.go` 的工厂、回调及加密 AAD 固定为 GitHub。
- `db/migrations/000004_managed_auth.up.sql` 的 login_configs 约束只允许 github。
- 首次管理员记录是 `oauth_bootstrap.github_subject`，相关事务有 GitHub 专用查询。
- `internal/httpapi/browser.go` 和设置页面的登录路由、保存与验证流程固定为 GitHub。
- 仓库发现依赖 GitHub App 安装及用户授权证明；Gitee 不能复用其凭据语义。
- 当前事件编排、凭据服务和状态投递仍有 GitHub 专用检查；不能仅新增 SCM 枚举。

## 目标与边界

1. 全新实例直接选择 Gitee，完成 OAuth 与首次管理员初始化，全程不请求 GitHub。
2. YuanCI 内部用户、会话、组织权限继续独立存在；外部身份由
   `(provider, canonical instance, immutable subject)` 唯一标识。
3. 登录身份与仓库访问授权分开：拥有登录账号不等于能读取其所有仓库。
4. 保留已有 GitHub 用户、会话、加密数据和回调兼容性。追加迁移，不重写历史迁移。
5. 不按同名用户名或同邮箱自动合并 GitHub/Gitee 身份，不因切换平台授予管理员权限。
   已有账号绑定必须走验证流程；不能通过修改数据库绕过重新认证。
6. 本轮实现 Gitee.com；Gitea 的实例地址、证书验证、SSRF 防护及 OAuth 实现仍属于
   GT-01。公共结构保留实例维度，但未实现的 Gitea 入口保持不可启用。
7. 先用独立 Gitee 沙箱验证首次初始化。当前 GitHub 沙箱保留，不以覆盖其认证配置
   的方式假装完成无 GitHub 依赖的首次登录。

可选实现：

- 复制整个 GitHub 登录栈：前期改动直观，但复制管理员事务和安全校验，不采用。
- 只加 Gitee 仓库适配、保留 GitHub 登录：无法满足本次要求，不采用。
- **推荐：先拆出按平台/实例绑定的认证契约，再接入 Gitee，复用内部会话/RBAC。**

## GE-01 拆分与验收

原 GE-01 只有下列子任务全部完成才可标记完成。每项独立提交，先增加最小失败
测试，再实现并跑受影响包/组件。各项以 3～8 个手写文件为目标；实际实施发现仍
超过边界时继续提出拆分，不靠压缩测试或把不同层代码塞进单文件凑数。

| ID | 依赖 | 交付范围 | 定点退出测试 |
| --- | --- | --- | --- |
| GE-01A | E2E-GH-01 | 平台/实例身份、登录配置和首次管理员持久化契约；追加迁移，保留 GitHub 数据与旧加密 AAD 兼容路径 | 升级前后 GitHub 数据可读；同 subject 不跨平台/实例串号；旧密文可解；未知平台不能启用 |
| GE-01B | GE-01A | Gitee OAuth 客户端：授权、换码、身份响应、错误分类和有界 HTTP 请求 | httptest 验证请求/响应契约、state/浏览器绑定衔接、超时/重定向/错误及凭据不出现在错误中 |
| GE-01C | GE-01B | 受保护初始化和登录服务/HTTP 路由：按配置绑定平台，原子消费授权流程、完成首次管理员与既有账号登录 | 全新数据库使用模拟 Gitee 初始化；所有 GitHub 请求设为测试失败仍可登录；跨配置回调、重放、CSRF 和管理员竞争测试 |
| GE-01D | GE-01C | 初始化、登录及认证设置 UI：选择 Gitee，展示正确回调/教程，使用平台对应身份；GitHub 保留兼容 | focused React 测试覆盖 Gitee-only 首次登录、密钥清空、错误/过期、未实现平台禁用；必要的可访问性检查 |
| GE-01E | GE-01C | Gitee 仓库授权与加密令牌生命周期：scope/过期、刷新串行化、失效/撤销、限流与有界重试 | 刷新并发不覆盖新令牌；权限不足拒绝；撤销后不可用；密文隔离；不把长效令牌当 GitHub 短期安装令牌 |
| GE-01F | GE-01D,GE-01E | Gitee 仓库发现/选择导入、受保护 API 和仓库接入页面；组织/仓库外部 ID 按平台/实例隔离 | 分页和管理权限重检、跨用户拒绝、重复导入幂等、同外部 ID 不碰撞；API 与 focused React 测试 |

GE-01B 开发前核实 Gitee OAuth 的实际能力，不能假设其 PKCE、scope、refresh-token
轮换和 GitHub 相同。登录换码不盲目重试；不得把 access token 放入日志或 URL。
若提供方没有某项能力，应明确设计替代保护并记录验证依据，不能静默降级。

## GE-02～GE-04 的后续顺序

原任务 ID 与依赖含义保留；在 GE-01 完成后逐项执行。开始每项时重新核对其
原子边界，若必须继续拆分，按同一停止规则提出子任务。

- **GE-02**：Gitee Webhook 认证、规范化、去重、Fork 拒绝及不可变配置读取；
  接入受保护的项目自动构建流程。必须核对真实签名覆盖的数据及时间戳来源。
  官方说明中的参考签名输入是时间戳与密钥，不应直接宣称它像 GitHub 一样对
  payload 做 HMAC。需要针对重放、payload 替换及仓库绑定设计验证测试。
- **GE-03**：Gitee 私有检出凭据和状态回传接入共享 Job/日志/状态 outbox；
  保留 mTLS、当前租约、Fork 策略、禁止重定向、固定 SHA、脱敏与清理约束。
  核实 Gitee OAuth 凭据是否支持 Git HTTPS 检出和所需状态接口，不假装 PAT 与
  OAuth token 有相同作用域、生命周期或派发安全性。
- **GE-04**：先跑确定性 Gitee E2E，再执行一次对应阶段门禁；准备并执行真实
  Gitee OAuth、私有仓库、Webhook、检出、执行、日志及最终状态验收。记录事件 ID、
  SHA、Run ID、状态回传和必要失败片段，不能以模拟测试替代真实结果。
- GE-04 的全量门禁仍按原清单执行。提前完成 Gitee 不等于 GitLab/Gitea 已完成；
  “四平台门禁”整体继续待验证，不能修改其结论来满足本次进度。

## 真实验收的外部条件

实现不需要在聊天中收取密钥。真实验收需要操作者创建 Gitee OAuth 应用，配置
回调，并通过受保护页面保存凭据、授权账号及指定专用私有测试仓库。届时先测量
控制面与 Runner 对 Gitee、Git 检出以及镜像仓库的实际连通性，不能仅因使用国内
平台就保证全部网络可用。若缺少条件，GE-04 记录待验证，不能标为成功。

参考入口：

- [Gitee OAuth 官方文档](https://gitee.com/api/v5/oauth_doc)
- [Gitee Webhook 密钥验证](https://help.gitee.com/webhook/how-to-verify-webhook-keys)

下一任务提案：**GE-01A**。本提案未获确认前，不据此自动修改身份或认证逻辑。
