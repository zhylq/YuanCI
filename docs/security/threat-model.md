# YuanCI threat model

## Protected assets

- SCM OAuth tokens and repository clone credentials
- Project, environment, and registry secrets
- Runner certificates and one-time registration tokens
- Source code, build logs, caches, and artifacts
- Deployment targets and production approvals
- Audit integrity and pipeline execution history

## Trust boundaries

1. Browser to control plane
2. Git provider to webhook receiver
3. Control plane to PostgreSQL and object storage
4. Runner to control plane
5. Runner daemon to task containers
6. Deployment runner to deployment targets

## Required controls

- Authenticate every state-changing endpoint and authorize the concrete resource.
- Use CSRF protection for cookie-authenticated browser actions and SameSite cookies.
- Encrypt stored credentials using per-record data keys protected by an instance master key.
- Verify webhook signatures before parsing provider events and deduplicate delivery identifiers.
- Issue short-lived, task-scoped runner credentials; never send all project secrets to a runner.
- Deny privileged containers, host namespaces, host paths, and Docker socket mounts by default.
- Route elevated and deployment jobs only to separately administered runner pools.
- Redact secret values before log persistence and cap request, log, artifact, and archive sizes.
- Reject unsafe archive paths, private-network SCM URLs by default, and unapproved redirect targets.
- Record append-only audit events for identity, authorization, secret, runner, and deployment changes.

## Known v1 limitations

Container isolation is not a security boundary against a malicious kernel exploit. The v1 threat model therefore requires dedicated runner hosts for production and assumes trusted internal repositories. Public fork builds and mutually untrusted SaaS tenants require an ephemeral VM executor and are outside v1.
