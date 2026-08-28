# YuanCI

[中文快速入门](docs/getting-started.zh-CN.md)

YuanCI is a lightweight, self-hosted CI/CD control plane and runner for small and medium engineering teams. The project is being built around a Go modular monolith, isolated Go runners, PostgreSQL, and a React/TypeScript interface.

> **Project status:** early development. The current milestone provides the architecture baseline, pipeline v1 compiler, run state machine, database schema, HTTP API foundation, runner protocol definition, GitHub SCM adapter, web console shell, and Docker Compose packaging. It is not yet suitable for production workloads.

The milestone-0 API is intentionally disabled for database-backed deployments unless the operator explicitly enables an insecure evaluation flag. OAuth/RBAC and runner mTLS are release blockers, not optional hardening.

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

Quickstart exposes the console and API at `http://localhost:8080`. Supply
`YUANCI_POSTGRES_PASSWORD` and `YUANCI_RUNNER_SHARED_TOKEN` as long random
values in the process environment; do not commit a populated `.env` file.

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
- [Authorization policy matrix (not yet API enforcement)](docs/security/authorization.md)

## License

Apache-2.0. See [LICENSE](LICENSE).
