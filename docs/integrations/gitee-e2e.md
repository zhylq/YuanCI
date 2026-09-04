# Gitee integration acceptance (GE-01 through GE-04)

## Current status

GE-01 through GE-03 are implemented. GE-04 deterministic tests and local
verification are separate from **operator-owned real Gitee acceptance, pending**.
The account name `huiyuan1986` is not a repository selection or proof of
authorization. No Gitee credentials have been requested in chat or fabricated.
This does not close the four-SCM gate or establish production readiness.

The existing `ci.uyii.cn` GitHub managed sandbox was upgraded to the GE-03 code
on 2026-09-04, but its persisted active provider is GitHub. It remains a GitHub
sandbox. Do not replace that configuration or database with a Gitee application:
create an isolated Gitee managed stack and use its own HTTPS origin/callback.

`deploy/compose.gitee-sandbox.yml` supplies that isolated stack. It has its own
PostgreSQL, master key, Runner PKI, Runner state and localhost port. It never
shares volumes with `yuanci-sandbox`; use the matching environment and Nginx
examples only after DNS and TLS are assigned to the new hostname.

### Current cloud sandbox handoff (2026-09-04)

The isolated `yuanci-gitee-sandbox` Stack is running on the existing cloud host
with the verified `d1c2b1e` Server and Runner images. Its local readiness,
public readiness and uninitialized managed-auth status passed. It deliberately
has distinct Docker volumes, database, master key, PKI and Runner identity from
the retained legacy GitHub stack.

The operator selected the existing `ci.uyii.cn` hostname for this Gitee stack,
so no DNS or root-owned Nginx file change is pending. Configure the third-party
application with this exact callback URL:

```
https://ci.uyii.cn/api/v1/auth/gitee/callback
```

Open `https://ci.uyii.cn/setup`, use a freshly issued one-time setup code
from the isolated Stack, choose Gitee, and enter Client ID, Client Secret and
the first administrator's immutable numeric Gitee user ID. Do not provide the
secret or setup code in chat.

### Reusing an existing CI hostname

If an operator explicitly retires GitHub testing for now, the existing CI
hostname can point at this isolated Gitee stack without rewriting the legacy
GitHub database. Stop only the legacy Server and Runner, keep its volumes and
Compose directory for rollback, then apply
`deploy/compose.gitee-ci-domain.override.yml` to the Gitee Stack. It gives the
new Server the legacy Nginx upstream alias. Update `YUANCI_PUBLIC_ORIGIN` in
the Gitee Stack's protected environment to that existing HTTPS hostname and
recreate the Gitee Server before reloading Nginx. This is a deliberate traffic
cutover, not a provider conversion or database migration.

On 2026-09-04 the operator chose this takeover for `ci.uyii.cn`: its public
Nginx upstream now routes to the uninitialized Gitee stack and the legacy
GitHub Server/Runner are stopped (their database and volumes remain retained).

## Deterministic evidence

- Gitee OAuth tests and PostgreSQL fixtures exercise independent initialization,
  login, session-bound repository authorization, encrypted token rotation, refresh
  races, revocation, admin repository discovery/import and provider isolation.
- Webhook/provider tests cover password-mode authentication, freshness, semantic
  duplicate delivery, fork rejection, immutable SHA file reads and revision-bound
  pipeline validation. A Gitee signing-mode timestamp signature does not bind
  the body; YuanCI intentionally requires **password mode over HTTPS**.
- `TestGiteeWebhookCreatesSharedRun` uses PostgreSQL and the shared orchestration
  path through pending Check Run, source Job claim, scoped checkout capability,
  Job completion and final Check Run. Disabled Runner, inactive repository,
  expired lease, wrong source and revoked grant checks deny access.
- `TestCheckoutBrokerRealGitProtocol` uses a real Git HTTP backend and Git client
  to fetch depth one and check out the assigned SHA/file through the broker.
  `TestCheckoutBrokerScopeExpiryAndRevocation` checks capability boundaries.
  Git/HTTP/provider fixtures are not a real Gitee account or Docker execution.
