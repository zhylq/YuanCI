CREATE TABLE github_app_configs (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 id uuid NOT NULL UNIQUE,
 login_config_id uuid NOT NULL REFERENCES login_configs(id),
 app_id bigint NOT NULL CHECK(app_id>0),
 client_id text NOT NULL,
 slug text NOT NULL,
 encrypted_key jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE github_import_flows (
 id uuid PRIMARY KEY,
 session_id uuid NOT NULL UNIQUE REFERENCES browser_sessions(id) ON DELETE CASCADE,
 state_hash bytea NOT NULL UNIQUE CHECK(octet_length(state_hash)=32),
 nonce_hash bytea NOT NULL CHECK(octet_length(nonce_hash)=32),
 login_id uuid NOT NULL REFERENCES login_configs(id),
 app_revision uuid NOT NULL,
 consumed boolean NOT NULL DEFAULT false,
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '5 minutes'
);
CREATE TABLE github_import_proofs (
 id uuid PRIMARY KEY,
 session_id uuid NOT NULL UNIQUE REFERENCES browser_sessions(id) ON DELETE CASCADE,
 login_id uuid NOT NULL REFERENCES login_configs(id),
 app_revision uuid NOT NULL,
 encrypted_token jsonb NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE TABLE github_accounts (
 account_id bigint PRIMARY KEY CHECK(account_id>0),
 organization_id uuid NOT NULL UNIQUE REFERENCES organizations(id)
);
CREATE TABLE github_installations (
 id bigint PRIMARY KEY CHECK(id>0),
 app_id bigint NOT NULL CHECK(app_id>0),
 account_id bigint NOT NULL REFERENCES github_accounts(account_id),
 checked_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE repositories ADD COLUMN github_installation_id bigint REFERENCES github_installations(id);
