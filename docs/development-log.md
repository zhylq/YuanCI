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

## 2026-09-01 — certificate-bound Runner protocol

- Approved security/recovery design committed as `67321bc`; executable ten-batch
  implementation plan committed as `8bf554c`. This entry covers only protocol
  Batch 1 and does not claim that the mTLS service is already deployable.
- Runner registration and rotation now carry a locally generated CSR. Responses
  reserve the former private-key field numbers/names and can return only public
  certificate chains and deadlines. Authenticated heartbeat/rotation messages no
  longer accept a body Runner ID; the eventual service must bind identity from
  the verified client certificate.
- Protocol v1 now defines capability isolation, active lease tokens/local state,
  typed cancellation/conclusion/rejection reasons, heartbeat/lease timing and
  explicit message bounds. Enforcement belongs to later service batches.
- Go and gRPC bindings are committed under `gen/runner/v1`. Buf 1.72.0 is pinned
  by image digest and both remote generator plugins are version-pinned. Buf lint
  passed, and two consecutive generations produced identical file hashes.
- `go test ./api/... ./gen/...`, `go vet ./api/... ./gen/...` and host
  `go test ./...` passed. PostgreSQL integration tests remained opt-in/skipped;
  this protocol-only batch neither migrated nor restarted Quickstart.

## 2026-09-01 — Runner identity and upgrade recovery schema

- Added forward migration 000007 for digest-only, Pool-bound registration
  tokens; certificate chains/fingerprints/serials and rotation lineage; bounded
  Runner capabilities; Job acknowledgement/renewal timestamps and stable failure
  reasons. Database constraints reject invalid usage counts, fingerprints,
  certificate states, multiple active identities and unrelated second rotation
  replacements.
- Upgrade recovery is transactional. Legacy assigned/unstarted Jobs return to
  `queued`; legacy running Jobs become `failed` with `runner_lost`; downstream
  work becomes `skipped`; old lease hashes/deadlines and legacy certificate
  serials are cleared. Affected Runs receive system audit records, while terminal
  Job history, users and ordinary queued work are preserved.
- Migration integration coverage starts from migrations 000001–000006 with
  queued, assigned, running, downstream and terminal fixtures, then opens and
  reopens the Store. It verifies seven applied migrations, explicit Run/Job
  outcomes, no surviving lease, data preservation, Runner invalidation and the
  new schema constraints.
- Host `go test ./...` and `go vet ./...` passed. The isolated Linux verification
  image ran `go test -race -count=1 -timeout=120s ./...` against PostgreSQL 17
  and then `go vet ./...`; final exit code was 0. Its tmpfs database, containers
  and network were removed. Quickstart was not migrated or restarted.
- Docker build source allowlisting now includes committed `gen/` bindings
  (`bb547c6`), a gap exposed by the first Linux container build after Batch 1.

## 2026-09-01 — offline Runner PKI and strict CSR policy

- Added `yuancictl runner-pki init` and a standard-library PKI implementation.
  It generates Ed25519 root/intermediate/Server keys, separates the offline root
  from the Server bundle, applies constrained CA/Server usages and explicit
  DNS/IP SANs, and writes public-only fingerprint/expiry metadata.
- Initialization accepts only a brand-new child directory. It uses directory-
  rooted file operations, exclusive temporary files, exact permissions, file and
  directory sync, and atomic file renames; validation/write failure removes only
  the directory created by that invocation. Existing targets are never changed.
- Runner CSR validation is capped at 16 KiB, checks the CSR signature and permits
  only Ed25519, P-256 or RSA 2048–4096. It rejects weak/unsupported keys, subjects,
  requested SANs/extensions/attributes, malformed or multiple PEM blocks and
  non-whitespace trailing data. Issuance adds exactly one immutable Runner URI
  SAN and client-auth usage, never private-key material.
