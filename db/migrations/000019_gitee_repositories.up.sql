CREATE TABLE gitee_accounts (
 account_id text PRIMARY KEY CHECK (account_id ~ '^[1-9][0-9]*$'),
 organization_id uuid UNIQUE NOT NULL REFERENCES organizations(id)
);
ALTER TABLE repositories ADD COLUMN gitee_authorization_id uuid REFERENCES gitee_authorizations(id);
ALTER TABLE repositories ADD CONSTRAINT repositories_gitee_binding CHECK (
 gitee_authorization_id IS NULL OR (provider='gitee' AND provider_instance='https://gitee.com' AND github_installation_id IS NULL)
);
