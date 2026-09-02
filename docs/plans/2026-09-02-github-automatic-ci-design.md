# GitHub automatic CI design

- Status: approved
- Date: 2026-09-02
- Scope: production-grade minimum GitHub automatic CI loop
- Follow-up: logs and artifacts, remaining SCM providers, deployment workflows

## 1. Goal and boundaries

This increment connects the existing managed GitHub App, pipeline compiler,
PostgreSQL scheduler, and mTLS Docker Runner into one reliable flow:

```text
GitHub webhook
  -> authenticated inbox
  -> event orchestrator
  -> immutable pipeline run
  -> secure source checkout
  -> Docker job execution
  -> reliable GitHub commit status
```

It supports `push`, tag pushes, and pull requests whose head repository is the
same repository as the base. Events from external forks are authenticated and
audited but do not create a run and never receive repository credentials.

This increment uses the GitHub Commit Status API. The status outbox is kept
provider-neutral so that GitHub Checks can be added after the run detail and log
experience is complete.

The following are deliberately deferred:

- GitHub Checks and GitHub-side reruns;
- execution of external fork pull requests;
- Git LFS and submodules;
- GitLab, Gitea, and Gitee automatic builds;
- complete log search, caches, and artifacts;
- Docker Compose deployment, approvals, health checks, and rollback.

## 2. Architecture and event flow

GitHub sends all App events to `POST /api/v1/webhooks/github`. The handler:

1. accepts only the expected method and media type;
2. enforces a 2 MiB request limit and required GitHub headers;
3. verifies the HMAC-SHA256 signature before JSON decoding;
4. parses the event into a bounded, provider-neutral event model;
5. transactionally inserts the event into the PostgreSQL inbox;
6. returns `202 Accepted` without calling the GitHub API or compiling a
   pipeline.

The GitHub delivery ID is the external idempotency key. Repeating the same ID,
event type, and payload hash succeeds without another effect. Reusing an ID
with a different type or payload hash is a security conflict and creates an
audit event.

A background orchestrator claims inbox records with a short database lease.
Network calls and pipeline compilation happen outside a database transaction.
The orchestrator then opens a new transaction, verifies its lease, and creates
the run graph, the delivery-to-run link, and the first status outbox record
atomically. A unique run trigger key prevents duplicate runs even if the worker
repeats its final transaction.

The orchestrator always obtains `.yuanci.yml` from the immutable commit SHA in
the authenticated event. It never resolves a mutable branch head during run
creation.

## 3. Webhook authentication and GitHub App configuration

The singleton managed GitHub App configuration gains an encrypted webhook
secret, an enabled flag, a secret version, and an update timestamp. The secret
uses the existing envelope encryption service. Read APIs expose only whether a
secret is configured.

Webhook authentication requires exactly one usable delivery ID, event name,
and `X-Hub-Signature-256` value. Signature comparison is constant-time. Invalid
signatures, malformed headers, oversized input, and delivery conflicts are not
retried. Their audit entries contain safe codes and hashes, never secrets or a
complete payload.

The App permission baseline for this increment is:

- Contents: read;
- Metadata: read;
- Commit statuses: read and write;
- Pull requests: read.

Subscribed events are push, pull request, installation, and installation
repositories. Tag creation is represented by a push event whose ref starts
with `refs/tags/`.

## 4. Inbox, outbox, and state recovery

The existing `webhook_deliveries` table is extended additively. It stores:

- a normalized event document, never the full raw payload;
- `received`, `processing`, `processed`, `ignored`, or `dead_letter`;
- attempt count and next attempt time;
- lease owner and lease expiry;
- project and run links;
- a safe error code and redacted error summary;
- the existing provider, instance, delivery ID, event type, signature result,
  and payload SHA-256.

The normalized event is intentionally small and includes only repository,
installation, immutable SHA, ref, actor, pull request identity, and fork
classification needed to evaluate the trigger.

A new provider-neutral `scm_status_outbox` stores the run, repository, commit,
status context, GitHub state, target URL, description, retry schedule, lease,
and safe terminal error. Every status effect has a deterministic idempotency
key. Run terminal transitions and their final status effect are committed in
one transaction.

Workers use `FOR UPDATE SKIP LOCKED` only to claim bounded batches. They do not
hold a transaction while calling GitHub. Expired leases are recovered on server
startup and by a periodic reconciler.

Transient errors use exponential backoff with jitter and respect GitHub rate
limit headers. The processing window is 24 hours. Exhausted events move to
`dead_letter` and may be manually retried by an administrator. Permanent
security and validation failures do not retry.

An enabled project with a missing or invalid pipeline receives a visible failed
run and an `error` commit status. Disabled projects, non-matching triggers, and
external fork pull requests are recorded as `ignored` without a run.

## 5. Secure source checkout

The server creates a GitHub installation token immediately before assigning a
job that needs source. The token is restricted to the target repository and to
`contents: read`. It is never stored in PostgreSQL or in the immutable pipeline
plan.

The Runner assignment protocol gains separate source metadata and an ephemeral
credential field. The fields are carried only over the existing certificate-
authenticated mTLS stream. A Runner capability/protocol version prevents a
source job from being assigned to an older Runner that cannot enforce the
checkout contract. Existing manual jobs without source requirements remain
compatible.

The source descriptor contains the provider, normalized repository identity,
trusted HTTPS clone URL, and 40-character GitHub commit SHA. Clone URLs are
constructed from trusted configuration, not copied from webhook JSON.

