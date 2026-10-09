# Job Commands Shorthand Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development for implementation and independent specification/quality reviews.

**Goal:** Accept Job-level commands or explicit steps without changing execution semantics.

**Architecture:** Extend the YAML Job type, validate mutually exclusive forms, then normalize shorthand into the existing compiled steps before hashing. Preserve all downstream interfaces and resource defaults.

**Tech Stack:** Go, yaml.v3, existing Go test and CLI fixtures.

### Compiler and examples

Files: `internal/pipeline/types.go`, `internal/pipeline/compiler.go`, new `internal/pipeline/commands_test.go`, `examples/pipelines/deployment.yuanci.yml`, `docs/command-deployment.md`.

- [x] Add meaningful regressions for shorthand, normalization/hash equivalence, both forms, empty/missing execution, strict decoding and deployment metadata.

Use this fixture:
```yaml
version: v1
name: simple
triggers: [{event: push, branches: [main]}]
deployment: {environment: production}
stages:
  - name: deploy
    jobs:
      - name: release
        image: alpine:3.21
        commands: ["printf 'hello\\n'"]
```
Compare its compiled Plan and digest with the same Job using:
```yaml
        steps:
          - name: commands
            commands: ["printf 'hello\\n'"]
```
Assert one compiled step, unchanged image/environment/resources/deployment,
default timeout and Runner requirements; compiled JSON has no Job-level commands.
Reject commands+steps, including either non-null empty list; reject commands
without an image, empty commands and neither form. A null form is omitted.

- [x] Run new tests before implementation and record their expected failure.

Run `.tools/go/bin/go.exe test -count=1 -run Commands ./internal/pipeline`.

- [x] Add the raw Job field and minimal normalization.

```go
Commands []string `yaml:"commands,omitempty" json:"commands,omitempty"`
```
Validate effective steps without mutating a caller's Pipeline. After validation,
Compile translates shorthand into a Step named `commands`, clears raw commands,
and uses that normalized value for both canonical hashing and the Plan. Continue
to reject unknown YAML fields and preserve supported executable aliases/merges.

- [x] Run affected pipeline/orchestration/run/Runner/HTTP API packages and CLI validation; add a minimal direct-command example without mandatory resources.
- [x] Update docs describing when explicit steps are useful and the two forms' exclusivity. Commit only implementation-owned paths.
- [x] Obtain fresh spec then quality review, repair any concrete findings, and push the existing authorized feature branch/PR.

### Server delivery

- [x] Build the reviewed Server/CLI with cached dependencies/runtime, verify direct-command CLI validation in the real image and existing steps backward compatibility.
- [x] Upgrade only the existing CI Server using a new image override, preserving database/keys/Runner identities and the already-disabled legacy timer. Do not change live YuanPlan YAML or push another application commit.
- [x] Verify readiness and both Runner protocols online; record evidence and complete delivery.

### Delivery evidence — 2026-10-09

- Implementation `cabb548c97dda542d926cda90786d15b71cbd257`; fresh specification and quality reviews approved.
- Fail-first shorthand regression was rejected as an unknown Job field before implementation. Affected pipeline, GitHub/Gitee orchestration, run, Runner/runnergrpc and HTTP API tests passed afterward with the checkout fixture's `core.autocrlf=false` setting.
- Actual Linux Server/CLI image validated both the direct-command example and the unchanged YuanPlan explicit-step YAML. No application commit or production deployment was triggered for this syntax change.
- Delivered image `yuanci-quickstart-server:commands-cabb548`, ID `sha256:420263165b1e090126d4e141c164249fb84d1fe5b3cef7fb4081e4821f011eb4`, through a checksummed archive using cached runtime and dependencies.
- Existing CI Server alone updated with `compose.job-commands.override.yml`. Future Compose operations in `/home/deploy/yuanci-gitee-sandbox` must include this additional override after the existing four Compose files to retain the new Server version.
- Server readiness passed; standard protocol 2 and deployment protocol 3 Runners online with fresh heartbeats. Legacy timer remains disabled/inactive; existing YuanPlan image remains healthy.
- Operational archive and guarded upgrade script: `/home/deploy/yuanci-job-commands-20261009/`. The script checks no active CI runs, validates archive/image identity, updates only Server and restores the prior image if readiness fails.
