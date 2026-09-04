-- Preserve the legacy GitHub subject column for upgrade compatibility while
-- making every bootstrap and login configuration provider/instance-bound.
ALTER TABLE oauth_bootstrap
    ADD COLUMN provider text NOT NULL DEFAULT 'github' CHECK (provider IN ('github','gitee')),
    ADD COLUMN provider_instance text NOT NULL DEFAULT 'https://github.com';
ALTER TABLE oauth_bootstrap
    ADD CONSTRAINT oauth_bootstrap_provider_instance CHECK (
        (provider='github' AND provider_instance='https://github.com') OR
        (provider='gitee' AND provider_instance='https://gitee.com')
    );

ALTER TABLE login_configs DROP CONSTRAINT login_configs_provider_check;
ALTER TABLE login_configs
    ADD COLUMN provider_instance text NOT NULL DEFAULT 'https://github.com',
    ADD CONSTRAINT login_configs_provider CHECK (provider IN ('github','gitee')),
    ADD CONSTRAINT login_configs_provider_instance CHECK (
        (provider='github' AND provider_instance='https://github.com') OR
        (provider='gitee' AND provider_instance='https://gitee.com')
    );
DROP INDEX login_config_single_active;
CREATE UNIQUE INDEX login_config_single_active ON login_configs(provider,provider_instance) WHERE status='active';
