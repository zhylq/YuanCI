# GitHub automatic CI implementation plan

This plan implements the approved design in
`docs/plans/2026-09-02-github-automatic-ci-design.md`. Every task ends with its
focused tests before the next task starts. Cross-cutting verification runs at
the end of every commit.

## Slice 1: reliable webhook inbox

1. Add migration `000010_github_webhook_inbox`:
   - add encrypted webhook secret metadata to `github_app_configs`;
   - extend `webhook_deliveries` with normalized event, processing state,
     attempt schedule, worker lease, project/run links, and safe error fields;
   - add claim/recovery indexes and constraints without rewriting existing
     delivery rows.
2. Extend the integration repository and service:
   - accept an optional replacement webhook secret when saving the App;
   - encrypt it with record-specific AAD;
   - return only `webhook_secret_configured` and the canonical webhook URL.
3. Add a bounded GitHub webhook ingress service:
   - validate headers and HMAC before decoding;
   - normalize supported events and classify external forks;
   - calculate the payload digest and insert idempotently;
   - distinguish exact duplicates from delivery conflicts.
4. Mount the public webhook route outside browser session middleware while
   retaining request IDs, security headers, logging, and rate/size protection.
5. Test valid and invalid signatures, header ambiguity, body limits, event
   normalization, exact duplicates, conflicts, encryption non-disclosure, and
   concurrent PostgreSQL inserts.
6. Run `go test ./...`, `go vet ./...`, migration tests, and Compose config.

## Slice 2: event orchestration

1. Add project automation settings with safe defaults disabled.
2. Implement bounded inbox claim, renew/finalize, expiry recovery, retry, ignore,
   and dead-letter repository operations using `SKIP LOCKED`.
3. Mint a repository-scoped GitHub installation token service and fetch
   `.yuanci.yml` at the event SHA.
4. Evaluate event/ref/path policy, compile the pipeline, and atomically create
   the run graph, trigger identity, delivery link, and initial status effect.
5. Create a visible failed run for enabled-project configuration errors.
6. Add worker lifecycle wiring and graceful shutdown.
7. Test duplicate workers, crash recovery, trigger filtering, fork rejection,
   immutable config fetch, and transaction rollback.

## Slice 3: secure Runner checkout

1. Add source/credential assignment messages and Runner capability negotiation
   to the protobuf contract, then regenerate checked-in Go bindings.
2. Match source jobs only to compatible Runners.
3. Mint repository-scoped `contents:read` tokens immediately before assignment;
   never persist them with the job or plan.
4. Implement the one-shot checkout helper and exact-SHA validation.
5. Ensure credential delivery uses mTLS and standard input, not URL, Docker
   configuration, command arguments, plan JSON, or step environment.
6. Clean all helper, workspace, network, and credential state on every exit.
7. Test protocol downgrade, clone URL validation, hooks/redirect/protocol
   rejection, SHA mismatch, cancellation, lease loss, redaction, and cleanup.

## Slice 4: reliable commit status

1. Add the provider-neutral status outbox migration and repository contract.
2. Enqueue `pending` with run creation and a final effect with the run terminal
   transition in their respective transactions.
3. Implement short-lived token acquisition and GitHub Commit Status delivery.
4. Add claim leases, rate-aware retry, expiry recovery, dead letters, and
   administrative replay.
5. Test state mapping, deterministic idempotency, crash windows, rate limits,
   authentication failures, and final convergence.

## Slice 5: API and React experience

1. Extend GitHub integration settings with webhook instructions, secret
   replacement, health, and connection verification.
2. Add project automation read/update and pipeline validation APIs.
3. Add run source/status details and administrator inbox/outbox diagnostics.
4. Build the GitHub setup guide, project automatic-build settings, enhanced run
   detail, and webhook diagnostics pages.
5. Keep secrets write-only and enforce session, CSRF, Origin, RBAC, and audit on
   every browser mutation.
6. Test keyboard access, labels, focus, textual status, loading/error/empty
   states, and 375/768/1024/1440 pixel layouts.
7. Run frontend tests and production build.

## Slice 6: operational acceptance

1. Update `.env.example`, Quickstart/production Compose, upgrade notes, GitHub
   App setup guide, security guide, and troubleshooting guide.
2. Add a deterministic fake-GitHub end-to-end suite covering webhook, config
   fetch, private checkout, execution, and final status.
3. Run database upgrade from the previous repository version.
4. Run `go test ./...`, `go vet ./...`, frontend tests/build, Compose build,
   startup health checks, and credential leak scans.
5. Execute and record the operator-owned real GitHub sandbox checklist.

## Commit and review policy

Each slice is committed only after focused and repository-wide checks pass.
Generated files stay with the source change that requires them. Migrations are
forward-only in production and receive upgrade tests. No unrelated working-tree
changes are included. Each final handoff lists the commit, tests run, remaining
manual checks, and the next unimplemented slice.
