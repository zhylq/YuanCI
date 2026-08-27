CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name text NOT NULL,
    email text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    is_instance_admin boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS external_identities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('github', 'gitlab', 'gitea', 'gitee')),
    provider_instance text NOT NULL,
    external_id text NOT NULL,
    login text NOT NULL,
    encrypted_token bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_instance, external_id)
);

CREATE TABLE IF NOT EXISTS organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE,
    display_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS repositories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider IN ('github', 'gitlab', 'gitea', 'gitee')),
    provider_instance text NOT NULL,
    external_id text NOT NULL,
    owner text NOT NULL,
    name text NOT NULL,
    clone_url text NOT NULL,
    default_branch text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_instance, external_id)
);

CREATE TABLE IF NOT EXISTS pipeline_definitions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    name text NOT NULL,
    config_path text NOT NULL DEFAULT '.yuanci.yml',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repository_id, name)
);

CREATE TABLE IF NOT EXISTS pipeline_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id uuid REFERENCES pipeline_definitions(id) ON DELETE CASCADE,
    config_sha256 char(64) NOT NULL,
    source_yaml text NOT NULL,
    compiled_plan jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (pipeline_id, config_sha256)
);

CREATE TABLE IF NOT EXISTS runs (
    id uuid PRIMARY KEY,
    pipeline_version_id uuid REFERENCES pipeline_versions(id),
    pipeline_name text NOT NULL,
    event text NOT NULL,
    ref text,
    commit_sha text,
    status text NOT NULL CHECK (status IN ('queued', 'running', 'waiting_approval', 'succeeded', 'failed', 'canceled')),
    config_sha256 char(64) NOT NULL,
    compiled_plan jsonb NOT NULL,
    idempotency_key text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz
);

CREATE INDEX IF NOT EXISTS runs_created_at_idx ON runs (created_at DESC);
CREATE INDEX IF NOT EXISTS runs_status_idx ON runs (status, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS runs_idempotency_key_idx ON runs (idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS runner_pools (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    pool_type text NOT NULL CHECK (pool_type IN ('standard', 'privileged', 'deployment')),
    labels jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS runners (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pool_id uuid NOT NULL REFERENCES runner_pools(id),
    name text NOT NULL UNIQUE,
    status text NOT NULL CHECK (status IN ('online', 'offline', 'disabled')),
    capacity integer NOT NULL CHECK (capacity > 0),
    labels jsonb NOT NULL DEFAULT '{}'::jsonb,
    certificate_serial text,
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    runner_id uuid REFERENCES runners(id),
    stage_name text NOT NULL,
    job_name text NOT NULL,
    job_key text NOT NULL,
    dependencies text[] NOT NULL DEFAULT '{}',
    spec jsonb NOT NULL,
    status text NOT NULL CHECK (status IN ('blocked', 'queued', 'assigned', 'running', 'succeeded', 'failed', 'canceled', 'skipped')),
    attempt integer NOT NULL DEFAULT 1,
    lease_token_hash bytea,
    lease_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    UNIQUE (run_id, job_key, attempt)
);

CREATE INDEX IF NOT EXISTS jobs_queue_idx ON jobs (status, created_at) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS jobs_lease_idx ON jobs (lease_expires_at) WHERE status IN ('assigned', 'running');

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    provider_instance text NOT NULL,
    delivery_id text NOT NULL,
    event_type text NOT NULL,
    signature_valid boolean NOT NULL,
    payload_sha256 char(64) NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    error text,
    UNIQUE (provider, provider_instance, delivery_id)
);

CREATE TABLE IF NOT EXISTS secrets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id uuid REFERENCES repositories(id) ON DELETE CASCADE,
    name text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('organization', 'project', 'environment')),
    encrypted_data_key bytea NOT NULL,
    key_nonce bytea NOT NULL,
    data_nonce bytea NOT NULL,
    ciphertext bytea NOT NULL,
    protected boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_user_id uuid REFERENCES users(id),
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text,
    request_id text,
    source_ip inet,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_events_created_at_idx ON audit_events (created_at DESC);
