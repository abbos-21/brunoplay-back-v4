#!/usr/bin/env bash
# Restore drill: proves the backups really work WITHOUT touching production.
# It restores the latest base backup and the WAL into a throwaway PostgreSQL container, optionally up to a chosen moment,
# and then checks the books: every balance must equal the sum of its ledger.
#   ./restore-drill.sh                      restore to the end of the archived WAL
#   ./restore-drill.sh "2026-10-24 12:30:00+00"   restore to that moment (point-in-time recovery)
# Do this once a month and after any change to the backup setup.
set -euo pipefail
cd "$(dirname "$0")"
set -a; . ./.env; set +a
: "${RESTIC_REPOSITORY:?}"; : "${RESTIC_PASSWORD_FILE:?}"
export RESTIC_REPOSITORY RESTIC_PASSWORD_FILE
target="${1:-}"
work=$(mktemp -d /tmp/bp-restore.XXXXXX)
trap 'docker rm -f bp-restore >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

echo "== fetching the latest snapshot into $work"
restic restore latest --target "$work"
base=$(find "$work" -type d -name 'base_*' | sort | tail -1)
wal=$(find "$work" -type d -name wal -o -type d -name _data | head -1)
[ -f "$base/base.tar.gz" ] || { echo "no base backup found in the snapshot"; exit 1; }

echo "== unpacking the base backup"
mkdir -p "$work/data"
tar -xzf "$base/base.tar.gz" -C "$work/data"
chmod 700 "$work/data"
touch "$work/data/recovery.signal"
{
  echo "restore_command = 'if [ -f /wal/%f.gz ]; then gunzip -c /wal/%f.gz > %p; else cp /wal/%f %p; fi'"
  [ -n "$target" ] && echo "recovery_target_time = '$target'" && echo "recovery_target_action = 'promote'"
} >> "$work/data/postgresql.auto.conf"

echo "== starting a throwaway PostgreSQL on the restored data"
docker run -d --name bp-restore -v "$work/data:/var/lib/postgresql/data" -v "$wal:/wal:ro" postgres:17 >/dev/null
for i in $(seq 1 60); do
  docker exec bp-restore pg_isready -U brunoplay -d brunoplay >/dev/null 2>&1 && break
  sleep 2
done

echo "== checking the books"
docker exec bp-restore psql -U brunoplay -d brunoplay -At -c "select 'players', count(*) from players"
bad=$(docker exec bp-restore psql -U brunoplay -d brunoplay -At -c "
  select count(*) from players p where
    p.coins <> coalesce((select sum(amount) from ledger_entries l where l.player_id=p.id and l.currency='COIN'),0) or
    p.stars <> coalesce((select sum(amount) from ledger_entries l where l.player_id=p.id and l.currency='STAR'),0)")
if [ "$bad" != "0" ]; then echo "RESTORE CHECK FAILED: $bad player(s) whose balance differs from the ledger"; exit 1; fi
echo "restore drill passed: the backup restores and the books balance"
