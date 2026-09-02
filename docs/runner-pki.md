# Runner mTLS、PKI 与分机部署指南

本文面向 YuanCI 运维人员。Runner 只主动连接控制面，Server 不反向连接 Runner。
注册是唯一允许没有客户端证书的 gRPC 调用，并且必须提供短期、有限次数的 Token；
注册成功后，Work 与证书轮换全部要求数据库中仍有效的 Runner 客户端证书。

> 当前项目仍是 pre-alpha。以下机制和 Compose 已经过本地集成验证，但尚未完成
> 72 小时稳定性测试、灾备演练和第三方安全审计，不能据此宣称已适合生产。

## 1. 信任模型

- Runner 本地生成私钥；私钥从不发送给 Server。
- Server 使用在线中间 CA 签发 Runner 客户端证书。
- 根 CA 私钥只用于签发中间 CA，必须离线保存，不能留在 Server 主机。
- Server 使用 TLS 1.3；Runner 固定根 CA，并校验配置的 DNS 名或 IP SAN。
- Work 身份来自已验证证书的 URI SAN，不信任请求体中的 Runner ID。
- 一次性 Token 在数据库中仅保存 SHA-256 摘要，明文只存在于指定文件中。

Quickstart 为了一键启动，会把离线根密钥保留在本机 Docker PKI 数据卷中；它只
适用于可信的本机体验，不是生产 PKI 仪式。

## 2. 生产 PKI 初始化

在一台可信、临时且不运行 Server 的管理机上构建或安装 `yuancictl`。源码构建：

```bash
go build -o yuancictl ./cmd/yuancictl
```

创建一个全新的输出目录，并为 Runner 实际连接使用的每个 DNS 名或 IP 重复传入
`-server-name`。URL、端口、通配符、下划线和空名称都会被拒绝。

```bash
./yuancictl runner-pki init \
  -dir /secure/yuanci/runner-pki \
  -server-name ci.example.com \
  -server-name 10.10.0.20
```

命令拒绝覆盖已有目录，也不会打印私钥。输出结构如下：

```text
runner-pki/
├── offline-root/
│   ├── root-key.pem
│   └── root-cert.pem
└── server/
    ├── intermediate-key.pem
    ├── intermediate-cert.pem
    ├── root-cert.pem
    ├── server-key.pem
    ├── server-chain.pem
    └── manifest.json
```

通过另一条可信通道核对命令输出的根证书 SHA-256 指纹。把 `offline-root/` 加密
备份到离线介质并从 Server 主机删除；只把 `server/` 传到 Server。Linux 上私钥
应为 `0600`、公共文件 `0644`、目录 `0700`。Compose 中 Server 使用 UID/GID
`10001:10001`，因此在线目录及其所有父目录必须允许该用户穿越，在线文件可执行：

```bash
sudo chown -R 10001:10001 /secure/yuanci/runner-pki/server
sudo chmod 700 /secure/yuanci/runner-pki/server
sudo chmod 600 /secure/yuanci/runner-pki/server/*-key.pem
```

不要用 `chmod 777` 解决权限问题；Server 会拒绝组/其他用户可读的私钥。

## 3. 启动独立控制面

复制 `deploy/production.env.example` 到仓库外的受保护位置，填写 HTTPS 公网地址、
数据库密码、Master Key 路径和在线 `server/` 绝对路径，并固定镜像版本。控制面
Compose 只包含 Server 和 PostgreSQL，不包含 Runner，也不挂载 Docker Socket。

```bash
docker compose --env-file /secure/yuanci/production.env \
  -f deploy/compose.production.yml config
docker compose --env-file /secure/yuanci/production.env \
  -f deploy/compose.production.yml up -d
```

HTTP 8080 默认只绑定 `127.0.0.1`，应由反向代理提供 HTTPS。TCP 9443 是独立的
Runner gRPC 端口，只允许专用 Runner 网段访问；不要通过普通 HTTP 反向代理，
也不要向公网全开放。证书 SAN、`YUANCI_RUNNER_GRPC_SERVER_NAME` 和 Runner 解析
到的地址必须一致。

Server 需要以下六项，缺少任何一项都会拒绝启动：

```text
YUANCI_RUNNER_GRPC_ADDR=:9443
YUANCI_RUNNER_SERVER_CERT_FILE=/run/yuanci-pki/server-chain.pem
YUANCI_RUNNER_SERVER_KEY_FILE=/run/yuanci-pki/server-key.pem
YUANCI_RUNNER_CLIENT_CA_FILE=/run/yuanci-pki/root-cert.pem
YUANCI_RUNNER_ISSUER_CERT_FILE=/run/yuanci-pki/intermediate-cert.pem
YUANCI_RUNNER_ISSUER_KEY_FILE=/run/yuanci-pki/intermediate-key.pem
```

## 4. 签发一次性注册 Token

`000009_default_runner_pool` 迁移会创建 `standard` 普通构建池。设置仅本次命令使用
的 `YUANCI_DATABASE_URL`，在可信管理环境签发默认 10 分钟、一次使用的 Token：

```bash
export YUANCI_DATABASE_URL='postgres://yuanci:密码@数据库地址:5432/yuanci?sslmode=require'
./yuancictl runner-token issue \
  -pool standard \
  -file /secure/transfer/registration-token \
  -ttl 10m \
  -uses 1
unset YUANCI_DATABASE_URL
```

