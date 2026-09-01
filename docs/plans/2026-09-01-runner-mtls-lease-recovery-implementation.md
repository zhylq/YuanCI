# Runner mTLS identity and lease recovery — implementation plan

Design source: `2026-09-01-runner-mtls-lease-recovery-design.md`.

This plan is intentionally incremental. Every numbered batch must leave tests
green and receive its own local commit. Do not expose a partially authenticated
Runner service, do not use real production credentials, and do not modify the
currently running Quickstart while developing.

## Batch 1 — freeze protocol and generation workflow

Files:

- update `api/runner/v1/runner.proto`;
- add `buf.yaml`, `buf.gen.yaml` and pinned generation instructions/scripts;
- add committed generated files under `gen/runner/v1/`;
- update `go.mod`, `go.sum`, `Makefile` and `CONTRIBUTING.md`.

Work:

1. Remove every Server-generated private-key field. Registration and rotation
   requests contain bounded CSR PEM/DER; responses contain only certificate and
   CA chains, identity and deadlines.
2. Remove trusted body Runner IDs from authenticated messages. Add protocol
   version, capabilities, active-lease tokens, acknowledgement/result reason
   enums and explicit rotation request identity.
3. Define message-size/count constraints in comments and enforce them later in
   the service. Keep field numbers stable where semantics are unchanged; reserve
   removed private-key field numbers.
4. Pin Protobuf/gRPC generator versions. Regeneration must be deterministic and
   generated Go must be committed so operators do not need protoc.

Tests/checks:

- regenerate twice and require a clean Git diff;
- `go test ./api/... ./gen/...` and `go vet ./api/... ./gen/...`;
- descriptor/contract test rejects private-key response fields and body identity.

Commit: `feat: define certificate-bound Runner protocol`.

## Batch 2 — additive identity and recovery migration

Files:

- add `db/migrations/000007_runner_identity_recovery.up.sql`;
- update migration-count assertions in PostgreSQL tests;
- add migration/reopen cases in
  `internal/store/postgres/store_integration_test.go`.

Work:

1. Add `runner_registration_tokens` with digest, Pool, expiry, maximum/used count,
   creator and consumption metadata.
2. Add `runner_certificates` with unique serial, Runner, CSR/public-key
   fingerprint, state, certificate chain, validity, retirement and revocation.
3. Extend `runners` with validated capabilities, version, heartbeat and disable
   reason. Extend `jobs` with accepted/renewed timestamps and failure reason.
4. Add constraints/indexes for expiry scanning, serial lookup, one pending
   rotation and bounded states.
5. Transactionally convert legacy assigned Jobs to queued and legacy running
   Jobs to failed with an upgrade reason, finalize affected Runs consistently,
   and clear old lease material. Preserve terminal Jobs and all user/project data.

Tests/checks:

- migrate a fixture containing queued, assigned, running and terminal Jobs;
- assert no old lease survives and graph/Run outcomes are explicit;
- reopen database and verify migration idempotency/data preservation;
- verify invalid state/fingerprint/usage rows are rejected by constraints.

Commit: `feat: add Runner identity and recovery schema`.

## Batch 3 — PKI library and offline initialization CLI

Files:

- add `internal/runnerauth/pki.go`, `pki_test.go` and fuzz tests;
- add `cmd/yuancictl/runner_pki.go` and tests;
- extend `cmd/yuancictl/main.go` command routing.

Work:

1. Implement root/intermediate/Server certificate creation using Go standard
   crypto. Validate Server DNS/IP SAN input, lifetimes, BasicConstraints, KU/EKU,
   serial entropy and supported algorithms.
2. Write files with exclusive creation, 0600 private-key permissions, directory
   boundary checks, fsync and atomic rename. Never overwrite an existing PKI.
3. Keep root key separate from the Server bundle; provide a manifest with public
   fingerprints and expiry only.
4. Implement CSR parsing/validation and client certificate signing. Bind Runner
   UUID in one URI SAN and client-auth EKU. Reject extra SANs/extensions,
   unsupported/weak keys, trailing PEM data and oversized input.

Tests/checks:

- table tests for every certificate field and permission;
- invalid path/existing target/no partial files on failure;
- malformed, multi-block, weak-key, wrong-SAN and oversized CSR fuzz/property tests;
- verify chains with `x509.Verify` for correct and wrong uses/names.

Commit: `feat: add Runner PKI initialization and CSR validation`.

## Batch 4 — transactional enrollment, rotation and revocation store

Files:

- add `internal/runnerauth/service.go`, ports/types and unit tests;
- add `internal/store/postgres/runner_identity.go` and integration tests;
- add `cmd/yuancictl/runner_token.go` and tests.

Work:

1. Implement token issue using 32 random bytes and SHA-256 persistence. CLI can
   write the one-time plaintext to an exclusive output file but never re-read it
   from the database or print it by default.
2. Consume tokens under a row lock. Bind Pool and requested Runner metadata;
   atomically insert Runner, certificate and audit. A certificate from a rolled
   back transaction remains unusable.
