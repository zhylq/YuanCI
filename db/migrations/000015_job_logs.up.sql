-- PostgreSQL is the v1 bounded log backend. A future object backend must retain
-- the same lease, ordering, idempotency, quota and retention semantics.
CREATE TABLE job_log_streams (
 job_id uuid PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 next_sequence bigint NOT NULL DEFAULT 1 CHECK (next_sequence BETWEEN 1 AND 8193),
 total_bytes bigint NOT NULL DEFAULT 0 CHECK (total_bytes BETWEEN 0 AND 16777216),
 truncated boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL
);
CREATE INDEX job_log_retention_idx ON job_log_streams(expires_at);
CREATE TABLE job_log_chunks (
 job_id uuid NOT NULL REFERENCES job_log_streams(job_id) ON DELETE CASCADE,
 sequence bigint NOT NULL CHECK (sequence BETWEEN 1 AND 8192),
 step_index integer NOT NULL CHECK (step_index BETWEEN 0 AND 1023),
 stream text NOT NULL CHECK (stream IN ('stdout','stderr')),
 data bytea NOT NULL CHECK (octet_length(data) <= 32768),
 truncated boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (job_id,sequence),
 CHECK ((truncated AND octet_length(data)=0) OR (NOT truncated AND octet_length(data)>0))
);