目标文件必须不存在，父目录必须已创建。命令不会把 Token 打到标准输出。通过
短期、加密且可审计的通道把文件传到 Runner 主机，不要粘贴到 Shell 历史、CI
变量、Issue 或聊天中。过期或使用过的 Token 不能重新读取或恢复，只能重新签发。

## 5. 部署独立 Runner

在专用 Linux Runner 主机上：

1. 复制 `deploy/runner.env.example` 到受保护位置。
2. 只复制根证书 `root-cert.pem`；不要复制根私钥、中间私钥或 Server 私钥。
3. 创建仅 UID/GID `10001:10001` 可写的 bootstrap 目录，将 Token 文件命名为
   `registration-token` 放入其中。
4. 用 `stat -c '%g' /var/run/docker.sock` 填写 `YUANCI_DOCKER_GID`。
5. 确认 `YUANCI_RUNNER_GRPC_SERVER_NAME` 与 Server 证书 SAN 完全一致。

```bash
sudo install -d -o 10001 -g 10001 -m 700 /secure/yuanci/runner-bootstrap
sudo install -o 10001 -g 10001 -m 600 \
  /secure/transfer/registration-token \
  /secure/yuanci/runner-bootstrap/registration-token
docker compose --env-file /secure/yuanci/runner.env \
  -f deploy/compose.runner.yml config
docker compose --env-file /secure/yuanci/runner.env \
  -f deploy/compose.runner.yml up -d
```

注册成功后日志会显示 Runner ID 和证书到期时间，但不会显示私钥或 Token；
bootstrap 中的 Token 会被 Runner 删除。Runner 身份保存在命名卷
`runner-state`，普通重启会复用身份，不需要新 Token。

Runner 容器以 UID 10001 运行，只额外加入 Docker Socket 的宿主机组。挂载 Docker
Socket 等价于把该 Runner 主机的 Docker 管理权限交给任务执行器，因此生产必须
使用专用主机，不能与控制面共机，也不能执行不可信公共 PR。

## 6. 自动轮换、停用与替换

Runner 会在客户端证书剩余约 6 小时时生成新私钥和 CSR，使用当前 mTLS 身份请求
轮换，并原子切换凭据。响应丢失时会复用同一个待处理 CSR，避免生成多个身份。
旧证书只有短暂宽限期；不要依赖手工复制证书完成轮换。

数据库层已经实现并审计 Runner 停用与单证书吊销，停用会立即使其 Work/轮换
认证失败；但 pre-alpha 尚未提供受支持的管理 UI/CLI。因此发生疑似泄露时，当前
可执行的安全隔离步骤是：先停止 Runner、在防火墙阻断其到 9443、保存审计证据，
再更换整套实例 PKI 并重新注册所有可信 Runner。缺少正式吊销入口本身就是生产
发布阻断项，不应通过手写 SQL 绕过。

正常替换一台 Runner 时，先停止旧容器并确认没有运行中任务，再在控制面签发新
Token，在新主机完成注册。不要复制旧 `runner-state` 卷；每台 Runner 应有独立
私钥和 Runner ID。确认新 Runner 正常领取任务后，保留旧主机以便审计，直到正式
停用入口交付。

## 7. 备份与恢复边界

必须分别备份并定期验证：

- PostgreSQL 数据库；
- YuanCI Master Key；
- 离线根 CA 目录（加密、离线、至少两份）；
- 在线 Server PKI 目录和部署配置；
- 日后启用的构件/日志对象存储。

不要备份或克隆一次性 Token。通常也不要用 Runner 身份卷做跨主机恢复；重建
Runner 时签发新 Token、生成新私钥。恢复时数据库、Master Key、在线 PKI 和应用
版本必须来自同一恢复点。当前尚未完成正式恢复演练，不能把“有备份文件”等同于
可恢复。

## 8. 排障

- `Runner PKI initialization failed`：检查在线目录父级能否由 UID 10001 穿越、
  文件是否为普通文件、私钥是否严格 `0600`，以及证书/私钥是否匹配。
- `registration denied`：Token 可能过期、已用、池不存在，或 Server 数据库与
  签发 Token 时不是同一实例。删除失败 Runner 的空状态后重新签发，不要复用 Token。
- TLS Server name 错误：连接地址可以是 IP 或 DNS，但 `GRPC_SERVER_NAME` 必须是
  证书初始化时明确加入的 SAN。
- Runner 身份文件权限过宽、损坏或密钥不匹配：Runner 会 fail closed。先隔离
  主机并调查；不要放宽权限或拼接文件，按“替换”流程创建新身份。
- Docker 权限被拒绝：核对 Socket 数字 GID 与 `YUANCI_DOCKER_GID`，重建 Runner
  容器以应用 group 配置；不要改为 privileged 或 root。
- 连接反复断开：检查 9443 的 TCP 防火墙、DNS、系统时间和 Server/Runner 日志。
  Runner 会指数退避重连；超过最后确认的任务租约期限会取消本地执行。

任何排障输出都不应包含 Token、私钥、Master Key、完整数据库 URL 或任务 Secret。

## 9. 旧共享 Token 迁移

新版本拒绝 `YUANCI_RUNNER_SHARED_TOKEN`、`YUANCI_RUNNER_TOKEN` 和
`YUANCI_SERVER_URL`。这是有意的 pre-alpha 不兼容变更，没有原地凭据转换。升级前
同时备份数据库和配置；升级 Server 并启用 PKI后，为每台 Runner 签发一次性 Token
并重新注册。需要回滚时必须整体恢复升级前数据库、镜像和配置，不能让旧 Server
读取已执行新迁移的数据库。
