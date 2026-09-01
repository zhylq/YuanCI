# Runner PKI initialization (development milestone)

> The Server and Runner mTLS channel is implemented and tested. Compose examples
> have not been migrated yet, so do not replace a working Quickstart Runner
> configuration with these files until the deployment batch is complete.

Build `yuancictl`, then create a brand-new output directory. Repeat
`-server-name` for every DNS name or IP address that Runners will use to reach
the dedicated gRPC listener:

```sh
go build -o yuancictl ./cmd/yuancictl
./yuancictl runner-pki init \
  -dir ./yuanci-runner-pki \
  -server-name yuanci-server \
  -server-name ci.example.internal \
  -server-name 127.0.0.1
```

The command refuses an existing target and never prints a private key. It
creates two intentionally separate directories:

```text
yuanci-runner-pki/
├── offline-root/
│   ├── root-key.pem
│   └── root-cert.pem
└── server/
    ├── intermediate-key.pem
    ├── intermediate-cert.pem
    ├── root-cert.pem
    ├── server-key.pem
    ├── server-chain.pem
    └── manifest.json
```

Back up `offline-root` securely, verify its printed SHA-256 fingerprint through
a separate trusted channel, then remove that directory from the Server host.
Only `server/` is intended for a read-only Server mount. Private files are
created with mode `0600` and directories with mode `0700` on Linux. The manifest
contains public fingerprints and expiry dates only.

DNS/IP SANs are explicit: URLs, ports, wildcards, underscores and empty names
are rejected. Re-running against the same output path is also rejected; inspect
and deliberately move an obsolete bundle before generating a replacement.

For a PostgreSQL-backed authenticated control plane, the dedicated gRPC listener
is enabled only when all six settings are present:

```text
YUANCI_RUNNER_GRPC_ADDR=:9443
YUANCI_RUNNER_SERVER_CERT_FILE=/run/secrets/runner-pki/server-chain.pem
YUANCI_RUNNER_SERVER_KEY_FILE=/run/secrets/runner-pki/server-key.pem
YUANCI_RUNNER_CLIENT_CA_FILE=/run/secrets/runner-pki/root-cert.pem
YUANCI_RUNNER_ISSUER_CERT_FILE=/run/secrets/runner-pki/intermediate-cert.pem
YUANCI_RUNNER_ISSUER_KEY_FILE=/run/secrets/runner-pki/intermediate-key.pem
```

The listener requires TLS 1.3. Registration is the only RPC allowed without a
client certificate; Work and certificate rotation require a CA-verified,
database-active Runner identity. Enabling this listener together with the
legacy shared Runner token is rejected during configuration loading.
