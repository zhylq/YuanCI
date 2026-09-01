-- Introduce certificate-bound Runner identities and make legacy in-flight work
-- explicit before the shared-token Runner protocol is removed. This migration
-- is additive: terminal history and all project/user data are preserved.

CREATE TABLE runner_registration_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pool_id uuid NOT NULL REFERENCES runner_pools(id),
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    expires_at timestamptz NOT NULL,
    max_uses integer NOT NULL DEFAULT 1 CHECK (max_uses BETWEEN 1 AND 256),
    used_count integer NOT NULL DEFAULT 0 CHECK (used_count >= 0 AND used_count <= max_uses),
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_used_at timestamptz,
    last_used_runner_id uuid REFERENCES runners(id),
    revoked_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK (
        (used_count = 0 AND last_used_at IS NULL AND last_used_runner_id IS NULL)
        OR (used_count > 0 AND last_used_at IS NOT NULL AND last_used_runner_id IS NOT NULL)
    )
);

CREATE INDEX runner_registration_tokens_valid_idx
    ON runner_registration_tokens (expires_at, id)
    WHERE revoked_at IS NULL;

CREATE TABLE runner_certificates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    runner_id uuid NOT NULL REFERENCES runners(id) ON DELETE CASCADE,
    serial text NOT NULL UNIQUE CHECK (serial ~ '^[0-9a-f]{16,64}$'),
    csr_fingerprint bytea NOT NULL CHECK (octet_length(csr_fingerprint) = 32),
    public_key_fingerprint bytea NOT NULL CHECK (octet_length(public_key_fingerprint) = 32),
    state text NOT NULL CHECK (state IN ('active', 'retiring', 'revoked', 'expired')),
    certificate_chain_pem bytea NOT NULL CHECK (
        octet_length(certificate_chain_pem) BETWEEN 1 AND 32768
    ),
    not_before timestamptz NOT NULL,
    not_after timestamptz NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    retire_at timestamptz,
    revoked_at timestamptz,
    revocation_reason text CHECK (
        revocation_reason IS NULL OR length(revocation_reason) BETWEEN 1 AND 256
    ),
    replaces_certificate_id uuid UNIQUE REFERENCES runner_certificates(id),
    CHECK (not_after > not_before),
    CHECK ((state = 'retiring') = (retire_at IS NOT NULL)),
    CHECK ((state = 'revoked') = (revoked_at IS NOT NULL)),
    CHECK ((state = 'revoked') = (revocation_reason IS NOT NULL)),
    CHECK (retire_at IS NULL OR retire_at <= not_after),
    CHECK (revoked_at IS NULL OR revoked_at >= not_before),
    CHECK (replaces_certificate_id IS NULL OR replaces_certificate_id <> id),
    UNIQUE (runner_id, csr_fingerprint)
);

CREATE UNIQUE INDEX runner_certificates_one_active_idx
    ON runner_certificates (runner_id) WHERE state = 'active';
CREATE INDEX runner_certificates_runner_state_idx
    ON runner_certificates (runner_id, state);
CREATE INDEX runner_certificates_expiry_idx
    ON runner_certificates (not_after) WHERE state IN ('active', 'retiring');

ALTER TABLE runners
    ADD COLUMN os text CHECK (os IS NULL OR length(os) BETWEEN 1 AND 64),
    ADD COLUMN architecture text CHECK (architecture IS NULL OR length(architecture) BETWEEN 1 AND 64),
    ADD COLUMN executor text CHECK (executor IS NULL OR length(executor) BETWEEN 1 AND 64),
    ADD COLUMN isolation_level text NOT NULL DEFAULT 'standard'
        CHECK (isolation_level IN ('standard', 'privileged', 'deployment')),
    ADD COLUMN available_disk_bytes bigint CHECK (available_disk_bytes IS NULL OR available_disk_bytes >= 0),
    ADD COLUMN protocol_version integer CHECK (protocol_version IS NULL OR protocol_version BETWEEN 1 AND 1024),
    ADD COLUMN runner_version text CHECK (runner_version IS NULL OR length(runner_version) BETWEEN 1 AND 128),
    ADD COLUMN disabled_reason text CHECK (disabled_reason IS NULL OR length(disabled_reason) BETWEEN 1 AND 256);

ALTER TABLE runners
    ADD CONSTRAINT runners_capacity_upper_bound CHECK (capacity <= 256),
    ADD CONSTRAINT runners_labels_object CHECK (jsonb_typeof(labels) = 'object');

CREATE INDEX runners_heartbeat_idx
    ON runners (status, last_seen_at) WHERE status <> 'disabled';

