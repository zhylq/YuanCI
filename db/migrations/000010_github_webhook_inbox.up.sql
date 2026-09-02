ALTER TABLE github_app_configs
    ADD COLUMN encrypted_webhook_secret jsonb,
    ADD COLUMN webhook_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN webhook_secret_version bigint NOT NULL DEFAULT 0 CHECK (webhook_secret_version >= 0),
    ADD COLUMN webhook_secret_updated_at timestamptz;

ALTER TABLE webhook_deliveries
    ADD COLUMN normalized_event jsonb,
    ADD COLUMN status text NOT NULL DEFAULT 'received'
        CHECK (status IN ('received','processing','processed','ignored','dead_letter')),
    ADD COLUMN attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ADD COLUMN lease_owner uuid,
    ADD COLUMN lease_expires_at timestamptz,
    ADD COLUMN repository_id uuid REFERENCES repositories(id) ON DELETE SET NULL,
    ADD COLUMN run_id uuid REFERENCES runs(id) ON DELETE SET NULL,
    ADD COLUMN error_code text CHECK (error_code IS NULL OR length(error_code) BETWEEN 1 AND 64),
    ADD COLUMN error_summary text CHECK (error_summary IS NULL OR length(error_summary) <= 1024),
    ADD CONSTRAINT webhook_delivery_lease_pair CHECK (
        (lease_owner IS NULL AND lease_expires_at IS NULL) OR
        (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
    ),
    ADD CONSTRAINT webhook_delivery_processing_lease CHECK (
        status = 'processing' OR (lease_owner IS NULL AND lease_expires_at IS NULL)
    ),
    ADD CONSTRAINT webhook_delivery_event_object CHECK (
        normalized_event IS NULL OR jsonb_typeof(normalized_event) = 'object'
    );

UPDATE webhook_deliveries
SET status = CASE
        WHEN processed_at IS NOT NULL AND error IS NULL THEN 'processed'
        WHEN processed_at IS NOT NULL THEN 'dead_letter'
        ELSE 'received'
    END,
    error_code = CASE WHEN error IS NOT NULL THEN 'legacy_error' ELSE NULL END,
    error_summary = left(error, 1024),
    next_attempt_at = COALESCE(processed_at, received_at);

CREATE INDEX webhook_deliveries_claim_idx
    ON webhook_deliveries (next_attempt_at, received_at)
    WHERE status = 'received';

CREATE INDEX webhook_deliveries_lease_idx
    ON webhook_deliveries (lease_expires_at)
    WHERE status = 'processing';

CREATE INDEX webhook_deliveries_status_received_idx
    ON webhook_deliveries (status, received_at DESC);
