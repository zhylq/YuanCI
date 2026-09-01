ALTER TABLE jobs
    ADD COLUMN required_pool_type text NOT NULL DEFAULT 'standard'
        CHECK (required_pool_type IN ('standard','privileged','deployment')),
    ADD COLUMN required_os text NOT NULL DEFAULT 'linux'
        CHECK (length(required_os) BETWEEN 1 AND 64),
    ADD COLUMN required_architecture text
        CHECK (required_architecture IS NULL OR length(required_architecture) BETWEEN 1 AND 64),
    ADD COLUMN required_executor text NOT NULL DEFAULT 'docker'
        CHECK (length(required_executor) BETWEEN 1 AND 64),
    ADD COLUMN required_labels jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(required_labels) = 'object'),
    ADD COLUMN required_disk_bytes bigint NOT NULL DEFAULT 0
        CHECK (required_disk_bytes >= 0);

CREATE INDEX jobs_runner_match_idx
    ON jobs (status, required_pool_type, required_os, required_executor, created_at)
    WHERE status = 'queued';
