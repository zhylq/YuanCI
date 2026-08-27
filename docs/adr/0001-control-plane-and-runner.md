# ADR 0001: Go modular control plane with isolated runners

- Status: Accepted
- Date: 2026-08-26

## Context

YuanCI needs low idle resource consumption, simple self-hosted deployment, transactional reliability, and a strong boundary between internet-facing control-plane services and user-supplied build commands.

## Decision

Build one Go control-plane binary containing well-defined application modules. Embed the React console in its image and use PostgreSQL as the single mandatory state service. Run jobs in independent Go runner processes that initiate authenticated connections to the control plane.

Use versioned REST/OpenAPI for public APIs and Protobuf/gRPC over mTLS for the internal runner protocol. Use PostgreSQL leases and row locking for the initial scheduler rather than introducing a message broker.

## Consequences

The default installation remains small and transaction boundaries remain clear. Runners scale independently and can be placed on dedicated hosts. Module interfaces must be enforced during review because process boundaries do not enforce them. Control-plane horizontal scaling and S3-compatible shared storage can be added without changing the public model.