- Tests verify chain building, CA path limits, Server/client EKU separation,
  correct/wrong DNS and IP names, serial size, key/certificate matching, manifest
  contents, existing-target preservation, issuer mismatch/expiry and Linux
  `0600` private-file, `0644` public-file and `0700` directory permissions.
- A three-second native fuzz run executed 959,126 CSR inputs without a failure.
  The isolated Linux suite then passed `go test -race -count=1 -timeout=120s
  ./...` against PostgreSQL 17 and `go vet ./...` with exit 0. Its containers,
  tmpfs database and network were removed; Quickstart remained untouched.

## 2026-09-01 — certificate-bound scheduling and lease renewal

- Added normalized Pipeline `runs_on` requirements for operating system,
  architecture, executor and labels, plus parsed disk-byte requirements. The v1
  JSON Schema and example Pipeline describe the same fields; default Jobs target
  Linux Docker Runners in the standard Pool.
- Added a strict Runner Job Store alongside the transitional legacy queue API.
  Claims require a non-zero authenticated Runner identity and use only persisted
  Pool/capability data. PostgreSQL serializes claims per Runner, enforces capacity
  without oversubscription, applies exact capability/label/disk matching, and
  binds each assignment to a digest-only 30-second lease.
- Receipt acknowledgement and start are separate, identity/token/deadline-bound
  and idempotent. Structurally validated heartbeats update capabilities and renew
  valid active leases to one authoritative deadline; invalid leases receive an
  explicit cancellation decision. Completion retains the parent-Run-before-Job
  lock order and cannot be performed by another Runner.
- Shared Memory/PostgreSQL contract coverage verifies the matching matrix,
  standard-to-privileged isolation, unscoped claim rejection, concurrent capacity,
  wrong identity/token handling, duplicate receipt/start/heartbeat behavior and
  lease deadline boundaries. Host `go test ./...` and `go vet ./...` passed. The
  isolated Linux race/PostgreSQL 17 suite and vet passed with exit code 0, and its
  containers, network and temporary database volume were removed.

## 2026-09-01 — deterministic expired Runner lease recovery

- The Server now starts a bounded lease reconciler immediately and repeats every
  five seconds. Each transaction considers at most 100 expired Jobs and logs only
  aggregate outcome counts or a stable `store_error` reason; lease material never
  enters logs.
- Recovery locks each parent Run before its Jobs and rechecks the database clock
  after lock acquisition. Expired assigned Jobs return to `queued` with all lease
  ownership cleared. Expired running Jobs become `failed` with `runner_lost`,
  downstream queued/blocked Jobs become `skipped`, and the Run is finalized as
  failed atomically.
- Every actual recovery writes a bounded audit event in the same transaction.
  Audit failure rolls back the complete graph mutation. `SKIP LOCKED`, state and
  deadline predicates make simultaneous reconcilers and late Runner messages
  converge without duplicate effects.
- Tests cover exact assigned/running outcomes, downstream preservation, batch
  bounds, clean reconciler shutdown, two concurrent reconcilers, audit-injection
  rollback, and rejected late start/completion. Host `go test ./...` and
  `go vet ./...` passed; the isolated Linux race/PostgreSQL 17 suite and vet
  passed with exit code 0. Its containers, network and temporary database volume
  were removed.

## 2026-09-01 — resilient Runner mTLS Work client

- The Runner now generates its Ed25519 identity key locally, enrolls through a
  root-pinned TLS 1.3 channel and atomically publishes a permission-checked state
  directory. Restarts reuse the validated identity without a registration token;
  writable token files are removed after successful enrollment.
- The bidirectional Work client uses certificate identity and opaque Job lease
  tokens for every transition. It accepts an immutable plan, waits for separate
  Server acknowledgement of receipt and start before execution, sends bounded
  five-second heartbeats, retains completion across response loss and reconnects
  with capped jittered backoff. One sender owns the stream and all local queues
  and capacity are bounded.
