#!/usr/bin/env bash
# Show the top 10 of a tournament, with the prize of each place (read only, nothing is paid or changed).
# The order is the one "Pay prizes" uses: highest score first, the earlier score wins a tie; disqualified and blocked players and zero scores are left out.
#
#   ./tournament-winners.sh          the last tournament that has ended
#   ./tournament-winners.sh 12       the tournament with id 12
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

psql_run -c "SELECT id, name_en AS tournament, metric, starts_at::date AS starts, ends_at::date AS ends,
  CASE WHEN cancelled_at IS NOT NULL THEN 'CANCELLED' WHEN paid_at IS NOT NULL THEN 'PAID' WHEN ends_at <= now() THEN 'ENDED' ELSE 'ACTIVE' END AS status
  FROM tournaments WHERE id = $tid"

psql_run -c "WITH ranked AS (
    SELECT e.player_id, e.score, row_number() OVER (ORDER BY e.score DESC, e.score_at ASC, e.player_id) AS place
    FROM tournament_entries e JOIN players p ON p.id = e.player_id
    WHERE e.tournament_id = $tid AND NOT e.disqualified AND NOT p.is_blocked AND e.score > 0
  )
  SELECT r.place, COALESCE(NULLIF(p.username, ''), '(no username)') AS username,
    NULLIF(trim(p.first_name || ' ' || p.last_name), '') AS name,
    p.id AS player_id, p.telegram_id, r.score,
    NULLIF(concat_ws(', ',
      CASE WHEN COALESCE((pr->>'coins')::bigint, 0) > 0 THEN (pr->>'coins') || ' coins' END,
      CASE WHEN COALESCE((pr->>'stars')::bigint, 0) > 0 THEN (pr->>'stars') || ' stars' END,
      NULLIF(pr->>'gift', '')), '') AS prize
  FROM ranked r JOIN players p ON p.id = r.player_id
  LEFT JOIN LATERAL (SELECT x AS pr FROM tournaments t, jsonb_array_elements(t.prizes) x
                     WHERE t.id = $tid AND (x->>'place')::int = r.place LIMIT 1) z ON true
  WHERE r.place <= 10
  ORDER BY r.place"
