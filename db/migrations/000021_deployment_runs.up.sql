-- This ledger survives history retention: deleting its Run or repository is
-- restricted so a previously registered commit can never silently replay.
CREATE TABLE deployment_runs (
    run_id uuid PRIMARY KEY REFERENCES runs(id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE RESTRICT,
    environment text NOT NULL CHECK (environment ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$'),
    commit_sha text NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$' AND commit_sha !~ '^0+$'),
    queue_order bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    released_at timestamptz,
    UNIQUE (repository_id, environment, commit_sha)
);
CREATE INDEX deployment_runs_fifo_idx ON deployment_runs(repository_id,environment,queue_order) WHERE released_at IS NULL;
ALTER TABLE jobs ADD COLUMN execution_finished_at timestamptz,
    ADD COLUMN execution_token_hash bytea CHECK (execution_token_hash IS NULL OR octet_length(execution_token_hash)=32);
CREATE INDEX jobs_deployment_cleanup_idx ON jobs(run_id) WHERE started_at IS NOT NULL AND execution_finished_at IS NULL;

-- Explicit YAML triggers replace the legacy event switches. Drop only the old
-- aggregate event check; preserve every path and revision constraint.
DO $$
DECLARE legacy_check text;
BEGIN
    FOR legacy_check IN SELECT conname FROM pg_constraint
        WHERE conrelid='repository_automation_settings'::regclass AND contype='c'
        AND regexp_replace(pg_get_constraintdef(oid),'[[:space:]()]','','g') =
            'CHECKNOTenabledORtrigger_pushORtrigger_tagORtrigger_pull_request'
    LOOP
        EXECUTE format('ALTER TABLE repository_automation_settings DROP CONSTRAINT %I',legacy_check);
    END LOOP;
END $$;
