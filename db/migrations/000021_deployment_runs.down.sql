-- Once used, the ledger and execution confirmations are permanent safety data.
-- Use a forward migration to change their representation without losing identity.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM deployment_runs) THEN
        RAISE EXCEPTION 'deployment registrations exist; preserve the ledger and execution tracking with a forward migration';
    END IF;
END $$;
DROP INDEX jobs_deployment_cleanup_idx;
ALTER TABLE jobs DROP COLUMN execution_token_hash, DROP COLUMN execution_finished_at;
DROP TABLE deployment_runs;
ALTER TABLE repository_automation_settings ADD CONSTRAINT repository_automation_legacy_events_check
    CHECK (NOT enabled OR trigger_push OR trigger_tag OR trigger_pull_request) NOT VALID;
