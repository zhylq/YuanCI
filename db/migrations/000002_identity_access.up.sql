-- Additive: legacy evaluation runs keep NULL ownership and are never returned
-- by authenticated project queries. The singleton survives process restarts.
CREATE TABLE instances (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid()
);
INSERT INTO instances(singleton) VALUES (true);

CREATE TABLE environments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id uuid NOT NULL REFERENCES repositories(id),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    protected boolean NOT NULL DEFAULT true,
    UNIQUE (repository_id, name)
);

CREATE TABLE memberships (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    role text NOT NULL CHECK (role IN ('viewer','developer','maintainer','admin','deployer','approver')),
    instance_id uuid REFERENCES instances(id),
    organization_id uuid REFERENCES organizations(id),
    repository_id uuid REFERENCES repositories(id),
    environment_id uuid REFERENCES environments(id),
    CHECK (num_nonnulls(instance_id, organization_id, repository_id, environment_id) = 1),
    CHECK (role NOT IN ('deployer','approver') OR environment_id IS NOT NULL),
    UNIQUE NULLS NOT DISTINCT (user_id, role, instance_id, organization_id, repository_id, environment_id)
);
CREATE INDEX memberships_user_idx ON memberships(user_id);

CREATE TABLE browser_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX browser_sessions_expiry_idx ON browser_sessions(expires_at);
CREATE INDEX browser_sessions_user_idx ON browser_sessions(user_id);

ALTER TABLE runs ADD COLUMN repository_id uuid REFERENCES repositories(id);
ALTER TABLE runs ADD COLUMN created_by uuid REFERENCES users(id);
CREATE INDEX runs_repository_created_idx ON runs(repository_id, created_at DESC);
