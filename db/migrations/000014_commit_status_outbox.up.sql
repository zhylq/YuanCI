CREATE TABLE commit_status_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('github', 'gitlab', 'gitea', 'gitee')),
    commit_sha char(40) NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    context text NOT NULL CHECK (length(context) BETWEEN 1 AND 100),
    commit_state text NOT NULL CHECK (commit_state IN ('pending', 'success', 'failure', 'error')),
    description text NOT NULL CHECK (length(description) BETWEEN 1 AND 140),
    target_url text CHECK (target_url IS NULL OR (length(target_url) BETWEEN 1 AND 2048)),
    deterministic_key text NOT NULL UNIQUE CHECK (length(deterministic_key) BETWEEN 1 AND 255),
    delivery_state text NOT NULL DEFAULT 'queued'
        CHECK (delivery_state IN ('queued', 'processing', 'delivered', 'dead')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 100),
    available_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    lease_owner uuid,
    lease_expires_at timestamptz,
    last_error_code text CHECK (last_error_code IS NULL OR length(last_error_code) BETWEEN 1 AND 64),
    delivered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT commit_status_outbox_expiry CHECK (expires_at > created_at),
    CONSTRAINT commit_status_outbox_lease_pair CHECK (
        (lease_owner IS NULL AND lease_expires_at IS NULL) OR
        (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
    ),
    CONSTRAINT commit_status_outbox_processing_lease CHECK (
        delivery_state = 'processing' OR (lease_owner IS NULL AND lease_expires_at IS NULL)
    ),
    CONSTRAINT commit_status_outbox_delivered_time CHECK (
        (delivery_state = 'delivered' AND delivered_at IS NOT NULL) OR
        (delivery_state <> 'delivered' AND delivered_at IS NULL)
    )
);

CREATE INDEX commit_status_outbox_claim_idx
    ON commit_status_outbox (available_at, created_at, id)
    WHERE delivery_state = 'queued';
CREATE INDEX commit_status_outbox_lease_idx
    ON commit_status_outbox (lease_expires_at)
    WHERE delivery_state = 'processing';
CREATE INDEX commit_status_outbox_run_idx
    ON commit_status_outbox (run_id, created_at);
