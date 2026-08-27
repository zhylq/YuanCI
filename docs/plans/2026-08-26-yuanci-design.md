# YuanCI approved system design

Date: 2026-08-26

## Product decisions

YuanCI is an Apache-2.0 licensed, self-hosted CI/CD system for trusted internal repositories. The v1 capacity target is 50 users and 20 concurrent jobs. It supports GitHub, GitLab, Gitea, and Gitee, uses pipeline-as-code with a visual editor, and focuses continuous delivery on Docker Compose over SSH.

The system is intentionally smaller than Jenkins: it does not load arbitrary code into the control-plane process, does not require Kafka, Redis, or Elasticsearch, and does not bundle source control, artifact registries, or security scanners.

## Architecture

The control plane is a Go modular monolith with a React/TypeScript web console embedded into the server image. PostgreSQL is the only mandatory external service. Separate Go runners poll the control plane and execute jobs on dedicated hosts.

Production control planes never mount a Docker socket. Normal, privileged, and deployment workloads use separate runner pools. The quickstart Compose profile co-locates a Docker runner only for evaluation and explicitly documents its weaker isolation.

Pipeline source is stored in `.yuanci.yml`. The server parses the file into a shared AST, validates schema and semantics, evaluates policy, and compiles an immutable execution plan identified by its content hash.

## Security and reliability

External identities use Git OAuth with a disabled-by-default local emergency administrator. Authorization is enforced at instance, organization, project, and environment scope. Secrets use AES-256-GCM envelope encryption and are released only to authorized jobs.

Runner registration uses a one-time token to issue a client certificate. Runner traffic uses mTLS, short-lived task credentials, leases, and heartbeats. Webhooks are authenticated, size-limited, recorded, and deduplicated before asynchronous processing.

Run, job, and deployment transitions are transactionally persisted. Queue consumers use PostgreSQL row locking and leases. At-least-once delivery is permitted internally, but externally visible effects must use idempotency keys.

## v1 scope

The v1 pipeline model includes stages, jobs, steps, DAG dependencies, service containers, cache, artifacts, timeouts, retries, matrices, triggers, and concurrency groups. The UI includes templates, guided creation, visual DAG editing, YAML editing, validation, run details, logs, retries, and cancellation.

The v1 CD flow builds and promotes immutable images, gates protected environments, deploys Compose projects over SSH through a deployment runner, checks health, records snapshots, and restores the previous successful snapshot on rollback.

Drone configuration import, Kubernetes-native deployment, multi-tenant SaaS, in-process plugins, built-in registries, and per-job virtual machines are explicitly deferred.
