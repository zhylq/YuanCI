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

## 2026-08-28 — GitHub callback and authenticated preview runtime

- Added a fixed-endpoint GitHub user OAuth client with S256 PKCE, ten-second
  request timeout, bounded JSON responses, no redirect following and generic
  errors. User access tokens are used only for identity lookup, not persisted.
- Added login start/callback and CSRF-protected explicit identity linking.
  Callbacks reject ambiguous query/cookies, consume state before exchange, clear
  flow cookies and issue a new Secure/HttpOnly session only after DB commit.
  Landing redirects are fixed; callback logs never include query strings.
- Added an opt-in authenticated-preview runtime, requiring PostgreSQL, HTTPS
  origin, file-supplied GitHub client secret and explicit numeric bootstrap ID.
  Evaluation/memory/legacy Runner credentials cannot be mixed into this mode.
  Production is still blocked. The preview has no legacy Runner routes.
- Five consecutive PostgreSQL/identity/HTTP/config runs passed. Further HTTP
  tests cover cancellation, successful linking/session rotation and verification
  of the exact PKCE verifier; the full host Go suite (including real PostgreSQL)
  and vet passed afterward. Ten simultaneous callbacks exchange a code once.
- Provider tests use mocked HTTP responses, not a real GitHub sandbox. The
  console login UI and project selection are not delivered in this increment.

## 2026-08-28 — OAuth increment final verification and deployment guide

- Published the separate authenticated-preview Compose template, empty credential
  example and Chinese instructions for users without a host Go installation.
  Documented HTTPS/proxy requirements, file-secret permissions, explicit numeric
  administrator ID, one-time bootstrap behavior and current UI/Runner limitations.
- Updated OpenAPI with login start/callback/link and regression checks for public
  login entry points versus session/Origin/CSRF-protected identity linking.
- Final Docker verification rebuilt all Go executables and the embedded console;
  full Linux `go test -race -count=1 -timeout=120s ./...` plus `go vet ./...`
  passed against the isolated PostgreSQL database, including the new contract
  tests and additive migration regression. Verification exited 0 twice this batch.
- Frontend's existing one-test suite, lint and production build passed. These are
  regression checks, not browser acceptance of a login UI (none added yet).
- Preview Compose validated with public fixture values. The local Server image
  ran as UID 10001 with a read-only root and file-mounted fixture secret against
  disposable PostgreSQL: readiness/home 200, session 401, legacy Runner 404,
  login start 303 with GitHub destination, S256 PKCE and secure flow cookie.
  Redirects were not followed. DB had three migrations and one configured
  bootstrap record. A networkless default-mode test still exited with code 2.
- Removed only the verified OAuth smoke/test containers, network, tmpfs test data
  and public fixture secret file. Local verification images remain. Quickstart
  Server/PostgreSQL were healthy and Runner running; no live migration/restart.
- Local commits in this increment: `cdab6f1` design, `0e455ed` OAuth persistence,
  `66db6a4` callback/runtime; this documentation/contract commit closes the batch.
  No push, real GitHub App login, production activation or v1 completion claimed.

## 2026-08-28 — managed settings backend and CLI

- Added migration 000004, one-use 15-minute setup codes and 30-minute setup
  sessions. Host CLI issuance, rotation and redemption are audited; initialization
  closes setup. Only hashes are stored. Master-key generation refuses overwrite
  and never prints the key; persisted key binding rejects accidental replacement.
- Added revision-bound envelope-encrypted candidate settings and live instance
  admin checks. Candidates expire, preserve the active configuration until real
  OAuth verification, and pin flows to immutable revisions. Candidate replacement,
  stale verification, wrong identity/session and revoked administrator are rejected.
- Bootstrap/config activation/session rotation/audit are one transaction. Added
  managed HTTP setup/settings/status routes with exact Origin and CSRF checks;
  evaluation and file-configured modes remain separate.
- Real disposable PostgreSQL tests passed for code replay/races/expiry, encrypted
  storage/no metadata leaks, bootstrap closure, replacement ownership/revocation,
  revision pinning, expired setup sessions and audit-failure rollback. HTTP tests
  exercise a complete mock-provider wizard and provider failure without activation.
- Full Go regression passed before the HTTP increment; targeted DB/config tests
  passed afterward. Final full/race/UI/container verification follows separately.

## 2026-08-28 — self-service login console

- Added the setup-code screen, GitHub App tutorial, exact callback copy action,
  write-only credential form, candidate verification and administrator settings.
  Added login/account/logout screens and a fail-closed authentication boundary.
  Evaluation and file-managed modes cannot write managed settings; unsupported
  Gitee/GitLab/Gitea cards provide official links without accepting credentials.
- Secrets are excluded from query/mutation caches and browser storage; the
  credential input clears before submission. Forms have associated labels,
  pending states, field errors, visible keyboard focus and clipboard fallback.
  Responsive review changed the mobile reading order to show the form first.
