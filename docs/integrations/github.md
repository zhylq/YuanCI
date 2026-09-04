# GitHub integration

YuanCI's first SCM adapter targets GitHub's versioned REST API and currently
implements the contract needed for the Milestone 1 vertical slice:

- authenticated user lookup and repository discovery;
- raw repository file reads;
- HTTPS webhook creation with strict TLS and a shared secret;
- commit status reporting;
- creation of a branch, `.yuanci.yml` commit and pull request;
- signed push, tag and pull-request event parsing.

The adapter sends `X-GitHub-Api-Version: 2026-03-10`. Safe reads retry transient
502, 503 and 504 responses; write requests do not retry automatically because a
repeated write could create duplicate webhooks, statuses, branches or pull
requests.

Webhook payloads must include `X-Hub-Signature-256`, `X-GitHub-Delivery` and
`X-GitHub-Event`. Tag pushes are normalized to YuanCI's `tag` event, while branch
pushes remain `push` events.

## GitHub CI Alpha

The managed control plane now exposes GitHub App configuration, encrypted key
and webhook-secret storage, selected repository import, project automation,
signed delivery idempotency, immutable configuration/private checkout, Runner
execution/logs and commit-status orchestration. Setup readiness describes local
configuration; it does not prove that a real App installation works.

The [GitHub CI acceptance guide](github-e2e.md) documents the deterministic E2E
test and the operator-owned real sandbox checklist/evidence. The real GitHub
Alpha gate remains open until that evidence is complete. Use a GitHub App;
do not substitute a personal access token for its scoped installation tokens.
