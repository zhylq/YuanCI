CREATE TABLE repository_automation_settings (
    repository_id uuid PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    pipeline_path text NOT NULL DEFAULT '.yuanci.yml',
    trigger_push boolean NOT NULL DEFAULT true,
    trigger_tag boolean NOT NULL DEFAULT true,
    trigger_pull_request boolean NOT NULL DEFAULT true,
    cancel_older_commits boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (length(pipeline_path) BETWEEN 1 AND 256),
    CHECK (left(pipeline_path, 1) <> '/'),
    CHECK (strpos(pipeline_path, E'\\') = 0),
    CHECK (pipeline_path !~ '(^|/)\\.{1,2}(/|$)'),
    CHECK (pipeline_path !~ '[[:cntrl:]]'),
    CHECK (pipeline_path ~ '[.]ya?ml$'),
    CHECK (NOT enabled OR trigger_push OR trigger_tag OR trigger_pull_request)
);

CREATE INDEX repository_automation_enabled_idx
    ON repository_automation_settings (repository_id)
    WHERE enabled;
