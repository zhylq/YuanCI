# Native command deployment

Use `examples/pipelines/deployment.yuanci.yml` in an authorized GitHub or Gitee
repository and enable the project's webhook automation. A matching push to `main`
(including a merge into `main`) creates a deployment task. YAML `triggers` decide
the events and branches; project event switches are the fallback for older YAML
without triggers.

YuanCI dispatches at most one deployment for each repository commit and environment.
Deployments for the same repository and environment execute in FIFO order. Each
Runner step runs only the commands you wrote, at the assigned commit. Tests,
application builds, databases, and optional service containers are your choices.
There is no external publishing service dependency. Step images must provide `sh`;
YuanCI overrides their ENTRYPOINT so it can run the commands.

Configure a dedicated deployment Runner with a registration token for a deployment
pool and these environment variables:

```sh
YUANCI_RUNNER_ISOLATION_LEVEL=deployment
YUANCI_RUNNER_DEPLOYMENT_POLICY_FILE=/etc/yuanci/deployment-policy.json
```

Use a separate Runner identity/state directory or volume for this pool. Deployment
enrollment and heartbeat use protocol v3; an existing standard v2 identity cannot
be changed into a deployment identity by editing its environment variables.

Copy `deploy/deployment-policy.example.json` outside the repository workspace.
Replace the numeric provider repository ID and optional host mount paths. The
policy is strict JSON, limited to 64 KiB, 127 repositories and 16 mounts. It needs no
secret values. Install it as an administrator-owned regular read-only file
(for example root ownership and mode `0444` on Linux, in a directory without group
or world write permission). The Runner must be able to
read it; repository commands must have no way to replace it. A containerized Runner
also needs this file mounted read-only. Mount source paths must exist and be
visible at the same absolute paths inside the Runner and on its Docker host.
With `deploy/compose.runner.yml`, use an administrator-authored Compose override
to add that policy mount and any source paths the Runner needs to inspect; the
environment variable is the policy's container path.
Policy mounts are applied only to deployment command containers, never checkout,
services, or ordinary CI jobs. YAML cannot grant mounts or extend the allowlist.

The Runner advertises repository grants from this file and checks the assignment's
provider and repository ID again before checkout or Docker resource creation.
Unapproved repositories fail before execution. A deployment host trusts the
writers of every approved repository, including their deployment scripts and
access to the administrator-granted paths. Keep ordinary CI on standard Runners;
their default isolation and protocol remain unchanged.

**停止部署** cancels the local command context and requests bounded container stop,
then resource removal. The next deployment waits for explicit confirmation that
the job's containers, network and workspace volume are absent. The UI continues
polling while cleanup is pending, even when the run is already canceled. A Docker
query failure or retained resource leaves the queue closed. Completion messages
retry after transport reconnects; commands do not retry. Runner loss also does
not replay deployment commands. After unconfirmed cleanup or Runner loss, an
administrator must inspect the host; the queue stays held until cleanup is
explicitly resolved. A timer or Runner restart does not release it or replay it.

Stopping does not roll back external changes. Scripts own their remote API calls,
detached processes and application resources. YuanCI guarantees at most one
dispatch and local command execution for a deployment, not a transaction over
external systems. Deployment rerun controls and automatic retries are disabled.
