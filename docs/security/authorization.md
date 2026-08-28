# Authorization policy contract

Status: **policy, persisted memberships and the protected browser handler are
tested; production runtime activation is still blocked**. Authenticated Run
queries/writes now resolve resource ownership and recheck grants transactionally.
The executable still uses the explicit evaluation API until OAuth/bootstrap are
ready. See [session integration status](sessions.md). Do not enable production.

## Resource and identity inputs

`internal/authorization.Allowed` is a pure default-deny evaluator. A trusted
server-side resolver supplies an active principal, stored role grants and the
complete `instance -> organization -> project -> environment` path down to the
target. Every ID must be nonzero and distinct; missing/reordered ancestors,
unknown actions/roles and malformed environment protection classification deny.
Do not construct authoritative inputs from browser claims or YAML.

A grant matches both its scope kind and ID. Ordinary permissions inherit down
the path, never up or sideways. Separate legitimate grants are additive; an
unrelated/unknown grant adds no permissions. There is no wildcard administrator.

## Permission matrix

V = viewer, D = developer, M = maintainer, A = admin,
DEP = deployer, APR = approver. Entries list all roles permitted by the policy.

| Action | Target | Permitted roles |
| --- | --- | --- |
| resource.read | Any of the four scopes | V, D, M, A, DEP, APR |
| run.read | Project | V, D, M, A |
| run.create / run.cancel / pipeline.write | Project | D, M, A |
| repository.manage | Project | M, A |
| secret.manage | Organization, project, environment | M, A |
| environment.manage | Environment | M, A |
| deployment.read | Environment | V, D, M, A, DEP, APR |
| deployment.create | Environment | M, A, DEP |
| deployment.approve | Environment | APR |
| members.manage / audit.read | Any of the four scopes | A |
| runner.manage / instance.manage | Instance | A |

DEP/APR grants are valid only at environment scope. Every environment must be
explicitly classified `protected` or `unprotected`; an empty value denies even
reads. Protected environment mutations (`secret.manage`, `environment.manage`,
`deployment.create`, `deployment.approve`, `members.manage`) require a grant on
that exact environment, including for administrators. Scoped reads still inherit.

There is deliberately no `secret.read` plaintext permission. Secret management
must return metadata only; job-scoped secret release is a separate capability.

## Approval separation and next integration gates

Approval always requires an exact environment APR grant and a persisted,
nonzero deployment requester different from the acting user. An additional admin
grant cannot bypass this rule. This policy check alone is **not** a transaction:
the eventual deployment service must load requester/environment protection and
membership in the approval transaction, validate deployment state and persist
approval plus audit atomically. It must prevent scope changes/role escalation
and recheck revocation rather than trust stale session claims.

Completed in the protected handler: persisted memberships/ownership, scoped Run
queries, CSRF checks, grant/revocation auditing and self-grant rejection. Before
production: implement OAuth/bootstrap and activate the protected runtime, finish
deployment approval transactions, test provider identity linking and review
membership administration/last-admin protection end to end.

Validation: 1,440 role/action/target/grant combinations plus explicit protected
environment, malformed ancestry, cross-scope, suspended-user and self-approval
cases. Statement coverage is not a security audit.
