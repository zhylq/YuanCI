# YuanCI

[中文快速入门](docs/getting-started.zh-CN.md)

YuanCI is a lightweight, self-hosted CI/CD control plane and runner for small and medium engineering teams. The project is being built around a Go modular monolith, isolated Go runners, PostgreSQL, and a React/TypeScript interface.

> **Project status:** pre-alpha. The current milestone provides the architecture baseline, pipeline v1 compiler, run state machine, PostgreSQL queue, certificate-bound Runner service and client with lease-loss cleanup, GitHub SCM adapter, web console shell, and mTLS Docker Compose packaging. It has not completed webhook-to-build orchestration, log/secret delivery, the remaining SCM providers, CD, a 72-hour soak, or an external security review. It is not a production release.

Database-backed startup requires an explicit mode: isolated insecure evaluation,
or the new [GitHub authenticated preview](docs/auth-preview.zh-CN.md). The latter
implements login/bootstrap and protected browser APIs but has no legacy Runner
routes. The [project browser](docs/project-browser.zh-CN.md) now provides scoped
project selection, repository metadata and paginated run summaries. Managed mode
also provides [GitHub App installation discovery and repository import](docs/github-import.zh-CN.md),
with encrypted credentials and transactionally checked authorization. Webhooks,
private checkout and automatic builds are still pending. The [managed setup wizard](docs/managed-setup.zh-CN.md)
provides one-time setup codes, encrypted app settings, login/logout and verified
configuration activation. Full identity administration and production deployment
qualification remain release blockers.

## Repository layout

```text
cmd/                    Go entry points
internal/               Control-plane and runner packages
api/                    OpenAPI and protobuf contracts
db/migrations/          PostgreSQL schema migrations
web/                    React/TypeScript console
deploy/                 Docker Compose deployment profiles
docs/                   Architecture, security, and development plans
```

## Quick development commands

Prerequisites: Go, Node.js 22+, npm, Docker 28+, and Docker Compose.

```bash
npm --prefix web install
npm --prefix web run build
go test ./...
docker compose -f deploy/compose.quickstart.yml up --build
```

Quickstart exposes the console and API at `http://localhost:8080`. Supply a
long random `YUANCI_POSTGRES_PASSWORD`; one-shot PKI and Runner registration are
created inside persistent Docker volumes. The plaintext registration token is
deleted after enrollment. Do not commit a populated `.env` file.

The quickstart profile mounts the Docker socket into the runner. It is intended only for local evaluation and trusted internal jobs. Production deployments must use `deploy/compose.production.yml` for the control plane and `deploy/compose.runner.yml` on a dedicated runner host.

If you only want to run YuanCI, you do not need Go or Node.js. Follow the
[Chinese Docker quickstart](docs/getting-started.zh-CN.md), which includes
configuration, startup, logs, upgrades and troubleshooting.

## Pipeline configuration

YuanCI reads `.yuanci.yml` from the repository root. See [the example](examples/pipelines/basic.yuanci.yml) and [the v1 schema](api/pipeline.schema.json).

## Documentation

- [Approved system design](docs/plans/2026-08-26-yuanci-design.md)
- [Architecture decision record](docs/adr/0001-control-plane-and-runner.md)
- [Threat model](docs/security/threat-model.md)
- [Development roadmap](docs/roadmap.md)
- [Development workflow and test commands](CONTRIBUTING.md)
- [Completed batch evidence and limitations](docs/development-log.md)
- [Authorization policy and protected handler status](docs/security/authorization.md)
- [Browser session integration and remaining activation gates](docs/security/sessions.md)
- [GitHub login preview deployment (Chinese, no host Go installation needed)](docs/auth-preview.zh-CN.md)
- [Managed Git-platform settings wizard and Docker deployment](docs/managed-setup.zh-CN.md)
- [GitHub App configuration, installation and repository import](docs/github-import.zh-CN.md)
- [Runner mTLS PKI and split deployment operations](docs/runner-pki.md)

## License

Apache-2.0. See [LICENSE](LICENSE).
