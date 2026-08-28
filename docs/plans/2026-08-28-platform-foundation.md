# Platform foundation execution plan

This is an implementation breakdown of the approved 2026-08-26 design, not a
new product scope. The user authorized incremental implementation, tests and
Git commits on 2026-08-28. No push or production deployment is included.

## Batch 1: reproducible validation

1. Establish a development checklist and honest release acceptance ledger.
2. Add PostgreSQL integration tests isolated in newly created test databases.
   Never truncate, drop or migrate the developer's Quickstart database.
3. Add a GitHub Actions workflow with read-only repository permissions and
   pinned Actions commits. The workflow builds the console before Go tests,
   runs Go's race detector on Linux and executes database integration tests.
4. Record local test evidence separately from hosted CI results.

## Batch 2: transaction reliability

Test before fixing: malformed plan creation must be atomic, concurrent claims
must be unique, concurrent completions must unblock a join and finish the Run,
and a store reopen must preserve records. Expired/wrong leases must not mutate
state. Canceled jobs must not be reported as successful Runs.

Use consistent per-Run locking before Job locks for claims and completions to
avoid parent/child lock inversion. Different Runs remain independently
schedulable. Keep SKIP LOCKED queue consumers; do not implement distributed
exactly-once execution or automatic deployment replay in this batch.

## Batch 3: authorization foundation

Implement an explicit, default-deny permission evaluator over a trusted
instance/organization/project/environment resource ancestry. Unknown actions,
unknown roles, suspended users, malformed ancestry and cross-scope grants deny.
Viewer, developer, maintainer, administrator, deployer and approver roles have
explicit action allowlists. Protected environment deploy/approve permissions
require an exact environment grant; a broad parent grant must not imply them.

This package does not constitute an authenticated product. OAuth identity,
persisted memberships, HTTP enforcement and transactional approval separation
remain separate exit gates. Never accept resource ancestry or role bindings
from a browser as authoritative.

## Exit evidence

Each code batch must include regression tests, local verification output noted
in the development log, a reviewed staged diff and a separate conventional Git
commit. Tests requiring absent credentials or external audit remain pending.

## Next batches (not completed by this document)

OAuth/session/CSRF + membership persistence + audit; Runner mTLS/lease recovery;
GitHub App/webhook idempotency/checkout/status; complete CI console and editor;
remaining SCM adapters; artifacts/cache/matrix; protected Compose deployments;
backup/restore/upgrade/soak/security review.
