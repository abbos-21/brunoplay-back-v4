#!/usr/bin/env bash
# List the players disqualified in a tournament (read only, nothing is changed).
#
#   ./tournament-disqualified.sh          the last tournament that has ended
#   ./tournament-disqualified.sh 12       the tournament with id 12
set -Eeuo pipefail
cd "$(dirname "$0")"

[ -f .env ] && { set -a; . ./.env; set +a; }
DB_USER="${PG_USER:-brunoplay}"
DB_NAME="${PG_DATABASE:-brunoplay}"
psql_run() { docker compose exec -T postgres psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 "$@"; }

if [ "${1:-}" != "" ]; then
  case "$1" in *[!0-9]*) echo "the tournament id must be a number"; exit 1 ;; esac
  tid="$1"
else
  tid="$(psql_run -tA -c "SELECT id FROM tournaments WHERE ends_at <= now() AND cancelled_at IS NULL ORDER BY ends_at DESC, id DESC LIMIT 1")"
  [ -n "$tid" ] || { echo "no tournament has ended yet"; exit 1; }
fi

psql_run -c "SELECT id, name_en AS tournament, starts_at::date AS starts, ends_at::date AS ends,
  CASE WHEN cancelled_at IS NOT NULL THEN 'CANCELLED' WHEN paid_at IS NOT NULL THEN 'PAID' WHEN ends_at <= now() THEN 'ENDED' ELSE 'ACTIVE' END AS status
  FROM tournaments WHERE id = $tid"

psql_run -c "SELECT COALESCE(NULLIF(p.username, ''), '(no username)') AS username,
    NULLIF(trim(p.first_name || ' ' || p.last_name), '') AS name,
    p.id AS player_id, p.telegram_id, e.score,
    CASE WHEN p.is_blocked THEN 'blocked' ELSE '' END AS blocked
  FROM tournament_entries e JOIN players p ON p.id = e.player_id
  WHERE e.tournament_id = $tid AND e.disqualified
  ORDER BY e.score DESC, p.id"
