# Event-driven Command Deployment Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans inline. The user has approved the design and requested implementation.

**Goal:** Run only user commands for matching branch pushes, once per commit/environment, in a serial deployment queue with an effective stop action.

**Architecture:** Extend the existing Webhook orchestration and transactional scheduling. Add deployment identity and cleanup tracking rather than another poller. Preserve standard CI execution and protect deployment access through administrator Runner policy.

**Tech Stack:** Go, PostgreSQL, Docker, mTLS Runner protocol, React/TypeScript.

### Trigger matching
- [ ] Add failing matcher/orchestrator tests for events and exact/glob branch filtering.
- [ ] Preserve triggers in compiled plans and filter before task creation; reject unsupported path filters instead of silently ignoring them.
- [ ] Add delayed-push regression and verify immutable repository commit instead of mutable branch HEAD.
- [ ] Run affected Go package tests.

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
