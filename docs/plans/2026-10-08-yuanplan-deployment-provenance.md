# Gitee Check Run 部署来源元数据

YuanPlan 用户要求所有分支验收、仅 main push 成功后自动发布。服务器轮询 Gitee Check Runs 并验证当前 main SHA，生产配置与只读仓库凭据保留在部署主机。

Check Run 的 head_sha 不能区分同一提交在不同分支的验收。在 ClaimCommitStatus 时从持久化 runs 表读取 event/ref，Gitee output.summary 添加单行 `YUANCI_SOURCE_V1` JSON，包含 event、ref、commit。内容不来自 YAML environment，不需要数据库迁移；GitHub 状态描述保持原契约。

发布消费者要求项目/run 详情链接匹配、来源为 push/refs/heads/main、SHA 一致、最新匹配 Check Run completed/success。旧结果缺少元数据时拒绝。该标记不是数字签名：必须保护仓库及 Check Run 写入权限。

相关包测试和 vet 通过；临时 PostgreSQL 下 Gitee shared-run、commit-status claim/recovery、atomic terminal-state 集成测试通过。真实 Gitee 输出读取和生产自动部署尚未验收；Server/Runner 新版本与项目接入后才能启用。
