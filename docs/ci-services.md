# Job service containers (CI-01)

Declare services under a Job to run dependencies on that Job's private Docker
bridge network. Steps connect using the service name and container port; no
host ports, host directories, Docker socket or build workspace are exposed to
services. Different Jobs receive different containers and networks.

```yaml
version: v1
name: database-test
stages:
  - name: test
    jobs:
      - name: sql
        image: postgres:17-alpine
        timeout: 2m
        services:
          - name: db
            image: postgres:17-alpine
            environment:
              POSTGRES_USER: test
              POSTGRES_PASSWORD: disposable-test-only
        steps:
          - name: query
            commands:
              - 'i=0; until pg_isready -h db -U test; do i=$((i+1)); test $i -lt 30; sleep 1; done'
              - "PGPASSWORD=disposable-test-only psql -h db -U test -d test -c 'select 1'"
```

The password above is only for the disposable Job database. Protected Job
Secret storage and delivery remain SEC-01 through SEC-05; do not put real
credentials in YAML or service environment values.

Services are created and started after checkout, before user steps. All services
start before readiness is checked, allowing dependencies between services. If
an image defines a Docker HEALTHCHECK, it must become healthy. A stopped or
unhealthy service fails the Job without running steps. An image without a
HEALTHCHECK is considered started when Docker reports it running; steps must
perform application readiness checks, as in the PostgreSQL example above.
The shared readiness deadline is 60 seconds and is also bounded by the Job
deadline. Cancellation or lease loss interrupts both readiness and execution.

Service names follow Pipeline v1 name syntax and must be unique within the Job,
ignoring case. There are at most 16 services and 128 environment variables per
service. Environment names use shell identifier syntax; names are bounded to
128 bytes and values to 32 KiB, with NUL bytes forbidden. Images must be
non-empty references, not CLI options. The compiler and Runner both validate
these rules, including previously persisted plans.

Services use their image's default command and writable root filesystem so
database initialization works. They drop all Linux capabilities except CHOWN,
DAC_OVERRIDE, FOWNER, SETUID and SETGID for ownership initialization and switching
to the image's service user. They cannot acquire new privileges. Each service
uses the Job's CPU, memory and PID limits (default 256 PIDs); limits are per
container, not an aggregate Job budget. Service output is not stored in Docker
logs; readiness failures produce bounded diagnostics in the first step's Job
log without recording healthcheck output or service environment values.

After success, startup failure, step failure or cancellation, cleanup removes
service containers and their anonymous image volumes before removing the Job
network and build workspace. No global Docker prune is used. A Runner process
or host crash is outside this cleanup guarantee and remains an operations fault
qualification concern.

## Verification

Focused regression tests use subprocess fixtures for readiness transitions,
partial startup, invalid state, bounded inspection, cancellation, timeouts and
cleanup. Run the affected packages with `go test ./internal/runner
./internal/pipeline` and `go vet ./internal/runner ./internal/pipeline`.

For real Docker verification, use an isolated local/test daemon and run:

```sh
YUANCI_TEST_DOCKER=1 go test -count=1 -timeout=240s ./internal/runner -run '^TestDockerServicesIntegration$'
```

This explicitly enabled test builds a temporary HTTP service image from
official Alpine, checks image HEALTHCHECK readiness and network-alias access,
runs a PostgreSQL query, verifies unhealthy-service refusal and active-step
cancellation, and verifies container/network/workspace/anonymous-volume cleanup.
It removes only resources named for its own test Jobs and its temporary image.
An ordinary test run without this flag skips actual Docker verification.
