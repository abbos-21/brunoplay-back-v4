#!/usr/bin/env bash
# Restore drill: proves that a backup really restores, WITHOUT touching production.
# It loads a dump into a throwaway PostgreSQL container, then checks the books: every player's balance must equal the sum of
# their ledger. Run it on the first day, once a month, and after any change to the backup setup.
#
#   ./restore-drill.sh                 the newest hourly dump on this server
#   ./restore-drill.sh FILE.dump       a specific dump
#   ./restore-drill.sh --offsite       first fetch the newest copy from the off-server repository (proves THAT works too)
set -euo pipefail
cd "$(dirname "$0")"
if [ -f .env ]; then set -a; . ./.env; set +a; fi
BACKUP_DIR="${BACKUP_DIR:-$HOME/brunoplay-backups}"

work="$(mktemp -d)"
name="bp-drill-$$"
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

dump=""
case "${1:-}" in
  --offsite)
    : "${RESTIC_REPOSITORY:?RESTIC_REPOSITORY is not set in deploy/.env}"
    export RESTIC_REPOSITORY
    [ -n "${RESTIC_PASSWORD_FILE:-}" ] && export RESTIC_PASSWORD_FILE
    echo "== fetching the newest copy from the off-server repository"
    restic restore latest --tag brunoplay --target "$work/fetched"
    dump="$(find "$work/fetched" -name 'brunoplay-*.dump' | sort | tail -n 1)"
    ;;
  "")
    dump="$(ls -1 "$BACKUP_DIR"/hourly/brunoplay-*.dump 2>/dev/null | sort | tail -n 1 || true)"
    ;;
  *)
    dump="$1"
    ;;
esac
[ -n "$dump" ] && [ -f "$dump" ] || { echo "no dump found to restore"; exit 1; }
echo "== restoring $dump ($(du -h "$dump" | cut -f1)) into a throwaway PostgreSQL"

docker run -d --name "$name" \
  -e POSTGRES_USER=brunoplay -e POSTGRES_PASSWORD=drill -e POSTGRES_DB=brunoplay -e POSTGRES_INITDB_ARGS=--data-checksums \
  postgres:17 >/dev/null
# the image first starts a temporary server to run its set-up, then restarts: wait for the real one
for _ in $(seq 1 90); do
  docker logs "$name" 2>&1 | grep -q 'init process complete' && docker exec "$name" pg_isready -U brunoplay -d brunoplay >/dev/null 2>&1 && break
  sleep 2
done
docker exec "$name" pg_isready -U brunoplay -d brunoplay >/dev/null || { echo "the throwaway database did not start"; exit 1; }

docker exec -i "$name" pg_restore -U brunoplay -d brunoplay --no-owner --exit-on-error < "$dump"

q() { docker exec "$name" psql -U brunoplay -d brunoplay -At -c "$1"; }
echo "== checking the books"
echo "tables:         $(q "select count(*) from information_schema.tables where table_schema='public'")"
echo "players:        $(q 'select count(*) from players')"
echo "ledger entries: $(q 'select count(*) from ledger_entries')"
bad="$(q "select count(*) from players p where
    p.coins <> coalesce((select sum(amount) from ledger_entries l where l.player_id=p.id and l.currency='COIN'),0) or
    p.stars <> coalesce((select sum(amount) from ledger_entries l where l.player_id=p.id and l.currency='STAR'),0)")"
if [ "$bad" != "0" ]; then
  echo "RESTORE CHECK FAILED: $bad player(s) whose balance differs from the ledger"
  exit 1
fi
echo "restore drill passed: the backup restores and the books balance"