- Frontend: 8 tests in 3 files, ESLint, TypeScript and production build passed.
  Browser inspection of the actual smoke image covered the locked setup page,
  malformed-code error and narrow/tablet/desktop layout (360/753/1265 CSS px),
  with no horizontal overflow. Browser viewport override was reset afterward.
- Browser testing used loopback HTTP for public page/validation only. No Secure
  cookie bypass, real provider login or trusted-HTTPS browser acceptance claimed.
  Complete setup/activation flows are covered separately by mock-provider HTTP
  tests against real disposable PostgreSQL.

## 2026-08-28 — managed deployment and final verification

- Added separate managed Compose and empty environment example. A networkless,
  non-root key-init service creates a non-overwritable 0600 master-key file in a
  dedicated volume. The non-root/read-only Server mounts it read-only and now
  includes yuancictl for host-authorized setup-code issuance. No Docker socket.
- Chinese guide covers independent deployment, trusted HTTPS, App registration,
  setup-code expiry/rotation, candidate verification, replacement, safe key backup
  and troubleshooting without requiring Go on the operator's machine. Existing
  Quickstart/file-preview documentation explicitly links to the separate mode.
- Added OpenAPI setup/settings/status contracts, separate setup-cookie security
  and write-only credential schema. Origin/CSRF contract regression checks passed;
  only the one-time-code exchange is exempt from existing-session CSRF. Final
  setup-cookie/write-only assertions and optional expected_active alignment were
  checked again with `go test ./api` after the full Linux run.
- Full Linux `go test -race -count=1 -timeout=120s ./...` plus `go vet ./...`
  passed twice this batch against isolated PostgreSQL (verification exit 0).
  The final run included the new UI build and managed OpenAPI routes. Frontend
  tests, lint and build were rerun successfully after the responsive adjustment.
- Fresh managed-smoke key initialization and Compose `up --wait` succeeded. An
  API smoke test against the actual image verified CLI code issuance, wrong
  Origin rejection, secure setup cookie, replay rejection, CSRF rejection,
  candidate save/no secret in metadata, and GitHub authorization URL with S256.
  It did not contact GitHub; the candidate correctly remained inactive.
- Removed only verified managed-smoke and setup-tests containers/networks and
  disposable database/key volumes. Local build images remain. Existing Quickstart
  Server/PostgreSQL were healthy and Runner running; no live migration/restart.
- This batch has design commit `27e9f96`, backend `8a09548`, UI `b5633d2`, followed
  by this deployment/contract/documentation commit. No push was performed.
  Real GitHub App + trusted HTTPS acceptance, other SCM integrations, protected
  Runner execution and full v1 security/stability gates remain future work.

## 2026-08-28 — authorized project browser backend

- Added read-only project/list/detail/run-summary ports and authenticated HTTP
  routes. No evaluation route, project import, session bypass or live migration.
  Active repositories are filtered by live scoped grants before keyset paging;
  the canonical policy rechecks returned project paths. Environment-only grants
  never expose parent repositories, and unavailable/missing IDs share one denial.
- List reads synchronize with membership edits; session validity is checked after
  resource lock waits. Added bounded literal search and cursor parsing. Payloads
  exclude clone URLs, external identifiers, compiled plans and global totals.
  Local active flags never imply verified SCM connectivity.
- Full host Go suite and vet passed with real disposable PostgreSQL. Tests cover
  105-item paging, duplicate/inherited grants, cross-org reads, environment-only
  access, disabled repositories, equal-time run cursors, foreign cursor scoping,
  minimal DTOs, malformed HTTP parameters and revoked/suspended/expired sessions.
  All three reads reject sessions expiring during blocked resource access.
- Frontend and final Linux/browser verification are recorded in the next batch.

## 2026-08-28 — project console and regression verification

- Added Projects navigation, account entry, permission-scoped search/keyset paging,
  repository detail and run-summary pages. Query keys include user/project/page;
  previous results are hidden on navigation, permission errors and failed refresh.
  Requests are cancellable, refresh on focus/every 30 seconds, and do not run in
  evaluation mode. No fake connected state, import form or build action was added.
- UI guidance retained native controls and existing theme, wrapped mobile
  navigation, increased nav targets to 44px, and provided accessible search,
  pending/error/empty states. No animations or new runtime dependencies.
- Frontend now has 17 passing tests across 4 files, including 9 new project tests.
  Tests/lint/TypeScript/build passed again after the final navigation adjustment.
  Tests distinguish empty results from failures, reset paging on search, isolate
  project changes and hide cached records after authorization failures.
- Added an explicit loopback-only visual fixture, with fictional data and a
  visible warning. Browser checks covered search, detail, run pagination, long
  branch/SHA wrapping and 375/768/1024/1440 viewport overrides (content widths
  360/753/1009/1425px), without horizontal overflow. Real auth is not bypassed:
  this fixture has no database or OAuth and is excluded from the Server image.
- Linux `go test -race -count=1 -timeout=120s ./...` and `go vet ./...` passed
  against disposable PostgreSQL, including the new API contracts. Verification
  exited 0. This precedes only the final 44px navigation-class adjustment.
