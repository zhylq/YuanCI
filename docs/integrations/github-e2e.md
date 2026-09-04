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
