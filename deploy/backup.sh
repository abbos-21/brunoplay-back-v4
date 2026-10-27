#!/usr/bin/env bash
# Database backups: a compressed dump of the whole database, checked right after it is written, kept on this server and
# copied off it. Simple on purpose: restoring is one command (see docs/BACKUPS.md).
#
#   ./backup.sh            the hourly job (cron): dump, check, keep 48 hourly + 14 daily, copy off the server, alert on any problem
#   ./backup.sh --quick    only a dump and its check (used before every deploy; takes seconds)
#
# Settings in deploy/.env (all optional):
#   BACKUP_DIR            where the dumps are kept on this server (default: ~/brunoplay-backups)
#   RESTIC_REPOSITORY     the OFF-SERVER place (see docs/BACKUPS.md). Without it the backup still runs but warns you once a day.
#   RESTIC_PASSWORD_FILE  a file that holds the repository password (keep a second copy of that password somewhere else too)
# The bot messages ALERT_CHAT_ID (from ../.env) when a backup fails or a disk is filling up.
set -Eeuo pipefail
cd "$(dirname "$0")"
if [ -f .env ]; then set -a; . ./.env; set +a; fi

# how to reach PostgreSQL's tools: inside the container (the tests of this script set PG_EXEC to an empty string)
PG="${PG_EXEC-docker compose exec -T postgres}"
DB_USER="${PG_USER:-brunoplay}"
DB_NAME="${PG_DATABASE:-brunoplay}"
BACKUP_DIR="${BACKUP_DIR:-$HOME/brunoplay-backups}"

env_value() { grep -m1 "^$1=" ../.env 2>/dev/null | cut -d= -f2- || true; }
BOT_TOKEN="$(env_value BOT_TOKEN)"
ALERT_CHAT_ID="$(env_value ALERT_CHAT_ID)"

alert() {
  echo "ALERT: $1" >&2
  if [ -n "$BOT_TOKEN" ] && [ -n "$ALERT_CHAT_ID" ]; then
    # the token goes in through stdin so it never shows up in the process list
    printf 'url = "https://api.telegram.org/bot%s/sendMessage"\n' "$BOT_TOKEN" |
      curl -s -m 15 -o /dev/null -K - --data-urlencode "chat_id=$ALERT_CHAT_ID" --data-urlencode "text=$(hostname): $1" || true
  fi
}
# a half-written file must never stay behind, and any failure must reach you
cleanup() { rm -f "${tmp:-}"; }
fail() { cleanup; alert "the database backup FAILED: $1. Look at ~/brunoplay-backup.log"; exit 1; }
trap 'cleanup; alert "the database backup FAILED (line $LINENO). Look at ~/brunoplay-backup.log"' ERR

mkdir -p "$BACKUP_DIR"/hourly "$BACKUP_DIR"/daily "$BACKUP_DIR"/predeploy
chmod 700 "$BACKUP_DIR"
command -v flock >/dev/null 2>&1 || { echo "flock is not installed (sudo apt install util-linux)"; exit 1; }
exec 9>"$BACKUP_DIR/.lock"
flock -n 9 || { echo "another backup is still running: skipping this one"; exit 0; }

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
mode=hourly
[ "${1:-}" = "--quick" ] && mode=predeploy
final="$BACKUP_DIR/$mode/brunoplay-$stamp.dump"
tmp="$final.tmp"

echo "== $(date -u +%FT%TZ) dumping the database ($mode)"
# shellcheck disable=SC2086
$PG pg_dump -U "$DB_USER" -d "$DB_NAME" -Fc -Z 6 > "$tmp" < /dev/null   # no stdin: under "timeout" a read from the terminal would freeze it

# a dump that is empty or cannot be read is worse than none: it must never replace a good one
size="$(stat -c %s "$tmp")"
if [ "$size" -lt 20000 ]; then
  echo "the dump is only $size bytes: refusing to keep it"
  fail "the dump is only $size bytes (an empty or broken database?)"
fi
# shellcheck disable=SC2086
tables="$($PG pg_restore --list < "$tmp" | grep -c ' TABLE DATA ' || true)"
if [ "$tables" -lt 10 ]; then
  echo "the dump lists only $tables tables: refusing to keep it"
  fail "the dump lists only $tables tables"
fi
mv "$tmp" "$final"
echo "dump ok: $final ($size bytes, $tables tables)"

if [ "$mode" = predeploy ]; then
  ls -1 "$BACKUP_DIR/predeploy"/brunoplay-*.dump | sort | head -n -5 | xargs -r rm -f
  echo "quick backup done"
  exit 0
fi

# ---- keep this server tidy: 48 hourly dumps, and the first dump of each day for 14 days
ls -1 "$BACKUP_DIR/hourly"/brunoplay-*.dump | sort | head -n -48 | xargs -r rm -f
day="$(date -u +%Y%m%d)"
ls "$BACKUP_DIR/daily"/brunoplay-"$day"T*.dump >/dev/null 2>&1 || cp "$final" "$BACKUP_DIR/daily/"
ls -1 "$BACKUP_DIR/daily"/brunoplay-*.dump | sort | head -n -14 | xargs -r rm -f

# ---- off the server
if [ -n "${RESTIC_REPOSITORY:-}" ]; then
  if ! command -v restic >/dev/null 2>&1; then
    fail "restic is not installed, so nothing is copied off the server (sudo apt install restic)"
  fi
  export RESTIC_REPOSITORY
  [ -n "${RESTIC_PASSWORD_FILE:-}" ] && export RESTIC_PASSWORD_FILE
  restic snapshots >/dev/null 2>&1 || restic init
  restic backup --tag brunoplay --quiet "$final"
  if [ "$(date -u +%H)" = "03" ]; then
    # once a night: forget old copies and prove a sample of what is stored can still be read
    restic forget --tag brunoplay --keep-hourly 48 --keep-daily 14 --keep-weekly 8 --keep-monthly 6 --prune --quiet
    restic check --read-data-subset=5% --quiet
  fi
  echo "copied off the server"
else
  flag="$BACKUP_DIR/.no-offsite-warned-$day"
  echo "WARNING: RESTIC_REPOSITORY is not set, the backups exist only on this server"
  if [ ! -e "$flag" ]; then
    alert "the backups exist only on this server. If it dies they die with it: set RESTIC_REPOSITORY (docs/BACKUPS.md)"
    touch "$flag"
    find "$BACKUP_DIR" -maxdepth 1 -name '.no-offsite-warned-*' -mtime +3 -delete
  fi
fi

# ---- the disk
for mount in $( { df --output=target / | tail -n 1; df --output=target "$BACKUP_DIR" | tail -n 1; } | sort -u); do
  used="$(df --output=pcent "$mount" | tail -n 1 | tr -dc '0-9')"
  if [ "${used:-0}" -ge 80 ]; then
    alert "the disk is ${used}% full ($mount)"
  fi
done
echo "backup finished"