- Certificates rotate with a fresh local key at six hours remaining. The pending
  key/CSR is durably stored before the RPC and reused after response loss; the
  complete credential directory is switched atomically after certificate, key,
  chain, URI identity, validity and client-auth verification.
- The Server Work stream now performs capability-bound assignment, renewal,
  receipt, start, completion and cancellation decisions and permits only one
  active session per certificate Runner identity. Errors exposed to the Runner
  remain generic and do not include credentials or lease tokens.
- Tests cover enrollment/restart, corrupt, symlinked and over-permissive state,
  response-loss rotation, real TLS assignment-to-completion, invalid plans,
  clean shutdown and reconnect jitter bounds. Host `go test ./...` and
  `go vet ./...` passed. The isolated Linux/PostgreSQL 17 suite passed
  `go test -race -count=1 -timeout=120s ./...` and `go vet ./...`; its dedicated
  containers, network and database volume were removed. Quickstart was not
  changed; executor lease-deadline cleanup is the next batch.

## 2026-09-02 — lease-authoritative execution and Docker cleanup

- Every assigned Job now owns a cancellation context and a timer for the last
  Server-confirmed lease deadline. Renewal safely replaces the timer; cancellation,
  Runner shutdown, Job timeout or loss of lease authority stops execution. Once
  authority is lost it cannot be restored by a late response, and no successful
  completion is sent for that execution.
- Runner shutdown cancels all active Jobs and waits at most 15 seconds for
  executors. Local capacity remains a hard independent limit, and a Job whose
  start acknowledgement races with lease loss is discarded without invoking the
  executor.
- Docker Jobs use deterministic container, private bridge-network and workspace-
  volume names. Cleanup runs from a separate 15-second context after cancellation:
  all step containers are removed in one command, then network and volume cleanup
  run concurrently so both receive an attempt within the overall bound.
- Tests use a controllable fake Docker process to prove that cancellation reaches
  the process and container/network/volume cleanup still runs. Lease tests cover
  deadline extension, network-partition expiry, start/cancel races and bounded
  Runner shutdown. The Runner package passed ten consecutive focused runs.
- Host `go test ./...` and `go vet ./...` passed. The first Linux attempts were
  interrupted by `proxy.golang.org` EOFs; a disposable run using a one-shot
  alternate module proxy then exposed and fixed two slow-race-test cleanup timing
  assumptions. The final Linux/PostgreSQL 17 run passed `go test -race -count=1
  -timeout=120s ./...` and `go vet ./...` with exit 0. All dedicated containers,
  network and database storage were removed.

## 2026-09-02 — mTLS Compose migration and deployment smoke

- Removed the obsolete HTTP polling/shared-token Runner client, routes and
  configuration. Server and Runner now reject the old shared-token variables;
  the only execution channel is certificate-authenticated gRPC.
- Added a default standard Runner Pool migration, one-shot Quickstart PKI and
  registration-token initialization, persistent Runner identity, non-root
  Runner image, and separate production control-plane/Runner Compose examples.
  The Server remains read-only and never receives the Docker Socket.
- A fresh, isolated `yuanci-mtls-smoke` Quickstart exposed and fixed a parent
  PKI directory traversal-permission error. After the fix, Server health passed,
  the one-use token was deleted, the Runner persisted its identity, and a real
  Alpine Job moved from queued to succeeded through PostgreSQL, mTLS Work and
  the Docker executor. Restarting Runner reused the same Runner ID and certificate.
- The smoke used host port 18080 because the user's existing Quickstart already
  owned 8080; the existing deployment was not modified. The isolated environment
  was removed after verification. A forced Runner network partition canceled a
  120-second task at the last confirmed lease deadline; Server reconciliation
  finalized the Run as failed and no managed container, private network or
  workspace volume remained. Recreating only the Runner restored its mTLS Work
  stream while reusing the same identity.
