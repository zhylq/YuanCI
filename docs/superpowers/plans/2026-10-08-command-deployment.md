# Event-driven Command Deployment Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans inline. The user has approved the design and requested implementation.

**Goal:** Run only user commands for matching branch pushes, once per commit/environment, in a serial deployment queue with an effective stop action.

**Architecture:** Extend the existing Webhook orchestration and transactional scheduling. Add deployment identity and cleanup tracking rather than another poller. Preserve standard CI execution and protect deployment access through administrator Runner policy.

**Tech Stack:** Go, PostgreSQL, Docker, mTLS Runner protocol, React/TypeScript.

### Trigger matching
- [x] Add failing matcher/orchestrator tests for events and exact branch filtering; reject unsupported globs.
- [x] Preserve triggers in compiled plans and filter before task creation; reject unsupported path filters instead of silently ignoring them.
- [x] Add delayed-push regression and verify immutable repository commit instead of mutable branch HEAD.
- [x] Run affected Go package tests.

Trigger delivery: commits `29555ae`, `8145a33`, `1a20c7a`. Independent specification and quality reviews approved. Unit packages and real disposable PostgreSQL trigger/failure-policy integrations passed. Trigger extraction is independent of executable YAML decoding; ambiguous policy and multiple documents are rejected consistently. Valid tag refs and legacy disabled-event behavior remain supported.

### Deployment persistence and FIFO queue
- [x] Add additive migration for unique repository/environment/SHA identity and execution cleanup tracking.
- [x] Compile optional deployment metadata; disable deployment retries and reruns.
- [x] Register/reuse deployment in Run creation under transaction locks.
- [x] Gate Runner claims by environment FIFO and serialize deployment jobs.
- [x] Verify concurrent duplicate delivery, distinct commits, independent environments and ordinary CI using disposable PostgreSQL.

Backend delivery: commit `f7067ef`. Independent specification and quality reviews approved. Full Go suite with disposable PostgreSQL passed (PostgreSQL package 57.087 seconds). Regressions cover a stale-snapshot cross-Runner claim race, original-token late cleanup and terminal timestamp preservation, and downgrade refusal after any registration. Existing standard Runner protocols remain unchanged; deployment requires the upcoming protocol v3 executor.

Linux verification of snapshot `07f612f`: race checks passed for the backend packages, including real disposable PostgreSQL (50.434 seconds). Runner helper subprocess tests initially exhausted deadlines because each race-instrumented child adds an exit wait; rerunning with `GORACE=atexit_sleep_ms=0` passed the Runner package (3.559 seconds) without changing assertions. `go vet ./...` passed. The later protocol/executor changes require their own final verification.

### Cancellation and execution
- [x] Retain deployment gate until Runner reports actual execution cleanup; do not replay lost execution.
- [x] Add administrator-controlled deployment Runner access using the existing commands executor.
- [x] Make Run detail show stop deployment and suppress duplicate deployment rerun actions.
- [x] Verify stop, lost-lease cleanup, policy denial and commands-only Docker execution.

Executor delivery candidate: `354ae6c`. Independent specification review approved; quality review is in progress. Full Go tests, 35 frontend tests, lint and build passed. Real Docker ENTRYPOINT/commands/resource-cleanup verification passed (2.900 seconds). Complete Linux race tests and disposable PostgreSQL integrations passed (PostgreSQL 41.412 seconds), followed by `go vet ./...`. Completion acknowledgement is limited to protocol v3; older standard Runner binaries remain compatible.

### Delivery
- [ ] Update schema, examples, operational docs and development log.
- [ ] Run relevant backend/frontend checks and Linux race checks.
- [ ] Commit and push authorized changes; prepare verified server update and migrate the sample only after replacement verification.
