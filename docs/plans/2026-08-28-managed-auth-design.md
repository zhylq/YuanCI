# Managed authentication setup — approved design and execution plan

The user approved the recommended one-time setup-code workflow on 2026-08-28.
Brainstorming/context review is complete. The writing-plans skill and the UI/UX
search script are unavailable; this document supplies the implementation plan.

## Product and security decisions

- The operator registers the provider application; ordinary members only consent
  to login. Provide a Chinese settings wizard with official registration links,
  exact callback URL, minimum-permission guidance and honest adapter status.
- Keep file-configured authentication available. Add a mutually exclusive managed
  mode requiring PostgreSQL, HTTPS origin and a read-only master-key file. Never
  activate managed settings in the unauthenticated evaluation API.
- A host-only CLI issues a 256-bit, 15-minute, single-use setup code. Store only
  its digest. Redeem it over HTTPS with exact Origin into a 30-minute HttpOnly
  setup cookie. Subsequent writes also require session-bound CSRF. Regeneration
  revokes the previous code, setup session and its drafts. Initialization closes
  this route permanently. The code never travels in a URL or server log.
- Setup requires an explicit numeric GitHub ID for the first administrator; the
  setup code alone is not a login credential. Verify that identity with OAuth.
- Once initialized, only an active instance administrator may manage settings;
  saving/testing a replacement requires recent authentication. Recheck current
  permissions, cookie/session expiry and configuration revision transactionally.
- AES-256-GCM envelope-encrypt Client Secret with revision-bound AAD before DB
  persistence. API responses contain metadata only. Never populate secrets back
  into forms, localStorage, query caches, logs, audits or provider errors.
- Save creates a bounded, expiring candidate. The current configuration stays
  active until a real authorization-code exchange and identity lookup succeed.
  Pin each OAuth flow to an immutable configuration revision. Testing a candidate
  requires the initiating setup/admin session at callback. An administrator must
  verify using an external identity already bound to that same account.
- Activation, bootstrap grant (initial setup only), session rotation and audit
  are atomic. Parallel candidate activations cannot overwrite each other silently;
  stale revisions fail. No delete-last-provider/disable-login operation this batch.
- Only GitHub.com is executable in this increment. Gitee/Gitea/GitLab show guides
  and explicit not-yet-supported labels, never a fake connected status. GitHub
  App installation/repository permissions and webhook setup are separate from login.
- Keep Quickstart/live DB unchanged. Use separate disposable DBs and test images.

## UI contract

Public status drives evaluation/login/setup modes. Setup: unlock, application
instructions and fields, save candidate, authorize to verify and activate. Admin
settings: active metadata, pending candidate, replacement form and verification.
Show inline errors and pending states, semantic labels, visible keyboard focus,
copy success/failure and no unrequested animations. Use existing slate/blue
Tailwind tokens and native form controls. Add login/logout and a useful account
landing page for authenticated mode; do not expose unscoped Run queries there.

## Implementation and verification sequence

1. Add additive migration, domain/service interfaces, code/session lifecycle,
   encrypted candidate persistence and transactional authorization/activation.
   Test replay, expiry, rotation, permission denial, audit rollback and races.
2. Wire host CLI, managed config gate and HTTP API; preserve the file mode.
   Verify CSRF/Origin, no secret echo, config pinning and failed-provider behavior.
3. Add React setup/settings/login UI, tutorial links and tests for pending/error,
   disabled providers, secret clearing and unauthorized access. Browser-check the
   actual server in an isolated HTTPS preview at desktop/mobile sizes.
4. Add Compose/Chinese operator instructions and OpenAPI. Run full Go suite,
   real PostgreSQL, Linux race/vet, frontend tests/lint/build and container smoke.
   Commit coherent green increments; no push or production-readiness claim.

Real GitHub App sandbox verification still requires operator-provided credentials;
mock protocol tests are not reported as real-provider acceptance.
