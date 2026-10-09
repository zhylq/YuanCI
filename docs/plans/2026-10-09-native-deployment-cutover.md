# Native deployment production acceptance

The administrator installed the verified Server and Runner image package on the
existing YuanCI host. Standard Runner identity remained online at protocol v2;
the new `yuanplan-deployment` identity enrolled in the deployment pool at protocol
v3 with `yuanci.deploy.gitee.50685632=true`.

The older systemd release did not support `show --value`. The installer was
corrected to parse validated property-prefixed output, and an independently
reviewed `finish-cutover.sh` completed the handoff without repeating the upgrade.
The old publisher had exited; the script obtained the application's release lock
and verified application/database health before disabling the old timer. It is
now both inactive and disabled.

YuanPlan `.yuanci.yml` now matches main push events and declares deployment
environment `production`. It runs the user-authored release command directly.
The former all-branch isolated acceptance stage and external poller are absent
from the active deployment flow. Historical publisher documentation was retained
separately with an explicit retirement notice.

Gitee main commit: `7bcdac9a1ec9c301cdced5f7e8c2cdbbdcc3f1c6`.

[Native Run](https://ci.uyii.cn/projects/e706ea8e-2f4e-4e76-8aee-b213e05ea95a/runs/353c0ba0-9586-4789-a0cb-2c095f245869):

- Created: 2026-10-09 01:54:59.515935 UTC.
- Job started: 01:55:01.012423 UTC.
- Run succeeded: 01:56:11.625253 UTC; total 72.109318 seconds.
- Cleanup confirmed: 01:56:11.622877 UTC; environment gate released at 01:56:11.627832 UTC.
- Exactly one Run for this commit, one user step and zero service containers.
- No matching job containers, network or workspace volume remained after completion.

Application image `yuanplan:7bcdac9a1ec9c301cdced5f7e8c2cdbbdcc3f1c6` and the
existing database are healthy. Public `/api/health` returns `status: ok`. The
user-authored release script completed its backup/migration/start/maintenance
sequence and printed the matching published SHA. No production rollback,
forced duplicate deployment or deliberate production cancellation was performed.

Backend regression tests cover duplicate deliveries, FIFO races and cancellation
cleanup holds using disposable PostgreSQL. Local tests cover cancellation,
transport reconnect, policy denial, Docker absence verification and standard
Runner compatibility; the first native main publication supplies end-to-end
acceptance of the deployed path.