3. Authenticate certificate serial + URI SAN + active Runner from database after
   TLS verification. Return one generic denial for unknown/revoked/disabled IDs.
4. Rotate idempotently by old serial + CSR fingerprint. Insert one new active
   certificate and retire the old one for at most 15 minutes. Reject different
   concurrent CSR attempts.
5. Implement disable/revoke store operations without a public UI; CLI/admin API
   exposure may remain a later batch, but core operations and audit must exist.

Tests/checks:

- concurrent one-use token consumption has exactly one success;
- expired/wrong-Pool token and spoofed Runner identity fail;
- audit failure rolls back Runner and certificate;
- lost rotation response returns the same certificate for the same CSR;
- old certificate works only in grace, then fails; disabled/revoked always fail;
- stored/serialized/logged data never contains token or CA/Runner private keys.

Commit: `feat: persist and audit Runner certificate identities`.

## Batch 5 — gRPC Server and certificate interceptor

Files:

- add `internal/runnergrpc/server.go`, `auth.go` and tests;
- update `cmd/yuanci-server/main.go`;
- extend `internal/config/config.go` and configuration tests;
- update `Dockerfile` only as required for certificate file access.

Work:

1. Load Server TLS and intermediate signing files with regular-file, size and
   permission checks. Configure TLS 1.3 where deployment compatibility permits,
   trusted client roots and no insecure fallback.
2. Start a dedicated configurable gRPC listener. Registration accepts a
   Server-authenticated TLS connection without client cert and is protected by
   token, request limits, concurrency limits and deadline.
3. Require and authenticate mTLS for Work/Rotate with a unary/stream interceptor.
   Put only verified immutable Runner identity in context.
4. Implement enrollment and rotation handlers. Configure maximum receive/send
   sizes, keepalive bounds and graceful shutdown; safe gRPC status/reason only.
5. Do not mount any HTTP Bearer Runner route in the new mode.

Tests/checks:

- `bufconn` business tests plus real loopback TLS handshake tests;
- wrong CA/Server name, absent cert, forged SAN, unknown serial, wrong EKU,
  expired/retired/revoked/disabled cert and oversized messages;
- body Runner ID cannot select another Runner;
- shutdown closes streams without goroutine leaks or panic.

Commit: `feat: serve certificate-authenticated Runner gRPC`.

## Batch 6 — certificate-bound scheduling and lease renewal

Files:

- extend `internal/run/store.go` with Runner-bound assignment/renewal types;
- update `internal/run/state.go`, `state_test.go`, contract tests and memory store;
- add `internal/store/postgres/runner_jobs.go` and integration tests;
- adapt/remove legacy Runner mutations from `internal/store/postgres/store.go`.

Work:

1. Match Pool, validated labels, OS, architecture, executor, free disk and
   remaining capacity. Standard Jobs cannot fall through to privileged/deployment
   pools.
2. Claim with `runner_id`, token digest and 30-second deadline in one transaction.
   Mark receipt separately; accept start only for matching Runner/token/deadline.
3. Renew active Jobs in a bounded heartbeat transaction. Return authoritative
   renewed deadlines and cancel decisions per Job. Update last heartbeat and
   capabilities only after all validation.
4. Make start/receipt/heartbeat retry semantics explicitly idempotent where safe.
   Reject stale tokens, wrong Runner, terminal Jobs and deadline races.
5. Retain parent-Run-before-Job lock ordering for all graph changes.

Tests/checks:

- shared contract suite for memory and PostgreSQL stores;
- Pool/label/platform/executor/disk/capacity matching matrix;
- concurrent capacity claims never oversubscribe;
- wrong Runner/token, duplicate heartbeat, deadline boundary and session wait;
- no unscoped claim and no privileged fallback.

Commit: `feat: bind scheduling and renewal to Runner identity`.

## Batch 7 — reconciliation and deterministic loss handling

Files:

- add `internal/run/recovery.go` and tests;
- add `internal/store/postgres/runner_recovery.go` and integration tests;
- start/stop reconciler in `cmd/yuanci-server/main.go`.

Work:

1. Scan at most 100 expired Jobs every five seconds using `SKIP LOCKED` and the
   established parent-Run-first lock order.
2. Requeue expired assigned/not-started Jobs after clearing all lease ownership.
3. Fail expired running Jobs as `runner_lost`, clear lease, skip downstream and
   finalize Run failed in the same transaction.
4. Recheck `clock_timestamp()` after lock waits. Multiple reconcilers and Server
   restart must converge without duplicate effects.
5. Emit bounded metrics and structured reason-only logs; do not log lease tokens.

Tests/checks:

- exact assigned/running outcomes and terminal Job preservation;
- expiration while blocked on Job/Run lock;
- simultaneous complete versus recovery, two reconcilers and Server restart;
- audit/DB failure leaves the whole graph unchanged;
- late start/complete after recovery always rejected.

