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

## Not wired into the server yet

The adapter is covered by contract tests but is not yet exposed through the
control-plane HTTP API. Before enabling it, Milestone 1 must add GitHub App
installation authentication, encrypted token and webhook-secret storage,
repository synchronization, delivery idempotency and automatic pipeline/status
orchestration. Do not use a personal access token as a production substitute.