ALTER TABLE jobs
    ADD COLUMN accepted_at timestamptz,
    ADD COLUMN lease_renewed_at timestamptz,
    ADD COLUMN failure_reason text CHECK (
        failure_reason IS NULL OR (
            length(failure_reason) BETWEEN 1 AND 64
            AND failure_reason ~ '^[a-z][a-z0-9_]*$'
        )
    );

-- Snapshot affected Runs inside this transaction so assigned-only Runs can be
-- requeued while Runs that had begun executing are finalized as failed.
CREATE TEMPORARY TABLE yuanci_upgrade_assigned_runs ON COMMIT DROP AS
    SELECT DISTINCT run_id FROM jobs WHERE status = 'assigned';
CREATE TEMPORARY TABLE yuanci_upgrade_running_runs ON COMMIT DROP AS
    SELECT DISTINCT run_id FROM jobs WHERE status = 'running';

UPDATE jobs
SET status = 'queued',
    runner_id = NULL,
    accepted_at = NULL,
    lease_renewed_at = NULL
WHERE status = 'assigned';

UPDATE jobs
SET status = 'failed',
    failure_reason = 'runner_lost',
    finished_at = COALESCE(finished_at, clock_timestamp())
WHERE status = 'running';

-- Once an executing Job is lost, downstream work must not later become
-- claimable. Successful/terminal history remains untouched.
UPDATE jobs AS job
SET status = 'skipped',
    finished_at = COALESCE(job.finished_at, clock_timestamp())
WHERE job.run_id IN (SELECT run_id FROM yuanci_upgrade_running_runs)
  AND job.status IN ('blocked', 'queued');

UPDATE runs
SET status = 'failed',
    finished_at = COALESCE(finished_at, clock_timestamp())
WHERE id IN (SELECT run_id FROM yuanci_upgrade_running_runs);

-- Defensive consistency for legacy data that already contained a terminal
-- failure/cancellation alongside an assigned Job.
UPDATE jobs AS job
SET status = 'skipped',
    finished_at = COALESCE(job.finished_at, clock_timestamp())
WHERE job.run_id IN (SELECT run_id FROM yuanci_upgrade_assigned_runs)
  AND job.run_id NOT IN (SELECT run_id FROM yuanci_upgrade_running_runs)
  AND job.status IN ('blocked', 'queued')
  AND EXISTS (
      SELECT 1 FROM jobs AS terminal
      WHERE terminal.run_id = job.run_id
        AND terminal.status IN ('failed', 'canceled')
  );

UPDATE runs AS run
SET status = CASE
        WHEN EXISTS (
            SELECT 1 FROM jobs
            WHERE jobs.run_id = run.id AND jobs.status = 'failed'
        ) THEN 'failed'
        WHEN EXISTS (
            SELECT 1 FROM jobs
            WHERE jobs.run_id = run.id AND jobs.status = 'canceled'
        ) THEN 'canceled'
        WHEN EXISTS (
            SELECT 1 FROM jobs
            WHERE jobs.run_id = run.id
              AND jobs.status = 'succeeded'
        ) THEN 'running'
        ELSE 'queued'
    END,
    started_at = CASE
        WHEN EXISTS (
            SELECT 1 FROM jobs
            WHERE jobs.run_id = run.id
              AND jobs.status IN ('succeeded', 'failed', 'canceled')
        ) THEN run.started_at ELSE NULL
    END,
    finished_at = CASE
        WHEN EXISTS (
            SELECT 1 FROM jobs
            WHERE jobs.run_id = run.id AND jobs.status IN ('failed', 'canceled')
        ) THEN COALESCE(run.finished_at, clock_timestamp()) ELSE NULL
    END
WHERE id IN (SELECT run_id FROM yuanci_upgrade_assigned_runs)
  AND id NOT IN (SELECT run_id FROM yuanci_upgrade_running_runs);

INSERT INTO audit_events(action, resource_type, resource_id, metadata)
SELECT 'runner_protocol_upgrade_recovery', 'run', run.id::text,
       jsonb_build_object('result_status', run.status)
FROM runs AS run
WHERE run.id IN (
    SELECT run_id FROM yuanci_upgrade_assigned_runs
    UNION
    SELECT run_id FROM yuanci_upgrade_running_runs
);

-- No credential issued by the legacy protocol remains authoritative. Lease
-- material is invalid after upgrade even on already-terminal rows.
UPDATE jobs
SET lease_token_hash = NULL,
    lease_expires_at = NULL,
    lease_renewed_at = NULL;

UPDATE runners
SET status = CASE WHEN status = 'disabled' THEN 'disabled' ELSE 'offline' END,
    certificate_serial = NULL,
    last_seen_at = NULL;