Commit: `feat: recover expired Runner leases deterministically`.

## Batch 8 — Runner credential store and resilient Work client

Files:

- replace `internal/runner/client.go` with enrollment/credential/stream modules;
- add `internal/runner/credentials.go`, `work.go` and focused tests;
- update `cmd/yuanci-runner/main.go`;
- extend `internal/config/config.go` Runner configuration/tests.

Work:

1. Generate Runner key locally, enroll with root trust + one-time token, and
   atomically persist credentials in a dedicated state directory. Remove a
   writable token file after successful registration; a consumed immutable Docker
   secret remains harmless but should be rotated/removed operationally.
2. Reuse credentials after restart, verify local key/certificate match, validate
   chain/identity/expiry before dialing, and refuse corrupt/insecure-permission files.
3. Maintain one bounded Work stream with jittered exponential reconnect. Send
   five-second heartbeats and reconcile local active Jobs with Server decisions.
4. Rotate at six hours remaining using a fresh local key/CSR and atomic switch;
   retry the same pending CSR after response loss.
5. Remove HTTP polling/shared Bearer client code and obsolete Runner config.

Tests/checks:

- first enrollment, restart reuse, corrupt/mismatched/over-permissive files;
- response-loss rotation, reconnect backoff and clean shutdown;
- no token/key in errors or logs;
- bounded stream queues and race/leak checks.

Commit: `feat: run jobs over resilient mTLS work streams`.

## Batch 9 — executor cancellation and active lease lifecycle

Files:

- refactor `internal/runner/docker.go` and tests;
- update `cmd/yuanci-runner/main.go` orchestration tests.

Work:

1. Create a per-Job context that is canceled on Server cancel, lease deadline,
   Runner shutdown or Job timeout.
2. Track deterministic Docker resource names and ensure container, network and
   workspace cleanup runs with a bounded cleanup context after cancellation.
3. Do not execute before accepted start. Do not send successful completion after
   lease loss. Report a safe terminal result only while lease remains valid.
4. Enforce local capacity independently of Server assignment and reject excess
   assignments safely.

Tests/checks:

- fake Docker process observes cancellation; cleanup always runs;
- network partition cancels at last confirmed deadline;
- cancellation/start race never creates a container after denial;
- Runner shutdown waits only a bounded interval and leaves no managed resources.

Commit: `fix: stop execution when Runner lease authority is lost`.

## Batch 10 — Compose migration, operator docs and release evidence

Files:

- update `deploy/compose.quickstart.yml`, `compose.production.yml`,
  `compose.runner.yml`, environment examples and `deploy/README.md`;
- update `Dockerfile`, `README.md`, `docs/getting-started.zh-CN.md`, roadmap,
  threat model, OpenAPI/Protobuf documentation and `docs/development-log.md`;
- delete legacy Runner HTTP routes/tests/config only after the mTLS replacement
  passes all checks.

Work:

1. Add one-shot Quickstart PKI/token initialization and persistent Runner identity
   volume while preserving one-command startup. Server never mounts Docker Socket.
2. Production Compose mounts only online intermediate/Server material; Runner
   Compose mounts root trust, enrollment input and its local Docker Socket/state.
3. Reject obsolete `YUANCI_RUNNER_SHARED_TOKEN` settings with a clear migration
   error. Document the incompatible pre-alpha upgrade and rollback-from-backup path.
4. Document PKI ceremony, token transfer, firewall, enrollment, rotation,
   disable/revoke, replacement, backup and troubleshooting in Chinese.
5. Record limits honestly: no logs/secrets/Webhook orchestration and no 72-hour
   soak or production security certification yet.

Verification:

- host unit/integration tests against a separately named disposable PostgreSQL;
- `go test -race -count=1 -timeout=180s ./...` and `go vet ./...` in Linux;
- fresh Quickstart `up --build --wait`: enrollment, assignment, renewal,
  execution, completion, restart credential reuse and forced partition recovery;
- separate control-plane/Runner Compose on different Docker networks/containers;
- inspect Server as non-root/read-only/no Docker Socket and Runner credential modes;
- verify unauthenticated/legacy HTTP Runner paths are absent and wrong certificates
  cannot access gRPC;
- remove only label-verified temporary test containers/networks/volumes. Do not
  restart or migrate the user's existing Quickstart during development.

Commit: `docs: publish Runner mTLS deployment and verification guide`.

## Final acceptance for this increment

The increment is complete only when all ten batches are green and documented:

- no shared Runner secret or Server-generated Runner private key remains;
- one-use registration, rotation and revocation are transactionally enforced;
- all work mutations bind certificate Runner ID plus lease token and deadline;
- assigned loss requeues, running loss fails `runner_lost`, with graph consistency;
- Runner cancels execution when renewal authority expires;
- Quickstart remains one-command and production Runner remains separately deployable;
- real PostgreSQL, Linux race/vet, mTLS integration and fault smoke tests pass;
- no claim of full CI/CD or production v1 is made.
