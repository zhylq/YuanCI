# YuanCI v1 atomic task plan

This catalog implements the approved
[token-efficient task design](2026-09-03-token-efficient-task-design.md). Tasks
are executed in listed order unless their dependency column permits otherwise.

## How to trigger one task

Start a new Codex task in the YuanCI project and send:

```text
继续开发 YuanCI。执行 docs/plans/2026-09-03-v1-atomic-task-plan.md 中的任务 <TASK-ID>。
严格遵守 docs/plans/2026-09-03-token-efficient-task-design.md：只完成该原子任务，
先定点测试，只有阶段门禁才跑全量测试；更新开发记录，提交并推送 main；最后报告提交、
测试、远程 CI 和下一个任务 ID。禁止扫描 internal/webui/dist、node_modules、缓存和完整成功日志。
```

Before triggering, replace `<TASK-ID>`. Do not include prior chat history. If a
task is blocked only by operator-owned access, complete its automation/checklist
and report the exact manual action without fabricating acceptance.

## A. GitHub-only CI Alpha critical path

| ID | Model | Depends | Atomic outcome and exit test |
| --- | --- | --- | --- |
| GH-01 | S | `9e9b7ad` | Add a single-delivery orchestrator that loads automation policy, rejects disabled/fork/non-matching events, fetches immutable config, compiles it, and commits a Run. Unit tests cover every classification. |
| GH-02 | S | GH-01 | Add safe transient/permanent error classification, bounded backoff, retry/dead-letter finalization, and redacted summaries. Deterministic clock tests pass. |
| GH-03 | T | GH-02 | Add the bounded worker loop, lease recovery tick, graceful shutdown, and server lifecycle wiring. Shutdown and duplicate-worker tests pass. |
| GH-04 | S | GH-03 | Persist a visible failed Run for an enabled project whose config is missing or invalid, without creating executable Jobs. Atomic rollback tests pass. |
| GH-05 | S | GH-04 | Add authenticated automation validation/enable API with revision checks and immutable config proof. RBAC, CSRF, conflict, and failure tests pass. |
| SRC-01 | S | GH-05 | Add repository-scoped `contents:read` checkout credential issuance to the GitHub App service; validate repository binding and lifetime and clear key buffers. Unit tests pass. |
| SRC-02 | S | SRC-01 | Add a non-persistent assignment credential issuer to Runner gRPC; on issuance failure safely release or fail the lease without exposing the token. Store and server tests pass. |
| SRC-03 | S | SRC-02 | Upgrade the shipped Runner to protocol v2, validate source/credential pairing and expiry, copy only bounded token bytes, and clear protobuf buffers. Protocol downgrade/leak tests pass. |
| SRC-04 | S | SRC-03 | Add strict source descriptor validation: trusted HTTPS URL, no user info/query/fragment/local address ambiguity, GitHub repository identity, and exact 40-hex SHA. Fuzz/unit tests pass. |
| SRC-05 | S | SRC-04 | Implement the one-shot Git checkout helper using token input on stdin; disable hooks, redirects, unsafe protocols, LFS, submodules, and credential persistence. Command-construction tests prove tokens never enter argv/env. |
| SRC-06 | S | SRC-05 | Integrate checkout with the Docker workspace before user steps and verify `HEAD` equals the assigned SHA. Success, mismatch, cancellation, and cleanup integration tests pass. |
| SRC-07 | S | SRC-06 | Add lease-loss and process-crash cleanup tests for helper/container/network/volume/token buffers, plus a credential leak scan. This is the secure-checkout phase gate. |
| STAT-01 | S | GH-04 | Add provider-neutral commit-status outbox migration, state model, constraints, claim lease, and recovery repository. Migration and PostgreSQL concurrency tests pass. |
| STAT-02 | S | STAT-01 | Enqueue deterministic `pending` with Run creation and one final status with terminal Run transition in the same transactions. Replay and rollback tests pass. |
| STAT-03 | T | STAT-02,SRC-01 | Deliver GitHub Commit Status using a fresh repository token with bounded request/response handling and state mapping. Provider tests pass. |
| STAT-04 | S | STAT-03 | Add status worker retry, rate-limit scheduling, expiry recovery, dead letter, safe admin replay service, and shutdown wiring. Crash-window tests pass. |
| LOG-01 | S | SRC-03 | Define bounded ordered log chunks, PostgreSQL metadata/storage contract, sequence idempotency, size limits, and retention fields. Migration and concurrency tests pass. |
| LOG-02 | S | LOG-01,SRC-06 | Stream stdout/stderr from Runner with backpressure, reconnect-safe sequence handling, truncation, and cancellation. Transport tests pass. |
| LOG-03 | S | LOG-02 | Add streaming secret redaction before transport and disk, including split-boundary matches and bounded memory. Property/fuzz and leak tests pass. |
| RUN-01 | S | LOG-02 | Add authenticated Run cancellation API through server, lease cancellation, Docker termination, and terminal convergence. RBAC/race tests pass. |
| RUN-02 | S | RUN-01 | Add full Run rerun and failed-Job rerun with immutable source/config identity and new idempotency keys. DAG correctness tests pass. |
| UIA-01 | T | GH-05 | Add GitHub setup health, webhook instructions, secret replacement status, and project automation controls. API and focused React tests pass. |
| UIA-02 | T | LOG-02,RUN-02 | Add Run source/status, stage/job/step detail, live log view, cancel, full rerun, and failed-Job rerun. Accessibility and responsive tests pass. |
| E2E-GH-01 | S | SRC-07,STAT-04,LOG-03,UIA-02 | Build deterministic fake-GitHub E2E: signed webhook → immutable config → private checkout → execution/log → final status. Full isolated suite passes once. |
| E2E-GH-02 | T | E2E-GH-01 | Prepare and execute the operator-owned real GitHub App sandbox checklist. Record evidence; never mark complete without real credentials/results. GitHub Alpha gate. |