After accepting the lease, the Runner:

1. creates the job workspace and isolated network;
2. starts a one-shot checkout helper without credential environment metadata;
3. sends the token to the helper through standard input;
4. disables hooks and rejects local paths, `file://`, and non-HTTPS redirects;
5. fetches and checks out only the expected immutable commit;
6. verifies that `HEAD` exactly equals the assignment SHA;
7. destroys the helper and clears credential buffers before user steps run;
8. cleans the workspace and network on success, failure, cancellation, or
   lease loss.

The token is not placed in a URL, Docker command argument, pipeline variable,
or user step environment. After checkout the Runner makes a best-effort token
revocation request; expiry remains the fallback. Checkout errors are redacted
before being logged or reported.

Git LFS and submodules are disabled in this increment because they introduce
additional remote URLs and credential propagation. They will later require an
explicit project policy.

## 6. Commit status delivery

Run creation enqueues a GitHub `pending` status. A run terminal transition
enqueues exactly one of:

- `success` for a successful run;
- `failure` for a user build failure;
- `error` for configuration, SCM, Runner infrastructure, or internal failure.

The status context is stable and the target URL points to the YuanCI run detail
page. Status delivery uses a fresh installation token and is asynchronous.
Authentication failure, rate limiting, server errors, and timeouts are
classified separately so that user-facing diagnostics are actionable without
exposing credentials.

## 7. HTTP API and authorization

The increment adds or extends these endpoints:

- `POST /api/v1/webhooks/github`;
- `GET/POST /api/v1/integrations/github`;
- `POST /api/v1/integrations/github/verify`;
- `GET/PUT /api/v1/projects/{projectID}/automation`;
- `POST /api/v1/projects/{projectID}/pipeline/validate`;
- `GET /api/v1/runs/{runID}`;
- `GET /api/v1/admin/webhook-deliveries`;
- `POST /api/v1/admin/webhook-deliveries/{deliveryID}/retry`;
- `GET /api/v1/admin/scm-status-deliveries`;
- `POST /api/v1/admin/scm-status-deliveries/{deliveryID}/retry`.

Browser write endpoints retain session, CSRF, Origin, and RBAC checks. GitHub
webhooks use HMAC authentication and do not accept browser sessions as an
alternative. Diagnostic and retry endpoints require instance administrator
permission and always write an audit event.

## 8. User interface

`Settings -> GitHub integration` becomes a four-step setup guide:

1. create the GitHub App using displayed callback and webhook URLs;
2. enter the App identity, private key, and webhook secret;
3. verify identity, permissions, webhook readiness, and installation health;
4. install the App and import repositories.

Secrets never reappear after saving. The page shows configuration booleans,
last successful webhook time, and safe recent failures.

`Project -> Settings -> Automatic builds` controls enablement, pipeline path,
push/tag/same-repository-PR triggers, and cancellation of older commits. It
explains the external-fork policy and can validate the pipeline at the default
branch without creating a run. Enablement requires a valid GitHub installation
and pipeline.

The run detail page adds trigger, repository, ref, commit, actor, GitHub link,
delivery ID, config hash, checkout state, status delivery state, and categorized
failure information.

`Settings -> Webhook diagnostics` lets administrators filter deliveries, read
redacted errors, inspect status delivery, and retry dead letters. It never
displays raw webhook payloads or credentials.

The React implementation uses semantic controls, associated labels, visible
focus, at least 44 by 44 pixel action targets, textual status in addition to
color, stable loading layouts, and responsive layouts at 375, 768, 1024, and
1440 pixels. Animations stay within 150-300 ms and respect reduced motion.

## 9. Testing and acceptance

Automated coverage includes:

- HMAC success, failure, missing, malformed, and oversized inputs;
- concurrent duplicate deliveries and conflicting payload hashes;
- push, tag, same-repository PR, and external-fork PR parsing;
- trigger filtering and missing or invalid pipeline behavior;
- installation token repository and permission restrictions;
- inbox/outbox claiming, retries, expiry recovery, and dead letters;
- atomic run graph and terminal status creation;
- GitHub rate limits, authentication errors, server errors, and timeouts;
- token redaction and absence from stored plans and step environments;
- exact-SHA checkout, protocol/redirect rejection, cancellation, and cleanup;
- old Runner capability rejection for source jobs;
- settings forms, secret non-disclosure, RBAC, accessibility, and responsive
  states;
- migration upgrades, Compose build/start/health, and a mocked GitHub end-to-end
  flow.

Before the increment is complete, `go test ./...`, `go vet ./...`, frontend
tests/build, migrations, and Compose health checks must pass. A real GitHub
sandbox checklist remains a required manual acceptance step because it depends
on an operator-owned GitHub App and repository.

Acceptance requires that a valid event produces one run, 100 concurrent copies
still produce one run, external forks receive no token, all source/config reads
use the event SHA, private checkout succeeds, credentials are absent from every
persistent or user-visible channel, crashes converge to an actionable state,
and GitHub eventually receives the correct final status.

## 10. Delivery sequence

Implementation is divided into reviewable commits:

1. reliable GitHub webhook inbox;
2. GitHub event orchestration into runs;
3. secure GitHub source checkout;
4. reliable commit status delivery;
5. automation settings and diagnostics UI;
6. operator documentation and acceptance coverage.

Each commit must pass the tests proportional to its scope. Database, Runner,
frontend, and documentation changes are kept separate where practical so that
security-sensitive behavior remains reviewable.
