ALTER TABLE runs ADD COLUMN rerun_of uuid REFERENCES runs(id);
ALTER TABLE runs ADD COLUMN rerun_mode text CHECK (rerun_mode IN ('full','failed'));
ALTER TABLE runs ADD CONSTRAINT runs_rerun_pair CHECK ((rerun_of IS NULL)=(rerun_mode IS NULL));
ALTER TABLE runs ADD CONSTRAINT runs_rerun_not_self CHECK (rerun_of IS NULL OR rerun_of<>id);
ALTER TABLE jobs ADD COLUMN reused_from_job_id uuid REFERENCES jobs(id);
ALTER TABLE jobs ADD CONSTRAINT jobs_reused_success CHECK (reused_from_job_id IS NULL OR (status='succeeded' AND reused_from_job_id<>id));
