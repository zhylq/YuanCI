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

Before enrolling a dedicated Runner, create its pool using an administrator database
connection (`psql "$YUANCI_DATABASE_URL" -v ON_ERROR_STOP=1`). Only the `standard`
pool is seeded automatically. This transaction creates a deployment pool or verifies
that an existing pool of the same name already has the correct type:

```sql
BEGIN;
INSERT INTO runner_pools(name, pool_type, labels)
VALUES ('deployment-production', 'deployment', '{}'::jsonb)
ON CONFLICT (name) DO NOTHING;
DO $$
BEGIN
  PERFORM 1 FROM runner_pools
    WHERE name = 'deployment-production' AND pool_type = 'deployment'
    FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'deployment-production already exists with a different pool type';
  END IF;
END $$;
COMMIT;
```

Never repurpose a standard pool. Issue a short-lived token with the existing admin
CLI, with `YUANCI_DATABASE_URL` supplied securely in the environment:

```sh
yuancictl runner-token issue -pool deployment-production \
  -file /secure/bootstrap/registration-token -ttl 10m -uses 1
```

Configure the new deployment Runner with this token, the existing mTLS root CA and
server address, and these environment variables:

```sh
YUANCI_RUNNER_ISOLATION_LEVEL=deployment
YUANCI_RUNNER_DEPLOYMENT_POLICY_FILE=/etc/yuanci/deployment-policy.json
```

Use a separate Runner identity/state directory or volume for this pool. Deployment
enrollment and heartbeat use protocol v3; an existing standard v2 identity cannot
be changed into a deployment identity by editing its environment variables.
When using `deploy/compose.runner.yml`, use a new Compose project name (for example
`docker compose -p yuanci-deployment-production ...`) so its `runner-state` volume
is separate from the standard Runner. Apply the same project name and override
files on every start, stop and upgrade.

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

For exceptional recovery, an administrator with the database credential must:

1. Identify the exact cleanup-pending job UUID, its Run and assigned Runner. Use
   the Run detail and administrator database inspection; verify the repository,
   commit and environment before operating on the host.
2. Stop that Runner process/service and prevent it from restarting during recovery.
   For Compose, stop the `runner` service using its original project name and all
   original override files. Confirm the process is stopped before inspecting Docker.
3. On that Runner's actual Docker daemon, stop/remove the exact job's retained
   containers, network and workspace volume. With the job UUID's hyphens removed,
   names are `yuanci-JOB-checkout`, `yuanci-JOB-STEP_INDEX`,
   `yuanci-JOB-service-SERVICE_INDEX`, `yuanci-network-JOB` and
   `yuanci-workspace-JOB`. Inspect all configured step and service indices from the
   immutable job specification. Query the daemon successfully and verify every
   named resource is absent; a failed query is not proof of absence.
4. Independently verify user-authored remote commands, detached processes and
   external operations have stopped. Docker cleanup alone cannot prove this.
   Application resources intentionally created by a script may remain; verify
   that no old deployment operation can still modify the target.
5. Record the verification evidence without passwords or tokens, then execute:

   ```sh
   yuancictl deployment confirm-stopped -job EXACT_JOB_UUID -verified \
     -reason 'Runner stopped; exact Docker resources absent; remote operation terminated; incident ABC'
   ```

`YUANCI_DATABASE_URL` must be set securely to an administrator database connection.
`-verified` is an explicit operator attestation of steps 2–4. The reason must be
nonempty and at most 1024 UTF-8 bytes. The CLI performs no cleanup or verification
itself. It rejects live Runs/jobs, ordinary CI and jobs that never started. Once the
Run and started job are terminal, it records cleanup confirmation and a
`deployment.cleanup_confirmed` audit with job, Run, Runner, reason and database
operator identity in one transaction. Audit failure rolls back the confirmation.
An acknowledged confirmation is idempotent and produces no additional audit.
The environment releases only after every job is terminal and every started job
has confirmed cleanup. Run/job results, terminal timestamps, deployment identity
and execution token remain intact. The command never retries or redispatches the
old commit. Restart the dedicated Runner only after recovery is resolved.

Stopping does not roll back external changes. Scripts own their remote API calls,
detached processes and application resources. YuanCI guarantees at most one
dispatch and local command execution for a deployment, not a transaction over
external systems. Deployment rerun controls and automatic retries are disabled.