- Host `go test ./...`, `go vet ./...`, all 23 web tests, ESLint and the production
  web build passed. The first isolated Linux/PostgreSQL 17 race run correctly
  exposed three fixtures that still expected eight migrations or an empty Pool
  table; after updating them for migration 000009, `go test -race -count=1
  -timeout=120s ./...` and `go vet ./...` passed with exit code 0. Both dedicated
  Docker projects, networks and temporary data volumes were removed.
- Published a Chinese PKI/operator guide covering the offline-root ceremony,
  one-time token transfer, firewalling, split-host deployment, automatic rotation,
  replacement, backup boundaries, troubleshooting and the incompatible pre-alpha
  shared-token migration. It explicitly records that supported disable/revoke
  administration, cross-host rehearsal, forced-partition smoke, logs/secrets,
  webhook orchestration, 72-hour soak and security certification remain open.
## 2026-09-02 — project automatic-build policy persistence

- Added provider-neutral per-project automation settings with safe synthesized
  defaults: automatic builds remain disabled while push, tag, same-repository PR,
  older-commit cancellation and `.yuanci.yml` are prepared for explicit use.
- Pipeline paths are bounded, relative YAML paths and reject control characters,
  Windows separators, normalization ambiguity and traversal in both Go and
  PostgreSQL. Enabling remains closed until the next immutable GitHub config
  validation increment can prove the installation and pipeline are usable.
- Writes require `repository.manage`, recheck the browser session, use a
  repository-scoped lock plus expected revision for first-write-safe compare and
  swap, and commit the bounded audit event atomically with the settings.
- Tests cover defaults, viewer rejection, revisions, stale and simultaneous
  first writes, premature enablement, database constraint defense and audit
  rollback. Focused and full Linux/PostgreSQL 17 Race suites plus `go vet` passed;
  all four Compose files also passed configuration validation.

## 2026-09-02 — immutable GitHub pipeline retrieval

- Added a trusted GitHub runtime repository lookup that binds an active imported
  repository to its installation and current active GitHub App configuration;
  webhook owner, repository name and clone URL are never used as authority.
- GitHub App credentials now mint a short-lived installation token restricted to
  exactly one repository and read-only `contents` permission. Token responses,
  permissions, repository identity and lifetime fail closed, while private-key
  and token byte buffers are cleared after use.
- Pipeline configuration is fetched only from the 40-character commit SHA in the
  verified event, with a validated bounded repository-relative YAML path and a
  one-MiB response limit. External-fork pull requests are rejected before any
  database credential lookup or token issuance.
- Unit tests cover trusted-local identity, exact-SHA requests, least-privilege
  token bodies, redirects, malformed inputs, excessive permissions, unsafe token
  lifetimes and secret-safe errors. PostgreSQL integration tests cover active and
  inactive imported repositories. The isolated Linux/PostgreSQL 17 suite passed
  `go test -race -count=1 -timeout=120s ./...` and `go vet ./...` after one
  transient Alpine package-download retry.

## 2026-09-03 — atomic GitHub webhook run persistence

- Added the transaction boundary that persists a Pipeline definition and
  immutable version, creates the Run and complete Job dependency graph, links
  the claimed webhook delivery, clears its lease, and writes the audit event as
  one PostgreSQL commit.
- Run records can now carry internal Pipeline-version and idempotency identities
  without exposing them in the existing public JSON shape. Manual Run creation
  continues to use the same graph-writing path.
- The trigger key is derived from YuanCI's immutable delivery UUID. Replays
  verify repository, commit SHA, event, Pipeline name and configuration hash
  before reusing an existing Run, and perform no Pipeline or Job writes when
  those values conflict.
- Runtime automation lookup now requires an active repository and synthesizes
  disabled defaults only for a repository that actually exists. Integration
  tests cover the full committed graph, inactive repositories, valid replay,
  conflicting replay, wrong leases and rollback when audit persistence fails.
- The clean Linux/PostgreSQL 17 verification passed `go test -race -count=1
  -timeout=120s ./...` and `go vet ./...`. The first image build was canceled
  after an Alpine package download stopped producing output; the clean retry
  completed with exit code 0.

