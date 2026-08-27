# Development roadmap

## Milestone 0 — architecture baseline

- ADRs, threat model, RBAC matrix, state machines, OpenAPI and Pipeline v1 schema
- Buildable server, CLI, runner, console, database and Compose skeletons

## Milestone 1 — CI vertical slice

- GitHub repository connection and authenticated webhook ingestion
- Pipeline compilation, transactional scheduling, Docker execution, live logs and status reporting
- Run list, run detail, cancellation and retry in the console

Progress:

- GitHub REST adapter: authenticated identity, repository discovery, file reads,
  webhook creation, commit statuses and pipeline pull requests
- Signed GitHub push, tag and pull-request payloads normalized to the shared
  SCM event model
- Remaining: encrypted GitHub App installation storage, HTTP ingestion with
  delivery idempotency, repository sync and automatic run/status orchestration

## Milestone 2 — complete CI

- GitLab, Gitea and Gitee adapters under the shared SCM contract
- Service containers, cache, artifacts, matrices, concurrency groups and scheduled triggers
- Runner pools, capabilities, resource policies and administrative UI

## Milestone 3 — Docker Compose CD

- Environments, protected secrets, approvals and deployment locks
- Dedicated SSH/Compose deployment runner, health checks, snapshots and rollback

## Milestone 4 — v1 hardening

- Backup/restore and upgrade tests, failure injection, 72-hour soak test and capacity test
- SBOM, signed images, third-party security review and operator documentation
