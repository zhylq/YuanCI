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

## 2026-08-28 — final batch verification

- Rebuilt the latest verification image including authorization and crypto
  changes. All backend tests with Linux `-race` and PostgreSQL integration tests
  passed, followed by `go vet ./...` (verification container exit code 0).
- Built final Server and Runner images as local `foundation-verify` tags; no
  registry publication or replacement of the running application was performed.
- Started a temporary Server with no network, no volumes, read-only filesystem,
  all capabilities dropped and UID 10001. Readiness and embedded HTML checks
  passed. The temporary container was stopped/auto-removed afterwards.
- Confirmed a database-mode Server without the explicit insecure evaluation
  opt-in exits with configuration error code 2. Production remains fail-closed.
- Removed only the `yuanci-foundation-tests` containers/network; disposable
  tmpfs test data was discarded. Local verification images remain for reuse.
  Quickstart Server/PostgreSQL were still healthy and Runner running afterwards.
  Its existing Server still publishes `0.0.0.0:8080`; the loopback-only source
  change is not applied until that application container is recreated.
- This batch does not redesign the UI, deliver OAuth/mTLS or qualify v1. The
  roadmap records the next integration gates instead of a fabricated percentage.

Local commits (not pushed): `7c88654` workflow plan; `120e124` transactional
regressions/fixes; `bb47706` CI and Docker exclusions; `a69c77f` authorization
policy; `163137b` malformed envelope fix. The final documentation commit records
the batch outcome and remaining work.

## 2026-08-28 — session and membership persistence

- Added migration 000002: stable instance scope, protected environments,
  FK-backed memberships, hash-only browser sessions and optional Run ownership.
  Existing migrations were not rewritten; the live Quickstart DB was not used.
- Sessions use 256-bit opaque tokens, explicit expiry, revocation and active-user
  checks. Cookie helpers set Secure/HttpOnly/SameSite=Lax with a __Host prefix.
  Session-bound CSRF tokens are distinct from credentials; no public login bypass
  or token issuance endpoint was added.
- Membership changes enforce the existing scope policy, reject self-grants and
  inherited protected-environment administration, and persist the subject/scope/
  role in their audit event. Session and membership mutations roll back on audit
  failure. Membership edits are serialized to avoid reciprocal-revocation locks.
- Identity unit tests and the PostgreSQL suite passed against the independent
  `yuanci-identity-tests` database. Tested malformed/unknown tokens, expiry,
  suspension, revocation, FK constraints, permission escalation and audit failure.
- OAuth, account linking and administrator bootstrap remain separate gates. The
  internal IssueSession method assumes an already verified external identity.

## 2026-08-28 — scoped browser API and transaction regression

- Added the protected browser handler, session info/logout, exact Origin and
  session-bound CSRF checks, SQL-scoped Run reads/writes and live permission checks
  inside the Run transaction. Old evaluation routing is explicitly named and
  remains the only executable path until OAuth/bootstrap activation.
- Created Runs have server-assigned project/creator. Legacy unowned Runs are
  excluded from authenticated listings. Read-only, unrelated and revoked grants
  cannot create Runs; inherited instance admin grants still cannot manage a
  protected environment without explicit scope authorization.
- Hardened legacy Runner Authorization framing and JSON parsing: reject missing
  Bearer scheme, extra JSON values, misleading media types and oversized bodies.
  Parse errors no longer reflect unknown field names; panic values are not logged.
- PostgreSQL tests cover actual HTTP 401/403, CSRF mismatch, cross-project access,
  logout replay, audit-failure rollback, revocation while authorization waits,
  session expiry during lock waits and additive upgrade preserving existing Runs.
- Five consecutive host runs of PostgreSQL/HTTP/identity tests passed, followed
  by vet. Full Linux `go test -race -count=1 -timeout=120s ./...` and vet passed
  against the disposable database. Frontend's existing test, lint and build passed.
- No OAuth login or public session issuance was simulated as a real provider
  success. No production gate, live database or running application was changed.

## 2026-08-28 — identity batch final checks

- Added an OpenAPI YAML/reference regression test and checks for the browser
  cookie and mutation CSRF/Origin parameters, using the existing Go YAML library.
  This is targeted contract validation, not a complete OpenAPI validator.
  The Docker source allowlist now includes `api` so these tests run in Docker too.
- Final Linux verification included the contract test, full backend race suite,
  PostgreSQL integration tests and vet; the container exited 0. Windows full unit
  suite and vet also passed; that host-only run skipped DB tests as expected.
- Server/Runner local `identity-verify` images built. A temporary networkless,
  read-only Server passed readiness and embedded page checks. Database mode still
  exited with configuration code 2 when the evaluation flag was absent.
- The final verification image excluded `/src/.env` and `.git`. No credentials
  or images were published. Only the dedicated identity-test containers/network
  and their disposable tmpfs data were removed; reusable local images remain.
- Quickstart Server/PostgreSQL were healthy and Runner running at final check.
  No live database migration or application restart was performed by this batch.
- Local commits: `bfce8dd` design, `dbdb570` session/member persistence,
  `821fe5f` scoped authenticated handler. Final contract/documentation commit
  closes this increment. No commits were pushed; hosted CI remains unverified.

## 2026-08-28 — OAuth state and identity transaction

- Added migration 000003 for five-minute, browser-bound OAuth flows and a
  persistent one-time bootstrap subject. State, browser nonce and completion
  tickets are stored as digests; PKCE verifier is not persisted.
- State consumption and final completion each reject replay. New accounts use
  provider/instance/numeric-subject keys, not email/login names. Explicit linking
  checks the same active session, authenticated within ten minutes, and refuses
  identities owned by another user.
- First bootstrap requires the configured GitHub subject. Later accounts receive
  no default roles; subsequent login does not restore revoked admin privileges.
  Identity, bootstrap, session rotation and audit commit together or roll back.
- Real PostgreSQL and identity tests passed: wrong browser/expiry, ten concurrent
  state consumers, ten concurrent bootstrap logins, account conflict, session
  mismatch, suspension and injected audit failure. Existing store/upgrade tests
  also passed. No production data or real provider credentials were used.
