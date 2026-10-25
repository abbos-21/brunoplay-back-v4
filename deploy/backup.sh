#!/usr/bin/env bash
# Backups with point-in-time recovery.
#   - PostgreSQL archives every WAL segment to the "wal" volume continuously (see docker-compose.yml)
#   - this script takes a physical base backup, then sends base backup + WAL to an OFF-SERVER restic repository
#     (encrypted, deduplicated, with retention), and trims old local files.
# Run it nightly from cron:   17 3 * * *  /opt/brunoplay/deploy/backup.sh >> /var/log/brunoplay-backup.log 2>&1
#   ./backup.sh --quick   only a base backup on this server (used before a deploy)
set -euo pipefail
cd "$(dirname "$0")"
set -a; . ./.env; set +a
: "${WAL_KEEP_DAYS:=14}"
stamp=$(date -u +%Y%m%dT%H%M%SZ)

echo "== base backup $stamp"
docker compose exec -T postgres bash -c "rm -rf /backups/base_$stamp && pg_basebackup -U brunoplay -D /backups/base_$stamp -Ft -z -X none -c fast -P"

if [ "${1:-}" = "--quick" ]; then echo "quick backup done"; exit 0; fi

: "${RESTIC_REPOSITORY:?set RESTIC_REPOSITORY in deploy/.env (an off-server place)}"
: "${RESTIC_PASSWORD_FILE:?set RESTIC_PASSWORD_FILE in deploy/.env}"
export RESTIC_REPOSITORY RESTIC_PASSWORD_FILE

backups_dir=$(docker volume inspect brunoplay_backups --format '{{.Mountpoint}}')
wal_dir=$(docker volume inspect brunoplay_wal --format '{{.Mountpoint}}')

echo "== sending off the server"
restic snapshots >/dev/null 2>&1 || restic init
restic backup --tag brunoplay "$backups_dir/base_$stamp" "$wal_dir"
restic forget --keep-daily 14 --keep-weekly 8 --keep-monthly 6 --prune
restic check --read-data-subset=5%

echo "== trimming this server"
find "$backups_dir" -maxdepth 1 -name 'base_*' -mtime +3 -exec rm -rf {} +
find "$wal_dir" -type f -mtime +"$WAL_KEEP_DAYS" -delete

echo "backup finished: base_$stamp"
