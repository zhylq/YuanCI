# GitHub login and explicit bootstrap increment

Continuation of the approved OAuth/RBAC design and instruction to proceed. The
brainstorming review retains opaque PostgreSQL sessions, not JWTs or a parallel
password login. Writing-plans is unavailable; the execution breakdown is here.

## Security decisions

- GitHub.com user authorization code flow with state, browser-bound HttpOnly
  flow cookie and S256 PKCE. Use GitHub App client credentials; repository
  installation/Checks authentication remains separate. No repository scope is
  requested for login. The user token is used for `/user` and not persisted.
- Persist only state/browser digests. Derive the short-lived PKCE verifier from
  the random browser nonce and state. Flows expire after five minutes and are
  consumed before code exchange, so retries after failure must start a new flow.
- External identity keys use provider + provider instance + numeric subject ID,
  never login name or email. Linking requires an existing, recent session plus
  CSRF; the same active session must still be present on callback. An identity
  already owned by another user cannot be moved or automatically merged.
- Configure one explicit GitHub numeric subject for first administrator setup.
  Only that account may initialize an empty instance. Bootstrap is persisted,
  one-time and audited; later login must never restore revoked admin rights.
  New accounts after initialization receive no roles automatically.
- Identity mapping, bootstrap grant, replacement session, old-session revocation
  and audit are one database transaction. No client secret/code/verifier/token
  in logs or errors. Provider redirects are refused and replies bounded.
- Add a mutually exclusive, opt-in authenticated preview startup mode requiring
  PostgreSQL, canonical HTTPS origin, client ID, secret file and bootstrap ID.
  The legacy shared-token Runner API is absent in this mode. Production Compose
  remains disabled until Runner mTLS and other release gates are complete.

## Verification / delivery

1. Unit tests for flow binding/PKCE; additive DB migration; state expiry/replay,
   bootstrap races, identity conflicts, suspended users and audit rollback tests.
2. GitHub HTTP protocol tests (mocks, not claimed real-provider E2E), callback
   tests with real PostgreSQL and concurrent replay; configuration fail-closed.
3. Preview deployment/config documentation, API contract, five repeated database
   runs, Linux race tests, frontend regression and deployment builds. Commit each
   coherent green batch. Never migrate the running Quickstart DB or push changes.

References: [GitHub App user authorization](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app),
[GitHub OAuth code flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps),
[OAuth security BCP](https://www.rfc-editor.org/rfc/rfc9700.html).
