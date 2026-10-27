#!/usr/bin/env bash
# Deploy the current checkout: build, migrate, restart api and worker, wait until healthy, roll back if it does not come up.
#   ./deploy.sh            deploy
#   ./deploy.sh rollback   go back to the previous image
set -euo pipefail
cd "$(dirname "$0")"

[ -f .env ] || { echo "deploy/.env is missing (copy .env.deploy.example)"; exit 1; }
[ -f ../.env ] || { echo "../.env is missing (copy ../.env.example)"; exit 1; }
set -a; . ./.env; set +a

if [ "${1:-}" = "rollback" ]; then
  prev=$(docker image ls brunoplay --format '{{.Tag}}' | grep -v latest | sed -n 2p)
  [ -n "$prev" ] || { echo "no previous image"; exit 1; }
  echo "rolling back to $prev"
  VERSION=$prev docker compose up -d api worker
  exit 0
fi

export VERSION="$(git -C .. rev-parse --short HEAD 2>/dev/null || date -u +%Y%m%d%H%M%S)"
echo "== building $VERSION"
docker compose build

if [ -n "${SKIP_BACKUP:-}" ]; then
  echo "== skipping the backup (SKIP_BACKUP is set: use it for the very first start, when there is no database yet)"
else
  echo "== backing up before the migration"
  timeout 180 ./backup.sh --quick || echo "(quick backup failed: continuing, the nightly backup is the real one)"
fi

echo "== migrating"
docker compose run --rm migrate

echo "== restarting"
docker tag "brunoplay:$VERSION" brunoplay:latest
docker compose up -d --remove-orphans postgres
case ",${COMPOSE_PROFILES:-}" in *,caddy*) docker compose up -d caddy ;; esac   # Caddy only when asked for; with nginx it is not used
docker compose up -d api worker

# keep the disk tidy: dropped image layers, and a cap on the build cache (it can grow by gigabytes over many deploys)
docker image prune -f >/dev/null 2>&1 || true
docker builder prune -f --keep-storage 2GB >/dev/null 2>&1 || true

echo "== waiting for the API"
for i in $(seq 1 30); do
  status=$(docker inspect --format '{{.State.Health.Status}}' "$(docker compose ps -q api)" 2>/dev/null || echo starting)
  [ "$status" = "healthy" ] && { echo "healthy: $VERSION"; exit 0; }
  sleep 2
done
echo "the API did not become healthy: rolling back"
./deploy.sh rollback
exit 1
