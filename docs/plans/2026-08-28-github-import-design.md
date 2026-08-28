# GitHub installation discovery/import — approved plan

The user approved administrator-managed App setup, verified installation ownership,
repository selection and idempotent import. This is not automatic CI or deployment.
Brainstorming is complete; writing-plans is unavailable, so this file is the plan.

## Security and workflow

- Enable only in managed authenticated preview (existing master key). Preserve
  file login/evaluation behavior; show explanatory UI there, not editable secrets.
- Reuse the active login App's Client ID/Secret. Instance admins configure App ID
  and RSA private key, verified by GitHub GET /app before encrypted persistence.
  Bind credentials/flows/proofs to immutable login and App revisions. Replacement
  uses expected revision and invalidates prior discovery authorization.
- Link to the server-verified App slug's installation page. Setup URL returns to
  the settings page only; never trust its installation_id or perform a write there.
  A separate administrator-triggered OAuth/PKCE flow verifies the currently bound
  GitHub identity and discovers installations via the user's access token.
- Flow has a one-use state/nonce, five-minute lifetime and originating browser
  session binding. The callback never logs in or grants privileges. User tokens
  are envelope-encrypted for at most ten minutes, bound to the same session and
  revisions, never returned to the browser; no refresh token is stored.
- Discover using the intersection of user and App permissions. Check installation
  App/account/suspension via App JWT, and require GitHub repository admin permission
  in addition to YuanCI instance admin before sharing/importing a repository.
  Fixed GitHub endpoints, bounded responses/pages, no redirects, safe errors and
  rate-limit handling. Only metadata permissions are needed in this batch.
- Recheck remote access on import and local session, live admin permission,
  revision and proof expiry when committing. External I/O never holds DB locks.
  One GitHub account maps to a dedicated YuanCI organization keyed by stable ID;
  never merge an existing organization by name. Duplicate repository import is
  idempotent; conflicts never move projects or grant membership. Import + audit
  are atomic. Existing inactive projects stay inactive.
- Connection status means metadata verified at import, not webhook/CI readiness.
  No build/deploy action, installation token background automation or automatic
  permission synchronization this batch. Import failures never silently activate CI.

## Sequence and tests

1. Implement fixed-endpoint App/OAuth client, safe JWT/private-key validation,
   domain service, additive DB migration, transaction ports and regression tests.
2. Add managed-only REST handlers with CSRF/Origin, strict callback/session binding,
   setup instructions, private-key form, authorization/installations/repo picker.
3. Test forged/replayed callback, wrong account/session/App/installation, revoked
   local/remote rights, stale revisions, expiry, duplicate import, audit rollback,
   timeout/rate-limit/redirect/body limits and no secret exposure.
4. Update OpenAPI/deployment instructions, run real PostgreSQL + Linux race/vet,
   frontend tests/lint/build and appropriate browser/container smoke. Commit green
   increments locally; no push, real credentials or live Quickstart mutations.

Official references: GitHub [setup URL warning](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-setup-url),
[installation APIs](https://docs.github.com/en/rest/apps/installations),
[App JWT](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app).
