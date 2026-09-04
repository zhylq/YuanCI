CREATE TABLE gitee_authorizations (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 login_id uuid NOT NULL REFERENCES login_configs(id),
 subject text NOT NULL CHECK (subject ~ '^[1-9][0-9]*$'),
 revision uuid NOT NULL,
 encrypted_token jsonb,
 scope text NOT NULL,
 expires_at timestamptz NOT NULL,
 status text NOT NULL CHECK (status IN ('active','refreshing','revoked')),
 refresh_claim uuid,
 refresh_until timestamptz,
 retry_at timestamptz NOT NULL DEFAULT '-infinity',
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(user_id),
 CHECK ((status='refreshing')=(refresh_claim IS NOT NULL AND refresh_until IS NOT NULL)),
 CHECK ((status='revoked')=(encrypted_token IS NULL))
);
CREATE TABLE gitee_oauth_flows (
 id uuid PRIMARY KEY,
 session_id uuid NOT NULL REFERENCES browser_sessions(id) ON DELETE CASCADE,
 login_id uuid NOT NULL REFERENCES login_configs(id),
 state_hash bytea UNIQUE NOT NULL CHECK (octet_length(state_hash)=32),
 nonce_hash bytea NOT NULL CHECK (octet_length(nonce_hash)=32),
 consumed boolean NOT NULL DEFAULT false,
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '5 minutes'
);
CREATE INDEX gitee_oauth_flows_expiry ON gitee_oauth_flows(expires_at);
