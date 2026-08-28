# YuanCI 中文快速入门

这份指南面向希望直接使用 Docker 体验 YuanCI、但不熟悉 Go 的用户。
通过 Docker 运行时，不需要在电脑上安装 Go、Node.js 或 PostgreSQL。

> 当前版本是早期开发版本，只适合本机体验或可信的内部测试任务。OAuth、
> RBAC、Runner mTLS 和完整的 GitHub App 接入尚未完成，请勿直接暴露到公网或
> 用于生产发布。

GitHub 登录已加入独立的[网页设置向导](managed-setup.zh-CN.md)，支持设置码、
应用教程、配置验证和登录/退出，与下方免登录 Quickstart 分开部署。
受保护预览暂不含项目选择或构建执行，不改变本指南的用途与安全限制。

## 1. 准备环境

需要：

- Windows 10/11、macOS 或 Linux；
- Docker Desktop，或者 Linux 上的 Docker Engine；
- Docker Compose v2；
- 至少 2 GB 可用内存和 5 GB 可用磁盘；
- 本机 `8080` 端口未被其他程序占用。

Windows 用户应确认 Docker Desktop 正在使用 Linux containers。

## 2. 下载代码

```bash
git clone https://github.com/zhylq/YuanCI.git
cd YuanCI
```

如果你已经在本项目目录中，可以跳过这一步。

## 3. 创建本地配置

复制示例配置：

Windows PowerShell：

```powershell
Copy-Item .env.example .env
```

Linux 或 macOS：

```bash
cp .env.example .env
```

用文本编辑器打开 `.env`，至少替换下面两项：

```dotenv
YUANCI_POSTGRES_PASSWORD=请替换为一个较长且随机的数据库密码
YUANCI_RUNNER_SHARED_TOKEN=请替换为至少32字节的随机Runner令牌
```

`.env` 已被 Git 和 Docker 构建忽略，不要手动强制提交。当前 Quickstart 尚未使用
`YUANCI_MASTER_KEY_BASE64`，可以暂时保留示例值。

## 4. 一键启动

在仓库根目录执行：

```bash
docker compose --env-file .env -f deploy/compose.quickstart.yml up -d --build
```

首次启动会下载基础镜像并编译前后端，耗时取决于网络速度。查看状态：

```bash
docker compose --env-file .env -f deploy/compose.quickstart.yml ps
```

当 `postgres` 和 `server` 显示 `healthy`、`runner` 显示 `Up` 后，打开：

- 控制台：<http://localhost:8080>
- 健康检查：<http://localhost:8080/readyz>

Quickstart 默认只绑定本机 `127.0.0.1`，不对局域网或公网开放。已有容器需要
重新执行上面的 `up -d --build` 后才会应用新的端口绑定；只改文件不会改变
正在运行的容器。当前没有正式身份认证，不要改成 `0.0.0.0` 对外开放。

## 5. 当前版本可以做什么

Web 控制台目前支持：

- 查看 Server 状态和最近的流水线运行；
- 在 Pipeline 编辑器中编辑并校验 `.yuanci.yml`；
- 查看编译后的 Stage、Job 和 DAG 执行计划。

Runner 可以从 PostgreSQL 事务队列领取 Job，并在隔离的 Docker 容器中
执行命令。GitHub 适配器已经实现和测试，但 GitHub App 登录、仓库连接页面
以及 Webhook 到自动流水线的完整链路仍在开发，因此现在还不能在 UI 中添加
GitHub 仓库后自动触发构建。

后端已新增可撤销会话、成员权限、审计和受保护 API，并通过数据库集成测试。
但 OAuth 登录及管理员初始化尚未接通，运行程序暂未启用这些受保护入口，
因此页面还没有正式登录功能。不要通过手工写数据库来制造登录凭据，也不要
移除生产禁用保护；当前仍只适合本机体验。

示例 Pipeline 位于 `examples/pipelines/basic.yuanci.yml`。可以复制其内容到
控制台的 Pipeline 编辑器进行校验。

## 6. 查看日志和排查问题

查看全部日志：

```bash
docker compose --env-file .env -f deploy/compose.quickstart.yml logs -f
```

只看控制面或 Runner：

```bash
docker compose --env-file .env -f deploy/compose.quickstart.yml logs -f server
docker compose --env-file .env -f deploy/compose.quickstart.yml logs -f runner
```

常见问题：

- `8080` 端口被占用：停止占用该端口的程序，或修改 Compose 中的端口映射。
- Runner 无法启动：确认 Docker Desktop/Engine 正在运行，并允许挂载 Docker
  Socket。
- Server 一直不健康：先查看 `postgres` 和 `server` 日志，通常是 `.env` 中的
  数据库密码未配置或旧数据卷使用了另一密码。
- PowerShell 请求本机地址返回代理错误：访问 `localhost`，或把
  `localhost,127.0.0.1` 加入 `NO_PROXY`。

## 7. 停止、重启和删除

停止服务但保留数据库数据：

```bash
docker compose --env-file .env -f deploy/compose.quickstart.yml down
```

重新启动：

```bash
docker compose --env-file .env -f deploy/compose.quickstart.yml up -d
```

更新代码后重新构建：

```bash
git pull
docker compose --env-file .env -f deploy/compose.quickstart.yml up -d --build
```

不要随意执行 `docker compose down -v`，`-v` 会删除 PostgreSQL 数据卷。

## 8. 开发者模式

只有修改源码时才需要 Go 和 Node.js：

- Go：编译 Server、Runner 和 CLI；
- Node.js/npm：编译 React 控制台；
- PostgreSQL：保存配置、运行状态和任务队列；
- Docker：实际执行 Pipeline Job。

常用开发验证命令：

```bash
go test ./...
go vet ./...
npm --prefix web test
npm --prefix web run lint
npm --prefix web run build
```

不安装 Go，也可以通过 Docker 运行后端测试（包括真实 PostgreSQL 和竞态检测）：

```bash
docker compose -p yuanci-tests -f deploy/compose.test.yml up --build --abort-on-container-exit --exit-code-from verify
docker compose -p yuanci-tests -f deploy/compose.test.yml down
```

这是独立测试环境，不使用 Quickstart 数据库。测试数据库是临时的，停止后
自动丢弃；不要把 `YUANCI_TEST_DATABASE_URL` 指向日常使用的数据库。
每批开发记录与实际验证结果见 [开发日志](development-log.md)。

## 9. 生产部署状态

`deploy/compose.production.yml` 和 `deploy/compose.runner.yml` 已提供控制面与
Runner 分机部署骨架，但当前版本会对缺失的身份认证安全能力采取 fail-closed
策略，而且 GHCR 发布镜像尚未交付。因此现阶段请使用 Quickstart 做开发和
体验，不要把 production Compose 当作可正式上线的版本。

正式部署前至少还需要完成 OAuth/RBAC、Runner mTLS、密钥管理、备份恢复、
GitHub App 安装认证和安全审计。
