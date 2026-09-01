# Runner mTLS identity and lease recovery — approved design

Date: 2026-09-01

Status: approved by the user. This design replaces the pre-alpha shared-token
Runner protocol. It does not claim production readiness or complete CI.

## Goals and boundaries

This increment establishes the trusted execution-plane foundation required by
later checkout, logs, secrets and CI orchestration:

- one-use, pool-scoped Runner enrollment;
- locally generated Runner private keys and mTLS identity;
- capability-aware work assignment over a bidirectional gRPC stream;
- short leases, heartbeats, cancellation and deterministic recovery;
- certificate rotation, revocation and audit;
- one-command Quickstart and a separate production Runner deployment.

It deliberately excludes Webhook-to-Run orchestration, repository checkout,
log persistence/search, artifact/cache transfer, secret release, user-facing
Runner administration, manual Job retry and the 72-hour release soak.

## Alternatives considered

### Separate gRPC/mTLS channel — selected

Use the existing versioned Protobuf direction and a dedicated TLS listener.
Registration requires authenticated Server TLS plus a one-time token. Work and
rotation require a valid client certificate. A bidirectional stream supports
heartbeats, assignment, cancellation and future logs without HTTP polling.

### REST over mTLS

This would be initially smaller, but preserves polling and creates extra request
traffic for leases, cancellation and logs. A later streaming migration would
duplicate security-sensitive work, so it is rejected.

### External SPIFFE/Vault PKI

This has stronger enterprise integration, but adds mandatory infrastructure and
operational complexity contrary to the lightweight v1 goal. Provider interfaces
may permit it later; it is not a required dependency in this increment.

## PKI and Runner identity

YuanCI uses an offline root CA and an online intermediate CA. `yuancictl
runner-pki init` creates the root, intermediate and Runner-gRPC Server certificate.
The root private key is backed up offline and is not mounted by the Server. The
Server mounts only its Server key/certificate, the online intermediate signing
key/certificate and the root trust certificate, all from read-only files with
strict permission checks.

The Runner receives the root trust certificate and a one-time registration
token out of band. It generates its own key locally, writes it with mode 0600,
and sends only a CSR. The control plane never generates, receives, returns or
logs a Runner private key. The current Protobuf response containing a private
key must be changed before it is implemented.

The first implementation accepts a secure, deliberately small key set (Ed25519
preferred; ECDSA P-256 and RSA 2048–4096 only when supported by the implementation).
Certificates have client-auth EKU, a random serial, a 24-hour default lifetime
and a URI SAN containing the immutable Runner UUID. CN and a request-body
`runner_id` are not authority.

TLS verifies the chain and Server name first. A gRPC interceptor then verifies
the URI SAN, certificate serial, expiry, certificate database state and active
Runner state. Work and rotation derive the Runner ID from that verified identity.
Registration is the only method that does not require a client certificate; it
still requires Server-authenticated TLS and a valid registration token.

Registration tokens are random, stored only as SHA-256 digests, expire quickly,
and are bound to a Runner Pool. The default maximum use count is one. Signing a
CSR does not make a usable identity by itself: token consumption, Runner record,
certificate record and audit commit atomically. A certificate from a failed
transaction is rejected because its serial is absent from the database.

## Rotation and revocation

Runner certificates default to 24 hours and rotate when six hours remain. The
Runner generates a new local key and CSR. Rotation requires the current valid
certificate and may act only on that same Runner.

To tolerate a lost response, the database retains the new certificate and CSR
public-key fingerprint. A retry with the old certificate and the same CSR returns
the existing certificate. The old certificate enters `retiring` state for at
most 15 minutes (and never past its own expiry). A successful atomic local write
switches the Runner to the new key/certificate. Multiple unrelated pending
rotations are rejected.

The Runner writes credentials through a temporary file, fsyncs, sets restrictive
permissions and atomically renames. A crash leaves either the previous complete
pair or the new complete pair. Disabled Runners and explicitly revoked serials
cannot open Work or rotate. All enrollment, rotation, disable/revoke and cleanup
events are audited.

## Work stream, matching and capacity

Runner opens a bidirectional `Work` stream and sends a heartbeat every five
seconds. Capabilities include operating system, architecture, executor, labels,
capacity, free disk and Runner version. The Server applies size/count/format
limits and records only validated fields.

The authenticated certificate determines the Runner. A body-supplied Runner ID
is removed or ignored. The Server matches only Jobs allowed by the Runner Pool
and satisfying required labels, OS, architecture, executor, disk and remaining
capacity. This increment supports the standard pool; privileged and deployment
pools remain separate policy types and receive no implicit fallback.

The Server sends no more assignments than reported capacity minus valid active
leases. A Job is transactionally bound to `runner_id`, a random lease token and
deadline before assignment. The Runner acknowledges receipt, then sends
`JobStarted`. It must not create an execution container until the Server accepts
the start transition.

Assignment delivery is at least once, but lease-token and state checks make start
and completion idempotent or explicitly rejected. A lost assignment remains
`assigned` until its short lease expires. A stale Runner cannot start it after a
new lease is issued.

## Heartbeats and short leases

Job leases default to 30 seconds, independent of the maximum Job timeout. Every
valid heartbeat renews matching active leases for another 30 seconds. Renewal
requires certificate Runner ID, stored Job `runner_id`, Job ID, lease token and
eligible state to match. The reply states the authoritative deadline or directs
the Runner to cancel.

