#!/usr/bin/env bash
# FULL reset: delete EVERYTHING in the database (players, settings, admin accounts, tasks, combo, templates, gift pictures), build it
# again empty with all migrations, and create the first admin. Then you put the settings and tasks back with scripts/seed-snapshot.mjs
# (run from your own computer, see the last lines this script prints).
#
#   ./wipe-database.sh
#
# For removing only the players and keeping the settings use ./reset-game.sh instead.
# Steps: show what is there -> you type WIPE EVERYTHING -> admin e-mail and password (typed here, never stored) -> full backup (kept for good) ->
# stop api and worker -> drop and recreate the schema -> migrate -> create the admin -> start api and worker.
set -Eeuo pipefail
cd "$(dirname "$0")"

[ -f .env ] && { set -a; . ./.env; set +a; }
DB_USER="${PG_USER:-brunoplay}"
DB_NAME="${PG_DATABASE:-brunoplay}"
BACKUP_DIR="${BACKUP_DIR:-$HOME/brunoplay-backups}"
psql_run() { docker compose exec -T postgres psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 "$@"; }

echo "== what is in the database now"
psql_run -c "SELECT (SELECT count(*) FROM players) AS players, (SELECT count(*) FROM settings) AS settings,
                    (SELECT count(*) FROM admins) AS admins, (SELECT count(*) FROM tasks) AS tasks,
                    (SELECT count(*) FROM gift_images) AS gift_pictures, (SELECT count(*) FROM combo_images) AS combo_images"
echo
echo "This DELETES EVERYTHING: players, payments history, ALL settings, admin accounts, tasks, daily combo images and patterns,"
echo "notification templates and gift pictures. The game will not work until you restore the settings with seed-snapshot.mjs."
read -r -p "Type WIPE EVERYTHING to continue: " answer
[ "$answer" = "WIPE EVERYTHING" ] || { echo "cancelled, nothing was changed"; exit 1; }

# the first admin: asked now, before anything is deleted (the password stays in this shell only, it is not written anywhere)
read -r -p "E-mail of the first admin: " admin_email
case "$admin_email" in *@*.*) ;; *) echo "that does not look like an e-mail: nothing was changed"; exit 1 ;; esac
good_password() { # 12+ characters and at least three of: lower case, upper case, digit, other
  local p="$1" n=0
  [ "${#p}" -ge 12 ] || return 1
  [[ "$p" =~ [a-z] ]] && n=$((n+1)); [[ "$p" =~ [A-Z] ]] && n=$((n+1)); [[ "$p" =~ [0-9] ]] && n=$((n+1)); [[ "$p" =~ [^a-zA-Z0-9] ]] && n=$((n+1))
  [ "$n" -ge 3 ]
}
ask_password() {
  local a b
  while true; do
    read -rsp "Password of the first admin (12+ characters, three of: a-z, A-Z, 0-9, symbols): " a; echo
    good_password "$a" || { echo "too weak, try again"; continue; }
    read -rsp "Again: " b; echo
    [ "$a" = "$b" ] || { echo "they differ, try again"; continue; }
    ADMIN_PASSWORD="$a"; export ADMIN_PASSWORD
    return 0
  done
}
ask_password

echo "== backing up the whole database first"
./backup.sh --quick </dev/null
newest="$(ls -1 "$BACKUP_DIR"/predeploy/brunoplay-*.dump | sort | tail -n 1)"
[ -s "$newest" ] || { echo "no backup file found: nothing was changed"; exit 1; }
[ -n "$(find "$newest" -mmin -3)" ] || { echo "the newest backup is older than 3 minutes (another backup running?): try again. Nothing was changed"; exit 1; }
mkdir -p "$BACKUP_DIR/before-wipe"
keep="$BACKUP_DIR/before-wipe/$(basename "$newest")"
cp "$newest" "$keep"
echo "backup kept (never deleted automatically): $keep"

started=0
restart() { [ "$started" = 1 ] || { echo "== starting api and worker"; docker compose up -d api worker; started=1; }; }
trap 'echo; echo "!! FAILED. The backup is at $keep (docs/BACKUPS.md, Restore for real)."; restart' ERR

echo "== stopping api and worker"
docker compose stop api worker

echo "== deleting everything"
psql_run -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

echo "== building the empty database (migrations)"
docker compose run --rm migrate

echo "== creating the first admin"
tries=0
until docker compose run --rm -e ADMIN_PASSWORD api admin-create --email "$admin_email"; do
  tries=$((tries+1))
  [ "$tries" -lt 3 ] || { echo "the admin could not be created. Create it by hand: ADMIN_PASSWORD=... docker compose run --rm -e ADMIN_PASSWORD api admin-create --email $admin_email"; break; }
  echo "the server did not accept that password (too common or too weak?)"
  ask_password
done
unset ADMIN_PASSWORD

restart
trap - ERR
echo
echo "== the database is empty and the first admin exists. NOW put the settings back, from your own computer:"
echo "   cd \"<your PC>/brunoplay-back-v4\""
echo "   set ADMIN_PASSWORD=<the password you just chose>       (PowerShell: \$env:ADMIN_PASSWORD='...')"
echo "   node scripts/seed-snapshot.mjs --api https://brunoplay.duckdns.org --email $admin_email           (look at the dry run)"
echo "   node scripts/seed-snapshot.mjs --api https://brunoplay.duckdns.org --email $admin_email --apply   (restore)"
echo "   The old data stays in $keep"