## 2026-09-03 — secure source assignment negotiation

- Extended Runner protocol v2 with separate trusted source metadata and
  ephemeral credential messages; credentials remain outside immutable plans
  and persistent assignment records. Protocol v1 remains accepted for
  source-free jobs during rolling upgrades.
- PostgreSQL scheduling now reads the enrolled Runner protocol version before
  claiming work. A protocol-v1 Runner cannot claim a repository-backed Run;
  protocol v2 receives provider, stable repository identity, trusted HTTPS
  clone URL and the exact event commit SHA from local database state.
- Heartbeats must use the protocol version bound at enrollment, preventing a
  connection from advertising a newer protocol and then downgrading its work
  session. Generated Protobuf bindings were refreshed from the pinned Buf
  image.
- Focused protocol, scheduler and PostgreSQL tests passed. A real PostgreSQL 17
  integration test verifies v1 rejection and v2 source metadata, followed by
  `go test ./...` and `go vet ./...` in the pinned Go container.

## 2026-09-03 — token-efficient v1 task sequencing

- Replaced open-ended "continue development" sessions with an approved atomic
  task contract: normally one 20–45 minute capability, focused tests, one
  commit, one push, and a clean handoff to a new conversation.
- Cataloged the remaining GitHub Alpha, complete CI, Secret, SCM, console, CD,
  operations and qualification work with stable task IDs, dependencies, model
  guidance and exit tests. Full repository verification is reserved for named
  phase gates to avoid repeated builds while retaining release rigor.
- Added a reusable Chinese trigger prompt so repository documents and Git state,
  rather than accumulated chat history, carry context between tasks. The next
  implementation task is `GH-01`.

## 2026-09-03 — GH-01 single-delivery GitHub orchestration

- Added a single-delivery orchestrator that resolves an active imported GitHub
  repository and its automation policy from trusted local state before any
  remote configuration request.
- Disabled projects, external-fork pull requests, disabled event types and
  unsupported event types now finalize as explicitly classified ignored
  deliveries without fetching configuration or creating a Run.
- Accepted events fetch the configured pipeline only at the immutable event SHA,
  verify that the trusted repository identity did not change between policy and
  credential resolution, compile the pipeline, and use the existing atomic Run
  commit with distinct created and idempotently reused outcomes.
- Focused `internal/githubci` unit tests cover every outcome classification and
  the repository-identity guard. The affected PostgreSQL package test and the
  specific PostgreSQL 17 integration test passed, as did focused `go vet` and
  `git diff --check`. GH-01 is not a phase gate, so no full repository suite,
  frontend build, Compose validation or deployment-image build was run.

## 2026-09-03 — GH-02 webhook failure policy

- Extended the single-delivery orchestrator with explicit transient and
  permanent failure categories. Rate limits, interruption and unknown
  infrastructure failures retry safely; invalid delivery/configuration,
  unavailable credentials or repositories, missing pipeline files and trusted
  repository mismatches dead-letter immediately.
- Retries use a deterministic exponential schedule starting at five seconds,
  capped at fifteen minutes, and stop at the existing twelve-attempt limit.
  Exhausted deliveries receive a distinct stable terminal classification.
- Delivery finalization persists only bounded stable codes and fixed summaries;
  raw provider, database, parser and credential-bearing error text is never
  copied into the inbox record. Unit coverage includes secret-bearing wrapped
  errors, permanent failures, retry exhaustion and finalization failure.
- The focused `internal/githubci` test and vet runs passed in the pinned Go 1.27
  container, followed by `git diff --check`. GH-02 is not a phase gate, so no
  full repository suite, frontend build, Compose validation or deployment-image
  build was run.

## 2026-09-03 — GH-03 GitHub delivery worker lifecycle

