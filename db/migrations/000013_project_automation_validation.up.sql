CREATE TABLE repository_automation_validations (
    repository_id uuid PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    settings_revision bigint NOT NULL CHECK (settings_revision >= 0),
    pipeline_path text NOT NULL,
    app_revision uuid NOT NULL REFERENCES github_app_configs(id) ON DELETE CASCADE,
    commit_sha text NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    config_sha256 text NOT NULL CHECK (config_sha256 ~ '^[0-9a-f]{64}$'),
    pipeline_name text NOT NULL CHECK (length(pipeline_name) BETWEEN 1 AND 128),
    validated_at timestamptz NOT NULL,
    CHECK (length(pipeline_path) BETWEEN 1 AND 256),
    CHECK (left(pipeline_path, 1) <> '/'),
    CHECK (strpos(pipeline_path, E'\\') = 0),
    CHECK (pipeline_path !~ '(^|/)[.]{1,2}(/|$)'),
    CHECK (pipeline_path !~ '[[:cntrl:]]'),
    CHECK (pipeline_path ~ '[.]ya?ml$')
);
