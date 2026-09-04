CREATE TABLE gitee_webhook_configs (
 repository_id uuid PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
 revision bigint NOT NULL CHECK (revision>0),
 encrypted_secret jsonb NOT NULL
);
CREATE TABLE gitee_automation_validations (
 repository_id uuid PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
 grant_revision uuid NOT NULL,
 webhook_revision bigint NOT NULL,
 settings_revision bigint NOT NULL CHECK (settings_revision>=0),
 pipeline_path text NOT NULL,
 commit_sha text NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
 config_sha256 text NOT NULL CHECK (config_sha256 ~ '^[0-9a-f]{64}$')
);
