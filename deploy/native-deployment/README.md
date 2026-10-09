# Native deployment operations

For normal project setup, follow [command deployment](../../docs/command-deployment.md).
YuanCI executes the repository's commands; application tests, builds, backup and
release operations belong to those commands.

`yuanplan/` records the reviewed one-time migration package for the existing
YuanPlan server. Its paths, repository grant and legacy service names are specific
to that installation. It is an operational example, not a default Runner policy.

The administrator package on that server is
`/home/deploy/yuanci-native-deployment-20261009`. It contains these text files and
the separately transferred `images.tar.gz` (112,778,968 bytes). The archive is
excluded from Git; `yuanplan/SHA256SUMS` pins its content along with the installer,
Compose override, policy, image identities and proposed YAML.

Run the package's `install.sh` in the administrator terminal. It backs up the
existing database/configuration to a root-protected directory, loads pinned
images, registers a separate deployment identity, and checks protocol v3 grants.
It stops the legacy timer, waits for the oneshot publisher and container to exit,
holds the application's release lock, and checks application/database health
before declaring the handoff ready. It does not push YAML or force a release.
A failed readiness check leaves the YAML migration pending.

The Server/CLI were built from `6322c0f48403dcc36b06272dceee06cbd182740d`;
the unchanged reviewed Runner was built from
`354ae6c8282498b1fcbebd73fee61510802130b8`. Dockerfiles reuse the existing verified
runtime images. The tar archive contains both complete images so installation
does not depend on downloading npm dependencies or Docker base images.

Local verification included fresh Server/PKI/Runner enrollment in isolated
Compose volumes: the deployment Runner advertised protocol v3 and the allowed
repository label. An old ordinary Runner enrolled at protocol v2, then retained
the same identity and protocol through the new Runner image upgrade. Bash
syntax, complete Compose rendering, YAML compilation, and file checksums passed.

After administrator installation and remote verification, copy the proposed
`yuanplan.yuanci.yml` to the project's `.yuanci.yml` and push it to main. It invokes
the existing user-authored release script directly; there is no isolated
application acceptance/database stage or external poller. Keep the old timer
disabled while native deployment YAML is active.