## B. Complete CI execution

| ID | Model | Depends | Atomic outcome and exit test |
| --- | --- | --- | --- |
| CI-01 | T | E2E-GH-01 | Implement service-container lifecycle on the isolated job network with aliases, readiness, cancellation, and cleanup. Docker integration tests pass. |
| CI-02 | S | CI-01 | Implement runtime Job retry with attempt records, policy limits, backoff, and terminal DAG semantics. Transaction/race tests pass. |
| CI-03 | S | CI-02 | Expand matrix definitions deterministically with bounded cardinality and immutable variant identity. Compiler/property tests pass. |
| CI-04 | S | CI-03 | Persist and schedule matrix Jobs with correct dependencies, retry identity, and UI labels. PostgreSQL tests pass. |
| CI-05 | S | CI-04 | Implement concurrency-group locking, queue ordering, lease recovery, and terminal release. Contention tests pass. |
| CI-06 | S | CI-05 | Implement cancel-older-commits policy without canceling tags, deployments, or unrelated refs. Race tests pass. |
| CI-07 | T | CI-06 | Complete event, branch, ref, and path trigger evaluation plus skip-marker behavior using bounded changed-file input. Contract tests pass. |
| CI-08 | S | CI-07 | Add persisted schedules, timezone-safe next-run calculation, leader-safe claiming, and manual pause. Clock/DST tests pass. |
| CI-09 | T | CI-08 | Harden manual and API triggers with repository RBAC, idempotency, immutable ref resolution, rate limits, and audit. HTTP tests pass. |
| CACHE-01 | S | CI-04 | Define scoped cache metadata/API, immutable keys, restore prefixes, quotas, expiry, and archive safety. Store and path-fuzz tests pass. |
| CACHE-02 | S | CACHE-01 | Add Runner cache download/upload with checksum, traversal defense, cancellation, and cleanup. Integration tests pass. |
| ART-01 | S | LOG-01 | Define artifact metadata/storage API, quotas, checksums, retention, authorization, and safe archive rules. Store/fuzz tests pass. |
| ART-02 | S | ART-01 | Add Runner artifact upload/download with streaming limits, cancellation, retry, and no credential leakage. Integration tests pass. |
| ART-03 | T | ART-02 | Add authorized artifact list/download/delete and cache status UI. HTTP/React accessibility tests pass. Complete-CI execution gate. |

## C. Job secrets

