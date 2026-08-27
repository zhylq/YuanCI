# Repository bootstrap design

## Decision

Initialize the empty GitHub repository with `main` as its primary branch and a
single, reviewable initial commit containing the complete Milestone 0 codebase.
Configure `origin` as `https://github.com/zhylq/YuanCI.git` but leave publishing
to a separate, explicit push operation.

## Documentation

Keep the README concise and link to a Chinese getting-started guide. The guide
uses Docker Compose as the default path so operators do not need Go, Node.js or
knowledge of either language. It must clearly separate the local evaluation
profile from the future production profile and call out the Docker socket and
unauthenticated Milestone 0 API risks.

## Verification

Before the initial commit:

- run all Go tests and `go vet`;
- run the console tests, linter and production build;
- validate all Compose files with non-secret placeholder values;
- scan staged files for credentials and confirm ignored local tool/cache files
  are not included;
- review the staged diff summary and commit identity.

## Commit policy

Use the configured author `zhy <zhy@uyii.cn>` and create one conventional commit:
`feat: bootstrap YuanCI`. Do not amend, force-push or publish without a separate
request.
