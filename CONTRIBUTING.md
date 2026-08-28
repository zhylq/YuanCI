# Development workflow

YuanCI is pre-alpha. A successful build is not a production release gate.

For every change:

1. Identify the requirement in the approved design and its exit criteria.
2. Add a failing regression test for a bug, or a contract test for new behavior.
3. Implement the smallest coherent change. Keep migrations additive and never
   edit an already-applied migration to change a deployed database.
4. Run unit tests, integration tests and builds relevant to the change.
5. Review security boundaries, errors, concurrency and cancellation paths.
6. Update documentation and `docs/development-log.md` with actual evidence.
7. Commit a coherent batch using `fix:`, `feat:`, `test:`, `ci:` or `docs:`.
   Do not mix unrelated work, commit secrets, or push without authorization.

## Local checks

Use the Go version in `go.mod` and Node.js 24. From the repository root:

```sh
npm --prefix web ci
npm --prefix web test
npm --prefix web run lint
npm --prefix web run build
go test ./...
go vet ./...
```

Linux CI additionally runs `go test -race ./...`. Windows needs a supported C
compiler for race builds; a normal Windows test pass is not a race-test pass.

PostgreSQL integration tests are opt-in via `YUANCI_TEST_DATABASE_URL`, pointing
at a dedicated, disposable PostgreSQL server with CREATEDB permission. Tests
create fresh `yuanci_test_<uuid>` databases and drop only those databases during
cleanup. Do not point this variable at production. The unset-variable skip in
ordinary `go test` is not evidence that integration tests passed.

Docker-only backend verification (no host Go toolchain required):

```sh
docker compose -p yuanci-tests -f deploy/compose.test.yml up --build --abort-on-container-exit --exit-code-from verify
docker compose -p yuanci-tests -f deploy/compose.test.yml down
```

This profile is isolated from Quickstart. Its PostgreSQL data lives in tmpfs;
stopping it discards test data only. The verification image builds the console
and Go binaries, then runs all backend tests with `-race` and `go vet` on Linux.
Console unit tests and lint still run separately as listed above and in CI.

## Delivery rules

`main` remains the primary branch. Hosted checks run on pull requests and pushes
after the workflow is actually published. For team collaboration, require
passing checks and human review before merging; repository branch protection
must be configured separately by an authorized administrator.

No phase may be labeled complete while a required scenario is skipped. The
four-provider sandbox tests, 20-job/72-hour soak, restore drill and independent
security audit are explicit release gates, not promises satisfied by unit tests.
