# GitHub CI Alpha acceptance

## E2E-GH-01: deterministic local evidence

`TestFakeGitHubEndToEnd` in `internal/store/postgres` exercises the signed
webhook inbox, replay rejection, immutable configuration validation, Run/DAG
creation, Runner enrollment and mTLS work stream, checkout credentials,
execution, persisted redacted logs, and pending/final commit-status delivery.
It advances the fixture branch after capturing the event SHA and checks that
the executed file still comes from that SHA. Issued token/key buffers must be
cleared and the temporary workspace removed.

The test uses the real PostgreSQL store, Git, shell, DockerExecutor command
construction and control-plane services. GitHub API calls and the Docker daemon
boundary are test doubles. It does not prove GitHub network interoperability,
actual private-repository authorization, container isolation, or a real App
installation. Those require E2E-GH-02 below.

Run the focused test in Linux with Git and a shell installed, against the
disposable database from `deploy/compose.test.yml`:

```sh
go test -race -count=1 -timeout=120s ./internal/store/postgres -run '^TestFakeGitHubEndToEnd$'
```

Set `YUANCI_TEST_DATABASE_URL` to that disposable PostgreSQL instance; without
it, database integration tests skip. Never point it at a deployment database.
The test creates and drops its own database. Synthetic fixture credentials are
not deployment credentials. Full verification runs only at the phase gate,
as specified in the atomic task plan.

## E2E-GH-02: operator-owned real GitHub App sandbox

Status on 2026-09-04: **NOT EXECUTED / BLOCKED ON SANDBOX DETAILS**.
The operator supplied `huiyuan1986`; whether this is the GitHub owner is not
confirmed. No complete repository name, protected YuanCI HTTPS origin, or
online Runner has been supplied. No real App, webhook, private checkout or
GitHub status result is claimed. Local E2E and repository Actions results do
not satisfy this gate.

### Prerequisites and setup

- Record the protected HTTPS origin, exact `owner/repo`, private repository ID,
  deployed commit/image digest, App ID/installation ID, Runner ID/pool and
  operator/date in the evidence table. Use a dedicated disposable repository
  and Runner with permission to push test commits. Do not use evaluation mode.
- Confirm `/healthz` and `/readyz` return success, and
  `/api/v1/auth/status` reports configured, initialized managed authentication.
  An unauthenticated project API request must be denied. Log in as the scoped
  operator; verify another user without project access cannot inspect Runs/logs.
- Configure the App in the protected console and install it only on the test
  repository. Set Metadata read, Contents read and Commit statuses write;
  enable the Push and Pull request subscriptions. Use the console's exact
  webhook URL (`/api/v1/webhooks/github` under the public origin), HTTPS with
  certificate verification, Active enabled and matching shared secret.
- Store App keys, OAuth credentials, webhook secret and Runner bootstrap
  credentials through the protected setup/deployment mechanism. Never add them
  to this document, shell arguments, screenshots or a Git commit. The evidence
  needs IDs and secret version numbers only.
- Import the private repository, confirm an enrolled Linux Docker Runner is
  online in the selected pool, then save automation with push and same-repo PR
  enabled. Validate the default branch configuration and enable using that
  revision. Record the validation SHA and configuration digest.

Use this small `.yuanci.yml` on the sandbox's default branch. Commit a
`proof.txt` containing a unique non-secret marker alongside it. Ensure the
Runner can pull this job image and its configured checkout image.

```yaml
version: v1
name: github-alpha-sandbox
stages:
  - name: verify
    jobs:
      - name: predecessor
        image: alpine:3.23
        steps:
          - name: proof
            commands: ["cat proof.txt"]
  - name: outcome
    depends_on: [verify]
    jobs:
      - name: result
        image: alpine:3.23
        steps:
          - name: check
            commands:
              - 'if test -f slow.flag; then sleep 60; fi'
              - 'test ! -f fail.flag'
```

### Execute and record

1. Push a new proof marker after automation is enabled. Record delivery ID,
   event SHA, YuanCI Run URL/ID and configuration digest. Confirm private
   checkout executes that marker, the live log reaches the terminal output,
   both Jobs succeed and GitHub shows pending then success for the same SHA
   with a working authenticated Run target URL. Inspect only bounded log
   excerpts needed for evidence; never collect full successful logs.
2. Advance the branch while an earlier Run is waiting. The earlier Run must
   retain its event SHA/configuration digest and execute the earlier marker.
   Redeliver its exact GitHub delivery: it must not create another Run.
3. Add `fail.flag`, push, and verify a failed Job, failed Run and matching final
   GitHub failure status. Choose failed-Job rerun: a new Run must keep the
   original source/configuration, explicitly reuse the successful predecessor
   and execute the failed Job again. Since the flag is immutable, failure is
   expected again. A full rerun must execute both Jobs with new Job IDs.
4. Replace `fail.flag` with `slow.flag`, push, wait for the result Job to start
   and cancel from the console. Confirm terminal canceled state, denied lease
   renewal and eventual Docker container/network/workspace cleanup. Record the
   actual cleanup duration and final GitHub status. Replaying cancel must be
   harmless. Remove the flag in a later commit for subsequent tests.
5. Open a same-repository PR with a proof change: confirm the PR head SHA is
   used. Where the sandbox permits forks, submit an external-fork PR and
   confirm it is ignored without checkout credentials or Jobs. If unavailable,
   record this case as pending, not passed.
6. With operator-controlled test traffic, verify a webhook with an invalid
   signature is rejected and creates no Run. During a planned sandbox secret
   replacement, record the new non-secret version, update both ends and prove
   new signed delivery succeeds while the old signature fails. Keep all secret
   material in protected tooling, never command arguments or the evidence.
7. Disable automation, push a proof change and confirm no Run is created for
   that delivery. Re-enable via validation; confirm the next valid delivery
   works. Verify an invalid configuration fails validation and cannot be
   enabled. Confirm UI errors contain no credentials and that logs, local
   Runner output and Docker inspection contain no issued installation token.
8. Remove sandbox-only changes/resources using the operator's retention policy.
   Preserve sanitized IDs, SHA/digests, outcomes and minimal failure excerpts.
   Sign off only when every required case has actual evidence; any pending or
   failed row keeps the GitHub Alpha gate open. Passing is not v1 production
   qualification.

### Evidence record

| Item | Evidence / observed result |
| --- | --- |
| Date / operator | Pending |
| GitHub owner candidate | `huiyuan1986` (unconfirmed) |
| Protected origin / deployed SHA / image digests | Pending |
| Exact private repository / repository ID | Pending |
| App ID / installation ID / webhook version | Pending |
| Runner ID / pool / online and Docker readiness | Pending |
| Authentication and project isolation | Not executed |
| Success: delivery ID / event SHA / config digest / Run URL / status | Not executed |
| Moving branch and duplicate delivery | Not executed |
| Failure and full/failed-Job reruns | Not executed |
| Cancellation, final status and measured cleanup | Not executed |
| Same-repository PR / rejected fork PR | Not executed |
| Invalid signature and secret replacement | Not executed |
| Disable/re-enable and invalid configuration | Not executed |
| Bounded credential-leak checks | Not executed |
| Cleanup / operator sign-off | Pending |
| GitHub Alpha gate | **OPEN — no real sandbox results** |
