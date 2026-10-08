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
- [ ] Add additive migration for unique repository/environment/SHA identity and execution cleanup tracking.
- [ ] Compile optional deployment metadata; disable deployment retries and reruns.
- [ ] Register/reuse deployment in Run creation under transaction locks.
- [ ] Gate Runner claims by environment FIFO and serialize deployment jobs.
- [ ] Verify concurrent duplicate delivery, distinct commits, independent environments and ordinary CI using disposable PostgreSQL.

### Cancellation and execution
- [ ] Retain deployment gate until Runner reports actual execution cleanup; do not replay lost execution.
- [ ] Add administrator-controlled deployment Runner access using the existing commands executor.
- [ ] Make Run detail show stop deployment and suppress duplicate deployment rerun actions.
- [ ] Verify stop, lost-lease cleanup, policy denial and commands-only Docker execution.

### Delivery
- [ ] Update schema, examples, operational docs and development log.
- [ ] Run relevant backend/frontend checks and Linux race checks.
- [ ] Commit and push authorized changes; prepare verified server update and migrate the sample only after replacement verification.
