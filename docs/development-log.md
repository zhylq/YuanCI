# Development log

## 2026-08-28 — start controlled incremental delivery

- Baseline: `dce4030` on `main`; no uncommitted changes at inspection.
- Quickstart Server/PostgreSQL were healthy and Runner running at inspection.
  No password reset, data deletion or running-service restart was performed.
- Approved plan: `plans/2026-08-28-platform-foundation.md`.
- Status: pre-alpha. No production approval, hosted CI result or third-party
  security review is implied by this log.

Further entries record completed checks and known limitations per batch.

## 2026-08-28 — transactional store regression suite

- Added a shared memory/PostgreSQL store contract: atomic invalid-plan rejection,
  duplicate IDs, immutable stored plans, wrong/expired lease rejection, completion
  replay, cancellation and 20 concurrent claims/completions with a dependent join.
- RED: memory storage accepted malformed/duplicate records and leaked mutable
  plan buffers; cancellations became failures. Real PostgreSQL also mislabeled
  cancellation, and concurrent completions left a DAG join blocked.
- GREEN: per-Run parent-first locks serialize graph changes without locking other
  Runs. Completion checks wall-clock lease expiry after waiting for the lock.
  Memory results are copied and invalid creates do not mutate state.
- Actual checks: `go test -count=10 -timeout=120s ./internal/run
  ./internal/store/postgres` passed against isolated PostgreSQL 17; `go vet ./...`
  passed. Linux Docker `go test -race -count=1 -timeout=120s ./...` plus vet passed.
  Concurrent migration startup and close/reopen persistence also passed.
- Each integration case creates and drops only its own UUID-named database on
  the dedicated test server. Quickstart data was not used or changed.
- Limits: this is a short concurrency regression, not the 72-hour release soak.
  Lease heartbeat/recovery and runner identity/capability enforcement are pending.

## 2026-08-28 — repeatable CI and safer Docker packaging

- Added a read-only GitHub Actions workflow with pinned action SHAs, console
  test/lint/build, isolated PostgreSQL integration/race tests, vet, Compose
  validation and deployment image builds. No images are published.
- Added `compose.test.yml`: separate Compose project, loopback dynamic database
  port and tmpfs database storage. Docker-only verification needs no host Go.
- Local console test (1 test), ESLint and TypeScript/Vite build passed. All four
  Compose files validated. Full Linux race tests and vet passed again after the
  Docker source allowlist change; PostgreSQL tests ran, not skipped.
- Security finding: baseline `.dockerignore` omitted `.env`. The first local
  verification build used that baseline and may have included local credentials
  in its intermediate layer. Added exclusions and explicit source COPY rules,
  rebuilt, and verified the new image lacks `/src/.env`, `.secrets`, and `.git`.
  The old image was no longer addressable by its emitted manifest/config IDs;
  no deletion or secure erasure of shared BuildKit cache is claimed. No image or
  cache was pushed/exported. Rotate evaluation credentials before production
  or exporting old caches. No live credentials were printed or changed.
- Quickstart now defaults to loopback-only publishing. Existing application
  containers were not restarted; operators must recreate them to apply this.
- Hosted CI and branch protection remain unverified until the commits are
  published and repository checks are configured. Local success is not hosted CI.

## 2026-08-28 — four-level default-deny authorization policy

- Added the pure `internal/authorization` evaluator and an explicit permission
  matrix in `security/authorization.md`. Tested RED with a deny-all evaluator,
  then implemented exact role allowlists, scope ancestry and protected mutations.
- Tests cover 1,440 role/action/target/grant combinations, malformed/unknown
  inputs, cross-instance/org/project/environment access, suspended identities,
  independent grants, explicit protected access and approval separation.
- `go test -count=1 -cover ./internal/authorization` and `go vet` passed;
  statement coverage was 100%. This is policy coverage, not proof of security.
- Not delivered: persisted roles/memberships, OAuth, sessions, HTTP enforcement,
  transactional approval/audit. Existing API security gates remain unchanged.

## 2026-08-28 — malformed secret envelope regression

- RED: opening an envelope with an absent nonce panicked in AES-GCM.
- Added nonce/wrapped-key size validation before decryption, returning errors
  without plaintext for corrupt envelopes. Ciphertext format is unchanged.
- Tests cover missing/short/long nonces and missing/tampered wrapped keys or
  ciphertext. All passed. A 15-second, two-worker fuzz run completed 2,045,273
  executions without a failure. This bounded run is not a formal crypto audit.
- Windows `go test -count=1 -timeout=120s ./...` and `go vet ./...` passed.
  PostgreSQL tests in this host-only command were skipped (no DB URL); the
  independent Docker integration run is the evidence for database behavior.