| ID | Model | Depends | Atomic outcome and exit test |
| --- | --- | --- | --- |
| SEC-01 | S | E2E-GH-01 | Add instance/organization/project/environment Secret schema and write-only CRUD contracts with envelope encryption and AAD. Migration/API tests prove no plaintext reads. |
| SEC-02 | S | SEC-01 | Implement Secret authorization, protected classification, name collision rules, audit, and environment inheritance. RBAC tests pass. |
| SEC-03 | S | SEC-02 | Enforce fork/PR/protected-environment release policy and issue short-lived Job-bound Secret material only after assignment. Policy tests pass. |
| SEC-04 | S | SEC-03,LOG-03 | Deliver Secrets over mTLS without plan/argv persistence, inject only into authorized steps, redact logs/errors, and clear buffers. Leak tests pass. |
| SEC-05 | S | SEC-04 | Add master/data-key rotation with resumable progress, dual-read transition, rollback-safe metadata, CLI, and audit. Recovery tests pass. Secret gate. |

## D. Remaining SCM providers

Repeat the shared contract; provider-specific code must not bypass normalized
events, immutable SHA lookup, checkout policy, or the status outbox.

| ID | Model | Depends | Atomic outcome and exit test |
| --- | --- | --- | --- |
| GL-01 | T | E2E-GH-01 | GitLab OAuth/instance configuration, identity, repository discovery/import, and token lifecycle pass the SCM contract. |
| GL-02 | S | GL-01 | GitLab signed/token webhook normalization, idempotency, fork policy, and immutable file lookup pass provider tests. |
| GL-03 | T | GL-02 | GitLab private checkout credential and Commit Status delivery pass the shared execution/outbox tests. |
| GL-04 | S | GL-03 | Fake and operator-owned real GitLab E2E evidence passes. |
| GT-01 | T | E2E-GH-01 | Gitea instance/OAuth, identity, repository discovery/import, and token lifecycle pass the SCM contract. |
| GT-02 | S | GT-01 | Gitea webhook normalization/idempotency/fork policy and immutable file lookup pass provider tests. |
| GT-03 | T | GT-02 | Gitea private checkout credential and commit-status delivery pass shared tests. |
| GT-04 | S | GT-03 | Fake and operator-owned real Gitea E2E evidence passes. |
| GE-01 | T | E2E-GH-01 | Gitee OAuth, identity, repository discovery/import, rate handling, and token lifecycle pass the SCM contract. |
| GE-02 | S | GE-01 | Gitee webhook normalization/idempotency/fork policy and immutable file lookup pass provider tests. |
| GE-03 | T | GE-02 | Gitee private checkout credential and status delivery pass shared tests. |
| GE-04 | S | GE-03 | Fake and operator-owned real Gitee E2E evidence passes. Four-SCM gate. |

## E. Console and administration

| ID | Model | Depends | Atomic outcome and exit test |
| --- | --- | --- | --- |
| ADM-01 | T | UIA-02 | Add Runner/pool inventory, capacity, labels, health, version, and active leases with scoped admin APIs/UI. |
| ADM-02 | S | ADM-01 | Add supported Runner disable, certificate revoke, replacement-token workflow, audit, and active-job policy. Security tests pass. |
| ADM-03 | S | GH-05 | Add organization/project membership lifecycle and role management with last-admin protection. RBAC/concurrency tests pass. |
| ADM-04 | T | ADM-03 | Add paginated audit viewer/export with filters, bounded output, authorization, and retention controls. |
| ADM-05 | S | ADM-03 | Add disabled-by-default emergency local admin, Argon2id, TOTP, CLI-only recovery, lockout, and audit. Security tests pass. |
| EDIT-01 | S | CI-07 | Define one canonical editor AST/API round trip so YAML and visual mode preserve equivalent Pipeline v1 semantics. Golden tests pass. |
| EDIT-02 | T | EDIT-01 | Build accessible Stage/Job/Step form editing and validation with responsive layouts. React tests pass. |
| EDIT-03 | T | EDIT-02 | Add DAG preview, matrix/concurrency/trigger controls, and YAML advanced mode conflict handling. UI tests pass. |
| EDIT-04 | T | EDIT-03 | Add versioned templates/new-project wizard without hidden privileged behavior. Snapshot/accessibility tests pass. |
| EDIT-05 | S | EDIT-04 | Commit edited config to a new branch and open PR/MR through the SCM contract with CSRF/RBAC/audit. Provider tests pass. Console gate. |

## F. Protected Docker Compose CD

