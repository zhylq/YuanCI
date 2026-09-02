# Deployment profiles

YuanCI has two deliberately different deployment shapes. Quickstart is a local
evaluation stack. Production keeps the control plane and Docker Runner on
different Linux hosts. The old HTTP polling/shared-token Runner protocol has
been removed; all Runner work now uses TLS 1.3 and certificate-bound gRPC.

## Quickstart

Copy `.env.example` to `.env`, replace the database password, then run:

```bash
docker compose --env-file .env -f deploy/compose.quickstart.yml up --build
```

Open `http://localhost:8080`. The quickstart runner mounts the host Docker socket and therefore shares the host's security boundary. Never use it for untrusted repositories or public pull requests.

The first start creates a local root/intermediate/Server PKI, issues a one-use
registration token, enrolls the Runner and removes the plaintext token. PKI and
Runner identity survive ordinary `down`/`up` cycles in named volumes. The
Quickstart volume also retains the root key so it can remain one-command; this
is not the production PKI ceremony. Do not copy this volume to production.

The current quickstart also enables `YUANCI_MILESTONE0_INSECURE_API=true`. Its
HTTP port is bound to `127.0.0.1` only and can be changed with
`YUANCI_HTTP_PORT`. Recreate existing containers to apply the binding; changing
the file does not change a running container. The control-plane process fails
closed without this explicit flag.

On Linux, set `YUANCI_DOCKER_GID` to the numeric group of
`/var/run/docker.sock`. Docker Desktop normally works with the example default.
The Server runs non-root, read-only and never receives the Docker socket.

## Isolated verification

`compose.test.yml` runs backend unit/integration tests, the Linux race detector,
and vet against a disposable PostgreSQL instance. Use a distinct project name;
see [CONTRIBUTING](../CONTRIBUTING.md). It does not use application volumes.

Local `.env*` (except the public example) and `.secrets` are excluded from the
Docker context, and the Go build stage copies explicit source directories.
Keep credentials in these excluded locations, not inside source directories.
Older builds made before this exclusion can retain local configuration in
builder caches. Do not export old caches; rotate evaluation credentials before
production use. Deleting an image tag alone is not secure cache erasure.

## Production control plane

`compose.production.yml` intentionally contains no Runner and never mounts the
Docker socket. Before starting it:

1. Copy `production.env.example` outside the repository and fill every required
   value. Pin `YUANCI_VERSION`; do not deploy `latest` as a release practice.
2. Generate the master key with `yuancictl master-key -file NEW_FILE` and keep a
   tested offline backup.
3. Complete the PKI ceremony in [the Runner guide](../docs/runner-pki.md). Move
   `offline-root/` off the Server host. The bind-mounted `server/` directory must
   be readable by container UID/GID `10001:10001`; private files must remain
   `0600`.
4. Publish HTTP through an HTTPS reverse proxy. Expose TCP 9443 only to dedicated
   Runner networks and restrict it with a firewall.

Start the control plane on its host:

```bash
docker compose --env-file /secure/yuanci/production.env \
  -f deploy/compose.production.yml up -d
```

Run `yuancictl runner-token issue` with `YUANCI_DATABASE_URL` set to the same
PostgreSQL database, transfer the new file through a secure channel, and deploy
`compose.runner.yml` on a dedicated host using `runner.env.example`. The token
directory must be owned by UID/GID `10001:10001`; the Runner deletes the token
after successful enrollment. Full commands, rotation behavior, replacement,
backup and incident response are in [the Runner guide](../docs/runner-pki.md).

These files are deployment previews, not a v1 production certification. GHCR
release images, digest/signature policy, backup/restore qualification, 72-hour
soak, webhook orchestration, secret/log delivery and external security review
are still release gates.

## Incompatible pre-alpha upgrade

`YUANCI_RUNNER_SHARED_TOKEN`, `YUANCI_RUNNER_TOKEN` and `YUANCI_SERVER_URL` are
rejected. Back up PostgreSQL and configuration first, remove the old Runner
container, deploy the mTLS Server settings, issue a one-time registration token,
and enroll a fresh Runner identity. There is no in-place conversion of the old
shared token. Rollback means restoring the matching pre-upgrade database and
application/configuration backup together; never point old binaries at a
migrated database.
