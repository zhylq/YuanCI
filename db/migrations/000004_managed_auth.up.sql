CREATE TABLE auth_setup (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    code_hash bytea CHECK (octet_length(code_hash)=32),
    code_expires_at timestamptz,
    session_hash bytea CHECK (octet_length(session_hash)=32),
    session_expires_at timestamptz,
    finished_at timestamptz
);
ALTER TABLE auth_setup ADD COLUMN master_key_digest bytea CHECK (octet_length(master_key_digest)=32);
INSERT INTO auth_setup(singleton) VALUES (true);

CREATE TABLE login_configs (
    id uuid PRIMARY KEY,
    provider text NOT NULL DEFAULT 'github' CHECK (provider='github'),
    client_id text NOT NULL,
    encrypted_secret jsonb NOT NULL,
    bootstrap_subject text NOT NULL CHECK (bootstrap_subject ~ '^[1-9][0-9]*$'),
    status text NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate','active','retired')),
    expected_active uuid REFERENCES login_configs(id),
    created_by uuid REFERENCES users(id),
    setup_hash bytea CHECK (octet_length(setup_hash)=32),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '30 minutes',
    CHECK ((created_by IS NOT NULL)::int + (setup_hash IS NOT NULL)::int = 1)
);
CREATE UNIQUE INDEX login_config_single_active ON login_configs(provider) WHERE status='active';
ALTER TABLE oauth_flows ADD COLUMN config_id uuid REFERENCES login_configs(id);
ALTER TABLE oauth_flows ADD COLUMN verify_session_id uuid REFERENCES browser_sessions(id);
ALTER TABLE oauth_flows ADD COLUMN verify_setup_hash bytea CHECK (octet_length(verify_setup_hash)=32);
