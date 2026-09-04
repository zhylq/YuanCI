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

Status on 2026-09-04: **LOGIN VERIFIED / AWAITING APP REPOSITORY INTEGRATION**.
The protected sandbox is available at `https://ci.uyii.cn`, with its dedicated
Runner online. The operator completed GitHub login: auth status now reports
configured/initialized managed mode. GitHub user is `zhylq` (ID `55230820`).
The operator supplied `zhylq/yuanci-test` (repository ID `1356634073`), currently
public. Its initial fixture commit is
`24f56ef8aa28193870e313d04ad1b4bf8c4819e3`, containing the pipeline below,
`proof.txt` and a short README. The pipeline compiled with configuration digest
`d6fdc3cfab02bc5a13c8ff000175a48b969b8de838f51eff37ec3389afd94387`.
No App private-key configuration or repository import exists in the sandbox
yet. Real webhook, execution and status evidence is still pending, and private
checkout acceptance requires a private repository. Deployment readiness and
repository Actions results do not satisfy the real acceptance gate.

### Cloud deployment checkpoint

- Dedicated Compose project `yuanci-sandbox`, under
  `/home/deploy/yuanci-sandbox`, uses independent database, master-key, PKI and
  Runner-state volumes. Runtime code is b436ab0. Server image ID:
  `sha256:c7e1f5c1229a06a119e4bd78d2543354a63cd478bb9c582d5f4321aef6fd1b81`;
  Runner image ID:
  `sha256:371ad435b54caa7efcd3ccbd72d1cf06291ca74af22fa495fe3a8aa4aa84afdf`.
- PostgreSQL 17 Alpine failed initialization with file-write EPERM on this
  Linux 3.10 host. PostgreSQL 17 Bookworm initialized successfully with the
  existing security restrictions; no privileged mode or seccomp bypass was
  introduced. Database and Runner gRPC ports are not published to the Internet.
- Existing Nginx has a separate `ci.uyii.cn.conf` virtual host, redirects HTTP
  to HTTPS and forwards through `yuanci-sandbox-edge` to the authenticated
  server. Other virtual hosts were preserved. This site's access/error logs
  are disabled so OAuth query strings and setup material are not logged there.
  After recreating the existing Nginx container, reconnect it to this external
  network before loading this site's configuration.
- The existing certificate covers `*.uyii.cn` and currently expires on
  2026-09-19. Its existing renewal mechanism was not modified or verified.
- Focused checks: Nginx configuration before/after addition, trusted HTTPS,
  health/readiness 200, setup page 200, HTTP-to-HTTPS 301, managed/unconfigured
  auth status, unauthenticated project API 401 and online Runner all passed.
  The setup code and master-key backup are kept in restricted operator files,
  never in this repository. Codes expire after 15 minutes; an authorized
  operator can issue a replacement with `yuancictl setup-code` in the server.
- First operator OAuth verification returned 502. The server could reach
  `api.github.com`, but connections to the locally resolved `github.com`
  address `20.205.243.166` repeatedly timed out. Alternate public DNS answers
  `140.82.114.4` and `140.82.121.3` responded over verified HTTPS. A temporary
  `extra_hosts` entry for `github.com:140.82.114.4` was applied only to the
  sandbox server; the original hostname, TLS verification and host DNS remain
  unchanged. The prior Compose file is saved as `compose.before-github-route.yml`.
  GitHub login-page/API probes from the container and site readiness passed
  after restart. This address is not a permanent DNS guarantee: remove the
  override when normal resolution is reachable, or revalidate if it changes.
  The consumed OAuth flow cannot be replayed; operator authorization must be
  started again before login can be marked passed.
- Later reauthentication returned 502 again. The temporary mapped address
  became unreachable. Current public DNS answers in several regions also
  alternated between verified HTTPS responses and TCP timeouts; even the
  ordinary resolver route failed a consecutive-check attempt. Removed the
  stale `extra_hosts` override, restored normal DNS and retained
  `compose.before-remove-stale-github-route.yml` as the deployment backup.
  Nginx validation, site readiness, initialized managed auth and online Runner
  checks pass. Reliable GitHub OAuth egress is still an operator/network
  prerequisite; no durable connectivity recovery is claimed. Credentials,
  TLS checks and the ten-minute sensitive-operation reauthentication rule
  were not changed.

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
| GitHub administrator | `zhylq`, numeric ID `55230820` (operator-confirmed; public API verified) |
| Protected origin / deployed SHA / image digests | `https://ci.uyii.cn`; b436ab0; image IDs above |
| Exact private repository / repository ID | `zhylq/yuanci-test`, `1356634073`; currently public, private checkout acceptance pending |
| App ID / installation ID / webhook version | Pending |
| Runner ID / pool / online and Docker readiness | `e991d33c-bd64-4a0c-b250-fce2a6037e87`; `standard`; online; actual sandbox Job execution pending |
| Authentication and project isolation | Operator login completed; managed/configured/initialized true; unauthenticated API denied; cross-project check pending |
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
