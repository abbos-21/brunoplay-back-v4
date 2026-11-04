#!/usr/bin/env bash
# Reset the game: remove all players and their data, keep the admin settings and everything the admins built.
# What is removed and what is kept is written at the top of ../scripts/reset-game.sql.
#
#   ./reset-game.sh
#
# Steps: show what will go -> you type RESET -> full backup (copied to a place that is never cleaned up) -> stop api and worker ->
# run the SQL in one transaction -> start api and worker again. If anything fails nothing is deleted and the app is started again.
set -Eeuo pipefail
cd "$(dirname "$0")"

[ -f .env ] && { set -a; . ./.env; set +a; }
DB_USER="${PG_USER:-brunoplay}"
DB_NAME="${PG_DATABASE:-brunoplay}"
BACKUP_DIR="${BACKUP_DIR:-$HOME/brunoplay-backups}"
psql_run() { docker compose exec -T postgres psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 "$@"; }

echo "== what is in the game now"
psql_run -c "SELECT (SELECT count(*) FROM players) AS players,
                    (SELECT count(*) FROM ledger_entries) AS ledger_rows,
                    (SELECT count(*) FROM topup_orders) AS topups,
                    (SELECT count(*) FROM withdrawals) AS withdrawals,
                    (SELECT count(*) FROM tournaments) AS tournaments,
                    (SELECT count(*) FROM settings) AS settings_kept,
                    (SELECT count(*) FROM admins) AS admins_kept"
echo
echo "This REMOVES every player and their data (balances, payments, gifts, progress, tournaments)."
echo "It KEEPS the admin settings, admin accounts, tasks, combo, templates, gift pictures and the dashboard statistics."
read -r -p "Type RESET to continue: " answer
[ "$answer" = "RESET" ] || { echo "cancelled, nothing was changed"; exit 1; }

echo "== backing up the whole database first"
./backup.sh --quick </dev/null
newest="$(ls -1 "$BACKUP_DIR"/predeploy/brunoplay-*.dump | sort | tail -n 1)"
[ -s "$newest" ] || { echo "no backup file found: nothing was changed"; exit 1; }
# the backup must be the one just made (if another backup was running, ./backup.sh skips and the newest file is an old one)
[ -n "$(find "$newest" -mmin -3)" ] || { echo "the newest backup is older than 3 minutes (another backup running?): try again. Nothing was changed"; exit 1; }
mkdir -p "$BACKUP_DIR/before-reset"
keep="$BACKUP_DIR/before-reset/$(basename "$newest")"
cp "$newest" "$keep"
echo "backup kept (never deleted automatically): $keep"

started=0
restart() { [ "$started" = 1 ] || { echo "== starting api and worker"; docker compose up -d api worker; started=1; }; }
trap restart EXIT

echo "== stopping api and worker"
docker compose stop api worker

echo "== resetting (it refuses, and changes nothing, if a withdrawal, gift claim or top-up is still in progress)"
psql_run --single-transaction -f - < ../scripts/reset-game.sql

restart
echo "== done. The game has no players; the settings are untouched."
echo "   If you ever need the old data: $keep (see docs/BACKUPS.md, 'Restore for real')."
