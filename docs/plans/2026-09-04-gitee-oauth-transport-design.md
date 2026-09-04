# Gitee OAuth REST credential transport

## Context

The managed Gitee grant is active and includes the required `user_info` and
`projects` scopes, but repository discovery cannot complete. Gitee's official
OpenAPI definition declares `access_token` as a query parameter for
`/v5/user/repos`; YuanCI currently sends that token only as a Bearer header.

## Decision

Use the documented `access_token` query parameter for every authenticated
Gitee REST request. Keep all OAuth exchange and refresh requests unchanged.
The helper that constructs GET requests will attach the parameter centrally;
the Check Runs write path will use the same representation.

## Safety and verification

Tokens remain absent from application logs, error messages, browser responses,
Runner credentials and persisted repository records. Targeted client and Check
Runs contract tests assert the parameter is present and the Authorization
header is absent. Deployment verification will use the existing authorized
Gitee grant and the operator's repository-settings page; it does not inspect
or export the OAuth token.