- Added a single-concurrency GitHub delivery worker that claims from the durable
  inbox with a one-minute lease, processes one claimed delivery at a time, and
  uses a bounded idle wait instead of busy polling. Database-backed claims remain
  safe when more than one Server process is running.
- Lease recovery runs once before delivery processing and every five seconds
  thereafter, with a maximum batch of 100. Worker logs expose only stable
  aggregate counts or reasons, never delivery payloads, lease IDs or credentials.
- Managed authenticated Server startup now creates the trusted GitHub pipeline
  client, orchestrator and worker. On service shutdown the worker context is
  canceled and the process waits for its loop to exit before closing storage.
- Focused worker tests cover cancellation, duplicate-worker single processing
  and repeated bounded recovery; the Server lifecycle test covers worker startup
  and shutdown. Focused `go test`, `go vet` and `git diff --check` passed. GH-03
  is not a phase gate, so no full repository suite, frontend build, Compose
  validation or deployment-image build was run.

## 2026-09-03 — GH-04 visible configuration-failure Runs

- Enabled projects now convert a missing or invalid immutable pipeline file into
  a terminal visible failed Run instead of only dead-lettering the webhook. The
  Run retains the trusted repository, event ref and commit identity plus a hash
  of the unavailable or invalid configuration, without persisting invalid source.
- Failed configuration Runs use a valid empty compiled-plan envelope and create
  no executable Jobs. Their delivery link, stable redacted error category,
  terminal timestamp and audit event are committed in one transaction under the
  existing delivery lease and webhook idempotency key.
- Focused orchestration tests cover missing and invalid configuration behavior.
  PostgreSQL 17 tests cover visibility, zero Jobs, idempotent replay and complete
  rollback when audit persistence fails. Focused package tests, vet and
  `git diff --check` passed. GH-04 is not a phase gate, so no full repository
  suite, frontend build, Compose validation or deployment-image build was run.

## 2026-09-03 — GH-05 authenticated automation validation and enablement

- Added authenticated project automation read/update and immutable pipeline
  validation endpoints. Browser mutations retain exact-Origin, session-bound
  CSRF and live `repository.manage` authorization checks.
- Validation resolves the trusted GitHub default branch to one exact commit,
  fetches and compiles only at that SHA, and persists a bounded proof containing
  the settings revision, active GitHub App revision, path, commit SHA and config
  hash. Remote work occurs outside database transactions.
- Enablement now uses the repository-scoped compare-and-swap lock and accepts a
  request only when its expected revision, pipeline path and current App match a
  successfully committed proof. Concurrent settings, identity, installation or
  App changes invalidate the operation; validation and update audits are atomic.
- Focused service/provider, HTTP, project and PostgreSQL 17 tests cover immutable
  SHA retrieval, RBAC, Origin/CSRF rejection, stale revisions, unvalidated and
  stale-proof enablement, safe provider failures, migration, and audit rollback.
  Focused package tests, vet and `git diff --check` passed. GH-05 is not a phase
  gate, so no full repository suite, frontend build, Compose validation or
  deployment-image build was run.

## 2026-09-03 — SRC-01 repository checkout credential issuance

- Added a dedicated GitHub App service contract that issues an ephemeral
  checkout token only when the trusted local repository UUID and GitHub
  repository ID resolve to the same active imported binding.
- Checkout credentials retain the provider-enforced single-repository
  `contents:read` scope and are accepted only with a bounded token value and an
  expiry more than 30 seconds but no more than 65 minutes in the future.
- Decrypted GitHub App private-key buffers are cleared after every issuance
  attempt. Rejected or partially returned provider token buffers are also
  cleared; ownership of a successful token is explicitly transferred to the
  caller for later delivery and cleanup.
- Focused `internal/githubapp` and `internal/integration` tests cover binding,
  lifetime, provider-failure and buffer-clearing behavior. Focused tests, vet
  and `git diff --check` passed. SRC-01 is not a phase gate, so no full
  repository suite, frontend build, Compose validation or deployment-image
  build was run.
