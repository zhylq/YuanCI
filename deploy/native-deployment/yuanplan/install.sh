#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
[[ ${EUID} = 0 ]] || { echo 'Run this installer in the administrator terminal.' >&2; exit 2; }
PACKAGE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
STACK=/home/deploy/yuanci-gitee-sandbox
test -d "$STACK"
test -s "$STACK/.env"
test -S /var/run/docker.sock
test -d /opt/yuanplan
command -v docker >/dev/null
command -v flock >/dev/null
docker compose version >/dev/null
exec 9>/run/lock/yuanci-native-upgrade.lock
flock -n 9 || { echo 'Another native upgrade is active.' >&2; exit 3; }
cd "$PACKAGE"
sha256sum --check SHA256SUMS
# Fixed values generated from the verified images, never project credentials.
source "$PACKAGE/images.env"
docker load --input "$PACKAGE/images.tar.gz"
test "$(docker image inspect -f '{{.Id}}' "$SERVER_IMAGE")" = "$SERVER_IMAGE_ID"
test "$(docker image inspect -f '{{.Id}}' "$RUNNER_IMAGE")" = "$RUNNER_IMAGE_ID"
cd "$STACK"
BASE=(docker compose -f compose.gitee-sandbox.yml -f compose.gitee-ci-domain.override.yml -f compose.automation-a882e8d.override.yml)
NEXT=("${BASE[@]}" -f compose.command-deployment.override.yml)
BACKUP="/root/yuanci-native-backups/$SOURCE_COMMIT-$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 "$BACKUP"
cp compose.gitee-sandbox.yml compose.gitee-ci-domain.override.yml compose.automation-a882e8d.override.yml "$BACKUP/"
cp .env "$BACKUP/stack.env"
if test -f compose.command-deployment.override.yml; then
  cp compose.command-deployment.override.yml "$BACKUP/compose.command-deployment.before.yml"
fi
"${BASE[@]}" exec -T postgres pg_dump -U yuanci -d yuanci_gitee --format=custom >"$BACKUP/yuanci.dump"
test -s "$BACKUP/yuanci.dump"
if test -f /etc/yuanci/deployment-policy.json; then
  cp /etc/yuanci/deployment-policy.json "$BACKUP/deployment-policy.before.json"
fi
install -d -o root -g root -m 0755 /etc/yuanci
install -o root -g root -m 0444 "$PACKAGE/deployment-policy.json" /etc/yuanci/deployment-policy.json
install -m 0644 "$PACKAGE/compose.command-deployment.override.yml" "$STACK/compose.command-deployment.override.yml"
"${NEXT[@]}" config --quiet
"${BASE[@]}" exec -T postgres psql -U yuanci -d yuanci_gitee -v ON_ERROR_STOP=1 <<'SQL'
BEGIN;
INSERT INTO runner_pools(name,pool_type,labels) VALUES('deployment','deployment','{}'::jsonb)
ON CONFLICT(name) DO NOTHING;
DO $$ BEGIN
  IF NOT EXISTS(SELECT 1 FROM runner_pools WHERE name='deployment' AND pool_type='deployment') THEN
    RAISE EXCEPTION 'Existing deployment pool has incompatible type';
  END IF;
END $$;
COMMIT;
SQL
"${NEXT[@]}" up -d --no-deps server runner
READY=false
for ((i=0; i<60; i++)); do
  if "${NEXT[@]}" exec -T server wget -q -O /dev/null http://127.0.0.1:8080/readyz; then READY=true; break; fi
  sleep 2
done
[[ "$READY" = true ]] || { echo "Server is not ready. Protected database backup: $BACKUP" >&2; exit 4; }
"${NEXT[@]}" up -d deployment-runner
READY=false
for ((i=0; i<60; i++)); do
  VALUE=$("${NEXT[@]}" exec -T postgres psql -U yuanci -d yuanci_gitee -At -v ON_ERROR_STOP=1 -c "SELECT EXISTS(SELECT 1 FROM runners r JOIN runner_pools p ON p.id=r.pool_id WHERE r.name='yuanplan-deployment' AND p.pool_type='deployment' AND r.isolation_level='deployment' AND r.protocol_version=3 AND r.status='online' AND r.last_seen_at>now()-interval '30 seconds' AND r.labels->>'yuanci.deploy.gitee.50685632'='true')")
  if [[ "$VALUE" = t ]]; then READY=true; break; fi
  sleep 2
done
[[ "$READY" = true ]] || { echo 'Deployment Runner did not enroll and heartbeat; legacy timer remains unchanged.' >&2; exit 5; }
# Close the old publisher before the new main YAML is pushed. Never kill a
# running release: allow its foreground service to finish before completing.
if systemctl cat yuanplan-publish.timer >/dev/null 2>&1; then
  systemctl stop yuanplan-publish.timer
  publisher_idle() {
    local state pid containers
    state=$(systemctl show yuanplan-publish.service --property=ActiveState --value) || return 1
    pid=$(systemctl show yuanplan-publish.service --property=MainPID --value) || return 1
    [[ "$pid" = 0 && ( "$state" = inactive || "$state" = failed ) ]] || return 1
    containers=$(docker container ls --all --filter 'name=^/yuanplan-publisher$' --format '{{.Names}}') || return 1
    [[ -z "$containers" ]] || return 1
  }
  for ((i=0; i<150; i++)); do
    if publisher_idle; then break; fi
    sleep 2
  done
  if ! publisher_idle; then
    echo 'An existing release is still active; timer stopped. Wait for it to finish, then rerun this installer before pushing the new YAML.' >&2
    exit 6
  fi
  systemctl disable yuanplan-publish.timer
fi
exec 8>/opt/yuanplan/.data/automation/release.lock
flock -n 8 || { echo 'An existing release still holds the application lock. Timer remains stopped; do not push the new YAML yet.' >&2; exit 7; }
test "$(docker inspect -f '{{.State.Health.Status}}' yuanplan-app-1)" = healthy
test "$(docker inspect -f '{{.State.Health.Status}}' yuanplan-db-1)" = healthy
echo "Native server and deployment Runner verified. Legacy timer disabled. Backup: $BACKUP"
echo 'Ready for the prepared main-only YuanPlan YAML; no application deployment has been forced by this installer.'
