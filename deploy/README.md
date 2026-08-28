# Deployment profiles

## Quickstart

Copy `.env.example` to `.env`, replace both secrets, then run:

```bash
docker compose --env-file .env -f deploy/compose.quickstart.yml up --build
```

Open `http://localhost:8080`. The quickstart runner mounts the host Docker socket and therefore shares the host's security boundary. Never use it for untrusted repositories or public pull requests.

The current quickstart also enables `YUANCI_MILESTONE0_INSECURE_API=true` because OAuth and API authorization are not implemented yet. Its port is bound to `127.0.0.1` only. Recreate existing containers to apply the binding; changing the file does not change a running container. The control-plane process fails closed without this explicit flag.

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

`compose.production.yml` documents the intended split deployment and intentionally contains no runner. During milestone 0 it fails closed because production identity and RBAC are not implemented. It must not be made operational by setting the insecure evaluation flag. Deploy `compose.runner.yml` on a dedicated Linux host only after the mTLS runner protocol is complete.

The milestone-0 HTTP runner compatibility protocol uses a high-entropy shared token. It is not the final production authentication mechanism; the checked-in Protobuf contract replaces it with one-time registration and mTLS certificates before the first production release.
