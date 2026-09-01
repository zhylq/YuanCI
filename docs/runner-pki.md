# Runner PKI initialization (development milestone)

> The PKI command is implemented and tested, but the Server/Runner mTLS channel
> is not wired until later Runner batches. Do not replace a working Quickstart
> configuration with these files yet.

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
