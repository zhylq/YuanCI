# Development roadmap

## Current delivery status — 2026-08-28

This is still pre-alpha, not a finished CI/CD product. See the
[development log](development-log.md) for executed tests and known limitations.

| Area | Implemented and locally checked | Remaining exit gate |
| --- | --- | --- |
| Engineering workflow | Incremental commits, CI definition, isolated DB/race tests | Publish workflow, hosted checks and branch protection |
| Transaction queue | Unique claims, serialized DAG completion, cancellation result, migration/reopen tests | Heartbeats, lease recovery, fault injection and 72-hour soak |
| Authorization | Scoped policy/sessions/audit, GitHub OAuth/PKCE, explicit bootstrap/linking, one-use setup code, encrypted candidate settings and verified activation | Real GitHub sandbox login, membership/audit lifecycle, last-admin/recovery and deployment enforcement |
| Secrets | Envelope encryption, AAD binding, corrupt-input/fuzz tests, file-supplied master key and encrypted login credentials | Job-secret storage API, key rotation, scoped release and streaming redaction |
| Docker | Source-context exclusions, loopback Quickstart, separate verification and authenticated-preview profiles | Release digest locks, signed multi-architecture images and recovery drills |
| Console | Evaluation dashboard, YAML validation/plan preview, login/logout, account landing, setup/settings wizard and provider guides | Project selection, repositories, run detail/logs, full visual editor and administration |
| SCM | Existing GitHub REST adapter and signed event normalization tests | End-to-end GitHub App flow plus GitLab, Gitea and Gitee |
| CD | Design only | Environments, approvals, SSH/Compose execution, health checks and rollback |

Next implementation order: project discovery/selection and scoped repository UI;
Runner identity/lease recovery; GitHub end-to-end CI; complete
console; remaining SCM/CI capabilities; protected CD; release qualification.
Do not remove the insecure-evaluation gate merely because the policy tests pass.

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
