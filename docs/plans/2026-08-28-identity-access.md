# Identity and API access increment

Continuation of the approved system design and the user's instruction to proceed.
The brainstorming review keeps the existing modular monolith; no new service or
user-facing login bypass is introduced. The writing-plans skill is unavailable,
so the implementation/acceptance breakdown is recorded here directly.

## Decisions

- Use opaque 256-bit browser session tokens with only SHA-256 digests persisted.
  Compared with stateless JWTs, this allows immediate logout/revocation and
  suspended-user checks without a separate revocation cache. Do not reuse Runner
  tokens for browser authentication.
- Add an instance singleton, environment scopes, FK-backed memberships, session
  records and optional project ownership on Runs through an additive migration.
  Here a project is the existing repository ID. Old evaluation Runs remain
  unowned; authenticated queries must never return them.
- Use the tested RBAC evaluator with ancestry resolved by PostgreSQL. Membership
  edits, session lifecycle and authenticated run creation write audit records in
  the same transaction. No tokens, YAML or plaintext secrets in audit metadata.
- Protected HTTP handlers require `__Host-yuanci_session` (Secure, HttpOnly,
  SameSite=Lax, Path=/). Mutations require the configured HTTPS Origin plus a
  session-bound CSRF token. No trust in arbitrary Host/X-Forwarded headers.
- Scoped run operations revalidate the session, user status, resource and grants
  inside their transaction, holding row locks through commit. List queries are
  filtered in SQL to an explicitly requested authorized project.
- Keep evaluation routing explicitly named and isolated. The authenticated
  handler has no legacy Runner routes. OAuth/session issuance remains a trusted
  internal boundary: this increment adds no public issue-token endpoint or CLI
  bypass. Runtime production remains disabled until OAuth and mTLS are ready.

## Batches and acceptance

1. Session primitives and additive persistence: malformed tokens, hash-only
   storage, expiry/revocation/suspension, FK constraints and audit atomicity.
2. Scoped run and membership transactions: cross-project denial, inherited
   grants, immediate revocation, protected environment restrictions, no self
   grants, old unowned Run exclusion and rollback when audit fails.
3. HTTP wiring: anonymous requests fail, CSRF/origin mismatch denies mutations,
   read-only role cannot create, logout revokes, browser/Runner credentials do
   not cross boundaries. Strict JSON framing and bounded error responses.
4. PostgreSQL integration/race tests, frontend regression/build, update API docs
   and development evidence, commit coherent green batches. No push or live DB
   migration/restart is included.

References checked during implementation:
[OWASP sessions](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
and [OWASP CSRF](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html).
