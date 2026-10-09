#!/usr/bin/env bash
# Resume the final handoff after an already successful native Server/Runner upgrade.
set -Eeuo pipefail
umask 077
[[ ${EUID} = 0 ]] || { echo 'Run this in the administrator terminal.' >&2; exit 2; }
exec 9>/run/lock/yuanci-native-upgrade.lock
flock -n 9 || { echo 'The original installer is still active. Wait for it to exit.' >&2; exit 3; }
test "$(docker inspect -f '{{.Image}}' yuanci-gitee-sandbox-server-1)" = sha256:566ded5ac503c57e8b2a4389a3c510425d6a7d84ab2e8304ea9bf6c38a4471d5
test "$(docker inspect -f '{{.Image}}' yuanci-gitee-sandbox-deployment-runner-1)" = sha256:e5e9fdedf2341b24f4121f81517262b76ba0e758069e70904b92940c4f2b65d6
docker exec yuanci-gitee-sandbox-server-1 wget -q -O /dev/null http://127.0.0.1:8080/readyz
VALUE=$(docker exec yuanci-gitee-sandbox-postgres-1 psql -U yuanci -d yuanci_gitee -At -v ON_ERROR_STOP=1 -c "SELECT EXISTS(SELECT 1 FROM runners r JOIN runner_pools p ON p.id=r.pool_id WHERE r.name='yuanplan-deployment' AND p.pool_type='deployment' AND r.isolation_level='deployment' AND r.protocol_version=3 AND r.status='online' AND r.last_seen_at>now()-interval '30 seconds' AND r.labels->>'yuanci.deploy.gitee.50685632'='true')")
[[ "$VALUE" = t ]] || { echo 'The native deployment Runner is not ready.' >&2; exit 4; }
systemctl stop yuanplan-publish.timer
publisher_idle() {
  local state pid containers
  state=$(systemctl show yuanplan-publish.service --property=ActiveState) || return 1
  pid=$(systemctl show yuanplan-publish.service --property=MainPID) || return 1
  [[ "$state" = ActiveState=* && "$pid" = MainPID=* ]] || return 1
  state=${state#ActiveState=}
  pid=${pid#MainPID=}
  [[ "$pid" = 0 && ( "$state" = inactive || "$state" = failed ) ]] || return 1
  containers=$(docker container ls --all --filter 'name=^/yuanplan-publisher$' --format '{{.Names}}') || return 1
  [[ -z "$containers" ]] || return 1
}
for ((i=0; i<150; i++)); do
  if publisher_idle; then break; fi
  sleep 2
done
publisher_idle || { echo 'Old publisher is still active or cannot be checked; do not push the new YAML.' >&2; exit 5; }
exec 8>/opt/yuanplan/.data/automation/release.lock
flock -n 8 || { echo 'Old release still holds the application lock; do not push the new YAML.' >&2; exit 6; }
test "$(docker inspect -f '{{.State.Health.Status}}' yuanplan-app-1)" = healthy
test "$(docker inspect -f '{{.State.Health.Status}}' yuanplan-db-1)" = healthy
systemctl disable yuanplan-publish.timer
echo 'Native deployment verified. Legacy timer disabled. Ready for the main-only YuanPlan YAML.'