- Actual Server image smoke: UID 10001/read-only root, ready and SPA routes 200;
  all three project APIs return 401 without a browser session. No GitHub request
  was made. Quickstart Server/PostgreSQL were healthy and Runner running.

## 2026-08-28 — project browser handoff

- Published explicit read-only OpenAPI schemas and tests for inherited session
  protection and minimal fields. Added Chinese usage/upgrade instructions and
  updated status pages to distinguish project browsing from pending SCM import.
- Browser also confirmed generic denial, expired-session and empty-installation
  states. Viewport override was reset and the temporary tab closed. The fixture
  server was stopped; only verified project-smoke/project-tests containers and
  their network/tmpfs database plus the public fixture-secret file were removed.
  Local build images remain. No real credentials or Quickstart data were changed.
- Commits: `5ec9a3f` approved design, `8422a62` backend, `e4ad94f` console, then
  this contract/documentation handoff. No push. No actual provider integration,
  trusted-HTTPS browser login or v1 production qualification is claimed.

## 2026-08-28 — GitHub App installation discovery and repository import

- Approved design `07efcf6`; backend/API `ab81081`; linked-identity and cleanup
  hardening `6ddd5a3`; guided console `4dba3cc`. All local on main; no push.
- Managed preview only: verify RSA App key via GET /app against active login
  Client ID, then envelope-encrypt it. Installation page uses verified App slug.
  Setup URL parameters never authorize or write projects. Separate OAuth/PKCE
  callback requires original browser session and single-use state/nonce; it
  neither logs in nor grants privileges. Supports multiple already-linked GitHub
  identities; proof records the actual verified identity and rejects later unlink.
- User authorization is encrypted and usable at most ten minutes; no refresh
  token persisted. Revision/session/identity/admin checks run again after remote
  I/O and transaction lock waits. Background cleanup runs at startup/every minute;
  expired credentials are unusable even if deletion fails. Backups/WAL may retain
  encrypted material and require their own retention/revocation procedures.
- Discovery uses user/App intersection, App/account/suspension verification and
  repository-admin permission. Requests use fixed GitHub endpoints, no redirects,
  bounded response sizes/pages/timeouts and sanitized errors. No remote writes or
  installation access-token automation. Import refetches permissions and creates
  a dedicated organization by stable GitHub account ID. No implicit memberships,
  project adoption/move, metadata replacement or reactivation on repeat import.
  Project creation and audit are atomic; concurrent repeats create one record.
- New 000005/000006 forward migrations preserve existing project/user data;
  000006 invalidates prior short-lived proofs to require explicit subject binding.
  Existing deployed Quickstart was not migrated or restarted by this batch.
- Host full Go tests and vet passed against disposable PostgreSQL. Added tests
  cover JWT/key validation, endpoint/redirect/size/transport errors, App/account
  mismatch, PKCE, admin filtering, token encryption/no echo, forged/replayed/
  superseded flows, multiple identity/unlink, stale App revision, duplicate import,
  local/remote revocation, audit rollback, name collision and proof expiry while
  waiting on a repository lock (including rollback of a new preceding project).
- Linux verification ran twice: `go test -race -count=1 -timeout=120s ./...` and
  `go vet ./...`, exit 0. Final run includes linked identities, cleanup, lock-wait
  expiry and expanded OpenAPI contracts. Real PostgreSQL tests were not skipped.
- Console has 23 passing tests across five files; lint and production build pass.
  UI skills kept existing theme/native controls, accessible labels and local
  errors, no added dependencies/animations, and no credential query/mutation cache.
  Private key input clears on submission; wrong mode/non-admin shows no form.
  Repository selection is explicit, capped at 20 and reset on paging/refresh.
- Loopback-only UI fixture checks in the browser covered instructions, installation
  selection, checkbox import/result links, permission denial, GitHub throttling and
  evaluation-mode gating. Desktop screenshot inspected. This batch did not repeat
  the earlier multi-width viewport checks or a real trusted-HTTPS OAuth journey.
  Fixture is visibly synthetic, has no database/OAuth and refuses credential saves.
- Fresh actual managed Docker image smoke completed key initialization and
  `up --wait`; ready/SPA routes 200, all new protected reads/writes 401 without a
  session, malformed callback 400. Six migrations present; Server UID 10001 and
  read-only root confirmed. No real GitHub credentials or requests were used.
- Chinese guide includes exact Docker upgrade command, App/private-key/callback
  instructions, minimal permissions, installation/repo selection, expiry/retry
  advice, backup/forward-migration warning and sandbox acceptance checklist.
  Real GitHub E2E, private checkout/webhook orchestration, Runner mTLS/recovery,
  other providers, member UI and complete v1 release gates remain unfinished.
- Cleanup verified ownership first, then removed only import-tests/import-smoke
  containers/networks and the temporary DB/key volumes (disposable, rebuildable).
  Browser tab and fixture process stopped; build images retained. Existing
  Quickstart Server/PostgreSQL remained healthy and Runner running.
