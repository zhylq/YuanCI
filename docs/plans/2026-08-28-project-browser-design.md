# Authorized project browser — approved design and implementation plan

The user approved this increment after comparing a staged browser-first delivery
with a combined GitHub installation/browser implementation. Keep one project per
repository. This increment is read-only; installation/discovery/import follows.
The writing-plans skill is unavailable, so this document records the plan.

## Contract and security

- Add an internal project read port and PostgreSQL adapter, plus authenticated
  GET /api/v1/projects, /projects/{id} and /projects/{id}/runs. Do not add these
  APIs to evaluation mode or introduce a session/bootstrap bypass.
- Return only active repositories allowed by live instance/organization/project
  grants. Environment-only grants never imply access to the parent project.
  Resolve ownership on the server; authenticate and authorize in the same read
  transaction, hold grants/resources during reads and check session expiry after
  lock waits. Missing/inaccessible projects have the same generic denial.
- Keyset pagination is bounded (default 20, maximum 100), permission filtering
  precedes pagination, search is literal and bounded, cursors convey position
  only and never grant authority. Do not expose global totals or unauthorized
  names, credentials, clone URLs, compiled plans or legacy unscoped runs.
- Project DTOs expose name/owner/provider/organization/default branch and honest
  not-connected status. Existing `active` is not proof of SCM installation.
  Run pages contain summary metadata only, ordered by created_at then ID.
- No project creation/import, external calls, membership administration, run
  execution controls, secret release or changes to Quickstart in this batch.

## Console

Add Projects navigation and account-home entry. Projects supports search and
pagination; the detail page shows repository metadata and paginated runs. Use
native links/buttons, existing slate/blue Tailwind tokens, labeled search,
visible focus, status/error feedback and responsive cards. No new animations.
Keep query keys scoped by user/project/search/cursor, discard old page data on
navigation, disable protected queries in evaluation mode, hide cached data on
authorization/refetch failure and clear data on logout. Empty installations show
an explanation, not fabricated repositories or a fake connect button.

## Implementation and acceptance

1. Commit this design; implement project DTOs/cursor validation, transactional
   reads and protected HTTP handlers; add real PostgreSQL and HTTP tests.
2. Implement project list/detail/run pages and frontend tests for scoped fetching,
   search/paging, empty/loading/error/revocation states and evaluation gating.
3. Update OpenAPI and Chinese usage/status documentation, build the embedded UI.
4. Run Go unit/integration/race/vet against a disposable database, frontend
   tests/lint/build, and browser/container checks where feasible. Check cross-org
   inheritance, duplicate grants, environment-only grants, disabled repositories,
   missing IDs, malformed/replayed/cross-project cursors, revoked/expired sessions
   and minimal payloads. Commit green increments locally; no push.

Fresh real installations may have no projects until the next import increment.
Fixtures belong only in tests, never startup/production data. Mocked browser
screens and real-database API tests are distinct from real GitHub acceptance.