- Existing Runner/gRPC regression tests cover credential transport and cleanup.
  Gitee uses the same executor with an ephemeral broker token, not the broad
  OAuth access token. Broker capabilities expire after two minutes, hold only
  one live Job/repository/commit, and die on server restart. Reads are capped at
  128 MiB and 45 seconds; in-flight lease checks run every second. Larger or
  slower repositories fail closed and are outside this initial implementation.

Focused Linux commands (set `YUANCI_TEST_DATABASE_URL` only to disposable test DB):

```sh
go test -race -count=1 ./internal/gitee ./internal/runnergrpc
go test -race -count=1 ./internal/store/postgres -run TestGitee
go test -race -count=1 ./internal/runner -run 'TestGiteeSource|TestValidateSource'
```

## Operator-owned real sandbox checklist

Supply only the exact private repository URL and YuanCI HTTPS URL, and confirm
whether the Gitee login and repository authorization have been completed. Enter
OAuth secrets and webhook passwords through the protected setup/settings UI;
never paste them into chat, source files, pipeline YAML or build logs.

1. Use a disposable YuanCI instance running the GE-03 server **and Runner**.
   Existing GitHub-initialized instances must not be reset or have their identity
   database edited to force a provider change. Select Gitee during setup, register
   the displayed `/api/v1/auth/gitee/callback` URL with Gitee and complete initial
   administrator verification. Confirm login never depends on GitHub.
2. Complete the separate Gitee repository authorization in repository settings
   (`user_info projects`), list and import the selected private sandbox repository.
   Record immutable numeric repository ID, not only its mutable name.
3. Commit a minimal `.yuanci.yml` and a non-secret `proof.txt` to that repository:

   ```yaml
   version: v1
   name: gitee-acceptance
   stages:
     - name: verify
       jobs:
         - name: source
           image: alpine:3.23
           steps:
             - name: verify-private-source
               commands: ['test -s proof.txt', 'cat proof.txt']
   ```

4. Set a fresh random webhook password (at least 32 bytes) in the project's
   Gitee controls. Configure the displayed receiver URL in Gitee with the same
   **password**, not signing mode, and subscribe to push/tag/merge-request events.
   Save, validate at the default branch's immutable commit, and enable automation.
5. Push a new proof marker. Record Gitee delivery time, exact SHA, Run/Job IDs,
   Runner identity and redacted logs. Confirm checkout uses that SHA even after
   the branch moves. Prove OAuth-authenticated Git HTTPS succeeds against Gitee:
   the control plane supplies Basic auth with the verified Gitee login and access
   token; its actual provider compatibility is an explicit pending acceptance.
6. Confirm Gitee Check Runs shows pending then final at the same SHA, with a
   working project Run link. Check Runs JSON/list behavior and OAuth permissions
   must be verified against the real API. Retries reconcile a per-Run name;
   Gitee documents no create idempotency key, so strict remote exactly-once is
   not claimed. Duplicate webhook delivery must not create another Run.
7. Exercise an internal PR, tag and a fork PR. Forks must create no executable
   Job or credential. Disable automation, replace the webhook password, cancel
   an active Job and revoke repository authorization; prove old trust and lease
   cannot authorize further checkout/status writes. Never dump bearer headers.
8. Record pass/fail evidence with timestamp, code SHA, private repo numeric ID,
   immutable commit, Run IDs, check IDs and redacted outcome. Only then mark the
   real Gitee gate passed. Four-SCM closure additionally requires the independent
   GitHub/GitLab/Gitea gates; Gitee unit tests do not satisfy them.

Official contract references: [Gitee OAuth](https://gitee.com/api/v5/oauth_doc),
[API v5 schema](https://gitee.com/api/v5/swagger_doc),
[webhook verification](https://help.gitee.com/webhook/how-to-verify-webhook-keys),
[webhook payload](https://help.gitee.com/webhook/gitee-webhook-push-data-format).