The Runner tracks the last confirmed deadline locally. If it cannot renew before
that deadline, it cancels the Job context and removes the container/network as a
best effort. It does not continue indefinitely during a partition. A recovered
connection reports local active Jobs; the Server renews those still valid and
cancels unknown, expired, moved or terminal Jobs. Late log and completion events
are rejected after lease loss.

## Deterministic recovery

A reconciliation loop runs on every Server and scans bounded batches every five
seconds. PostgreSQL row locks, a stable lock order and `SKIP LOCKED` permit
multiple Servers without duplicate recovery. No correctness state lives only in
Server memory, so restart recovery uses the same database deadlines.

- Expired `assigned` but not started: clear Runner and lease fields and return
  the same attempt to `queued`. Since the Runner is forbidden to execute before
  an accepted start, automatic reassignment is safe.
- Expired `running`: atomically mark `failed`, set `failure_reason=runner_lost`,
  clear the lease, skip blocked/queued downstream Jobs and finalize the Run as
  failed. It is never automatically rerun because commands or deployments may
  have external side effects.

The recovery transaction locks the parent Run before the Job and graph changes,
matching completion lock order. It rechecks wall-clock expiry after waiting for
locks. A late completion with an old token cannot overwrite recovery. Explicit
user retry is a later UI/API capability and will create a new attempt.

Runner disable/revocation stops new assignment immediately. Existing active Jobs
receive cancellation when possible and otherwise follow the same deterministic
lease-expiry path; there is no silent ownership transfer.

## Data model and API changes

An additive migration introduces:

- registration tokens: digest, Pool, creator, expiry, max uses, used count and
  consumption metadata;
- Runner certificates: serial, Runner, public-key/CSR fingerprint, state,
  validity, retirement/revocation and certificate chain needed for idempotent
  rotation;
- Runner fields for OS, architecture, executor, free disk, version, heartbeat
  and disable reason;
- Job acceptance, last-renewal and machine-readable failure reason fields.

Every lease mutation includes `runner_id` in addition to the token digest and
deadline. Server APIs never return token hashes, CA private material or other
Runner credentials. Protobuf messages gain CSR, certificate-chain, heartbeat
lease tokens, explicit acknowledgements/errors and version/capability bounds.

At upgrade, existing legacy non-terminal leases cannot be trusted because they
are not certificate-bound. The migration or startup transition places unstarted
assignments back in queue and marks legacy running Jobs failed with a clear
upgrade/runner-loss reason. It does not silently continue them.

## Deployment and migration

Runner gRPC uses a separate configurable port and TLS listener rather than the
browser HTTP listener. Production exposes it only to Runner networks/firewall
sources. A reverse proxy must not synthesize client identity headers.

Quickstart receives one-shot initialization services/volumes that create an
isolated development PKI and a database-backed one-use registration package.
The Runner stores credentials in its own persistent volume. This retains one
command startup while keeping the control plane free of Docker Socket mounts.

Production deployment keeps Server and Runner on separate hosts. The operator
initializes PKI, securely transfers the root trust certificate and registration
token/package, and mounts only the required credentials. Production Runner still
mounts only its local Docker Socket. Documentation covers initial enrollment,
rotation, revoke/replace, backup, firewall, upgrade and recovery.

The pre-alpha `YUANCI_RUNNER_SHARED_TOKEN`, Bearer authentication and HTTP Runner
claim/start/complete routes are removed rather than maintained as a downgrade
path. Configuration fails clearly on obsolete settings. The change is documented
as incompatible; current Quickstart is migrated in the same increment.

## Error handling and observability

Runner-facing errors are stable gRPC statuses with safe reason codes; CA keys,
registration tokens, lease tokens, CSR bodies and upstream details never enter
logs. Structured logs include Runner/Run/Job IDs only after validation. Metrics
cover online/offline Runners, enrollment/rotation result, heartbeat age, active
leases, renewal failures and recovery outcomes.

Malformed/oversized messages, excess labels, invalid disk/capacity, version
mismatch and rapid reconnect/register attempts are bounded and rejected. Stream
send/receive loops use cancellation and bounded queues to avoid goroutine leaks
or an unbounded slow-client buffer.

## Verification gates

Tests must cover:

- PKI: wrong CA, Server name, forged URI SAN, expiry, revocation, wrong EKU,
  weak/unsupported keys, malformed/oversized CSR and rotation replay;
- registration: expiry, Pool binding and concurrent consumption with exactly one
  success; database/audit failure yields no usable identity;
- identity: no client certificate, unknown serial, disabled Runner, request-body
  spoofing, old/new rotation grace and idempotent retry;
- scheduling: capability/Pool/capacity matching and no privileged fallback;
- lease: renewal, identity/token mismatch, late start/log/complete and bounded
  heartbeat behavior;
- recovery: assigned requeue, running `runner_lost`, graph/Run finalization,
  Server restart, DB lock waits, multiple reconcilers and no partial transaction;
- Runner: network partition cancels the executor at the confirmed deadline,
  reconnect reconciliation, restart credential reuse and atomic rotation writes;
- full regression: real PostgreSQL, Go race/vet, gRPC mTLS integration and fresh
  Quickstart plus separate production Runner Compose smoke.

The increment does not satisfy the 72-hour soak, real production PKI ceremony,
independent security review or all v1 release gates. Those remain explicitly
tracked.
