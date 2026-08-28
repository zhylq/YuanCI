CREATE TABLE oauth_bootstrap (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    github_subject text NOT NULL CHECK (github_subject ~ '^[1-9][0-9]*$'),
    consumed_at timestamptz,
    user_id uuid REFERENCES users(id),
    CHECK ((consumed_at IS NULL) = (user_id IS NULL))
);

CREATE TABLE oauth_flows (
    state_hash bytea PRIMARY KEY CHECK (octet_length(state_hash)=32),
    browser_hash bytea NOT NULL CHECK (octet_length(browser_hash)=32),
    link_session_id uuid REFERENCES browser_sessions(id),
    completion_hash bytea UNIQUE CHECK (completion_hash IS NULL OR octet_length(completion_hash)=32),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '5 minutes'
);
CREATE INDEX oauth_flows_expiry_idx ON oauth_flows(expires_at);
