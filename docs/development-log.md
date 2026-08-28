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