| ID | Model | Depends | Atomic outcome and exit test |
| --- | --- | --- | --- |
| CD-01 | S | SEC-02 | Add Environment, Deployment, Approval, snapshot, and transition schemas with transaction-checked state machines. Migration/property tests pass. |
| CD-02 | S | CD-01,SEC-04 | Add protected environments and environment Secret resolution with deployer/approver separation. RBAC tests pass. |
| CD-03 | S | CD-02 | Add approval requests, expiry, rejection, immutable release identity, and self-approval prohibition. Concurrency tests pass. |
| CD-04 | S | CD-03 | Add per-environment deployment locks, queueing, lease recovery, and duplicate-release idempotency. Fault tests pass. |
| CD-05 | S | CD-04 | Add deployment-only Runner protocol/capability and ensure build pools can never receive deployment credentials. Protocol tests pass. |
| CD-06 | S | CD-05 | Implement hardened SSH host-key pinning, command/data separation, timeouts, cancellation, and Secret-safe errors. Integration tests pass. |
| CD-07 | S | CD-06 | Implement immutable Compose bundle transfer, validation, image digest enforcement, and previous-success snapshot capture. |
| CD-08 | T | CD-07 | Implement bounded HTTP, TCP, and command health checks with stabilization windows and safe diagnostics. |
| CD-09 | S | CD-08 | Implement automatic failure rollback and audited manual rollback under the same environment lock. Failure-injection tests pass. |
| CD-10 | T | CD-09 | Add environment, approval, deployment history, health, and rollback UI with accessibility tests. CD gate. |

## G. Operations and v1 release qualification

| ID | Model | Depends | Atomic outcome and exit test |
| --- | --- | --- | --- |
| OPS-01 | T | E2E-GH-01 | Add structured operational metrics and OpenTelemetry spans without high-cardinality or Secret fields; dashboards/alerts documented. |
| OPS-02 | S | ART-02,LOG-02 | Add S3-compatible logs/artifacts backend with streaming checksums, scoped credentials, retry, and filesystem fallback. Integration tests pass. |
| OPS-03 | S | SEC-05,CD-09 | Add consistent PostgreSQL plus object-data backup and restore scripts with version manifest and checksum verification. Fresh-instance drill passes. |
| OPS-04 | S | OPS-03 | Add migration lock, previous-version upgrade test, compatibility check, and downgrade refusal/restore instructions. |
| OPS-05 | S | ADM-02 | Add forced network-partition, server restart, database interruption, Runner loss, disk-full, and duplicate-webhook fault suite. |
| OPS-06 | S | OPS-05 | Run and tune the 20-concurrent-job capacity test; publish measured CPU, memory, queue latency, and cleanup results. |
| OPS-07 | T | OPS-04 | Build digest-locked amd64/arm64 release images and publish workflow with provenance, immutable tags, and rollback metadata. |
| OPS-08 | S | OPS-07 | Generate SBOMs, vulnerability policy, image signatures, verification instructions, and pinned base-image update process. |
| OPS-09 | T | OPS-03,OPS-08 | Complete reverse proxy, TLS, production Compose, backup, recovery, upgrade, troubleshooting, and supported-version documentation. Fresh-host rehearsal passes. |
| QUAL-01 | S | GL-04,GT-04,GE-04,CD-10,OPS-09 | Run the shared four-provider core E2E suite and credential leak scan from clean environments. |
| QUAL-02 | S | QUAL-01 | Execute cross-host control-plane/build/deploy Runner rehearsal and forced-partition recovery; record evidence. |
| QUAL-03 | S | QUAL-02,OPS-06 | Execute the operator-owned 72-hour, 20-concurrency soak with no lost/duplicate jobs or deployments. Record immutable report. |
| QUAL-04 | S | QUAL-03 | Complete third-party security review, track findings, and resolve every critical/high issue with regression tests. |
| QUAL-05 | T | QUAL-04 | Add release notes, security/support policy, branch protection, signed v1.0 tag, hosted checks, and final installation verification. v1.0 gate. |

## Phase gates and full verification

Ordinary tasks run focused tests only. Full repository verification is reserved
for `SRC-07`, `E2E-GH-01`, `ART-03`, `SEC-05`, `GE-04`, `EDIT-05`, `CD-10`,
`OPS-09`, and `QUAL-01` through `QUAL-05`. A phase gate includes Go race tests,
`go vet`, frontend tests/build when applicable, Compose validation, and only the
deployment-image builds relevant to that gate.

The next task is `GH-01`.
