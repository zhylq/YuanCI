# Browser identity integration status

The protected `httpapi.NewAuthenticated` handler and PostgreSQL backend are
implemented and integration-tested. **The executable can opt into the isolated
GitHub authenticated preview; production activation is still blocked.** See the
[preview deployment guide](../auth-preview.zh-CN.md). GitHub App user login,
browser-bound state/PKCE, explicit bootstrap and account linking are implemented;
provider HTTP is mocked in tests, not yet verified with a real GitHub sandbox.
Do not manually issue sessions through SQL, expose a token-minting API, or remove
the production configuration gate. Tests create identities only in isolated DBs.

## Implemented contract

- A trusted, verified identity flow may call `IssueSession` for an active user.
  Tokens have 256 random bits. Only their SHA-256 digest is stored. Sessions have
  an absolute lifetime (default eight hours, maximum 24 hours) and can be revoked.
- Cookies use `__Host-yuanci_session`, Secure, HttpOnly, SameSite=Lax, Path=/ and
  no Domain attribute. The Lax setting accommodates the top-level OAuth
  callback; it is not the sole CSRF defense.
- `GET /api/v1/session` returns user details, expiry and a session-bound CSRF
  token, never the session credential. API responses use `Cache-Control: no-store`.
- Cookie-authenticated mutations require both the configured HTTPS Origin and
  an `X-CSRF-Token` bound to that session. Host and forwarded headers do not define
  the trusted origin. Duplicate cookies and mixed bearer/cookie authentication
  are rejected. The protected router does not expose legacy Runner endpoints.
- `DELETE /api/v1/session` revokes the session and writes audit in one transaction,
  then expires the browser cookie. Errors do not pretend logout succeeded.
- Run creation/listing require an explicit project (repository ID). The backend
  resolves ancestry and live grants, locks them during the transaction and checks
  expiry after permission lock waits. Revocation/suspension cannot commit ahead
  of an already-authorized in-flight operation; subsequent operations recheck.
- Browser-created Runs can only use the `manual` event; ownership and creator
  come from the server-side authorization decision, not submitted metadata.
  SQL filters by authorized project, excluding legacy unowned evaluation Runs.
- Membership changes reject self-grants, unauthorized targets and inherited
  administration of protected environments. Session, grant and Run mutations
  roll back when their audit insert fails. Audit records contain identifiers and
  grant metadata, not session credentials, pipeline YAML or secret material.

## OAuth transaction and bootstrap

Login flows have a five-minute TTL, browser-bound HttpOnly nonce and one-time
state consumption before code exchange. Only hashes of state/nonce/completion
tickets are persisted. S256 PKCE binds code exchange to the initiating browser.
Identity mapping uses numeric provider subject, never a matching email or name.
Explicit linking needs CSRF and the same active session created within ten minutes.
An external identity cannot be transferred from another local account.

The configured numeric GitHub subject is persisted before first login. Only it
may bootstrap administrator privileges, once. Revoked privileges are not restored
by subsequent login; new users receive no default membership. Identity, initial
grant, replacement session, old-session revocation and audit commit atomically.
The upstream token is not persisted, logged or sent to the browser.

## Still required before production

Real-provider sandbox verification, session renewal/idle timeout, multi-session management UI,
last-admin protections, public membership APIs, audit querying/export and stronger
audit storage permissions. Suspension currently invalidates use while suspended;
permanent all-session revocation on account security events is a separate gate.
Runner mTLS remains mandatory for production and is not replaced by browser auth.

## Managed settings increment

The [managed wizard](../managed-setup.zh-CN.md) adds login/logout, setup and admin
settings UI. Initial configuration requires a host-issued one-use setup code,
not first-visitor trust. Replacement credentials are encrypted candidates and
become active only in the verified identity/session/audit transaction. Setup is
permanently closed after bootstrap; later edits require live instance admin
permission and recent authentication. Original file mode is still read-only.

All tests run against a dedicated ephemeral PostgreSQL instance. No production
security audit, provider sandbox login or full end-to-end CI certification is
implied by these tests.

Design references: [OWASP session guidance](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
and [OWASP CSRF guidance](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html).
