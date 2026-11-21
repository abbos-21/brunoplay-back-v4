package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"brunoplay/internal/apperr"
	"brunoplay/internal/httpx"
)

// gameSession is one result of one game for the admin: a spin (Spin or Roulette) or a chest that was opened.
type gameSession struct {
	Game       string    `json:"game"` // spin | roulette | chest
	At         time.Time `json:"at"`
	Detail     string    `json:"detail"` // the Spin tier
	Spent      int64     `json:"spent"`  // stars paid for it
	PrizeType  string    `json:"prizeType"`
	PrizeValue int64     `json:"prizeValue"`
	Gift       string    `json:"gift"`     // the catalogue key of the gift won
	GiftName   string    `json:"giftName"` // its name
	Ref        string    `json:"ref"`
}

// adminPlayerGames lists what the player won in Spin, Roulette and Chest, newest first (paged by offset), optionally for one game.
func (d *Deps) adminPlayerGames(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	game := r.URL.Query().Get("game")
	switch game {
	case "", "spin", "roulette", "chest":
	default:
		return apperr.BadRequest.With("reason", "unknown game")
	}
	offset := int(queryInt(r, "offset", 0))
	if offset < 0 {
		offset = 0
	}
	limit := int(queryInt(r, "limit", 30))
	if limit < 1 || limit > 100 {
		limit = 30
	}
	// Spin and Roulette keep one row per spin (a 10x purchase is ten rows); a chest round keeps its prizes and the slots that were opened
	rows, err := d.Pool.Query(r.Context(), `SELECT game, at, detail, spent, prize_key, star_value, ref FROM (
			SELECT id, CASE WHEN tier='roulette' THEN 'roulette' ELSE 'spin' END AS game, created_at AS at, tier AS detail, price AS spent,
				gift_key AS prize_key, star_value, batch AS ref
			FROM spin_draws WHERE player_id=$1
			UNION ALL
			SELECT c.id, 'chest', COALESCE(c.finished_at, c.created_at), '', c.cost / GREATEST(c.picks, 1),
				CASE WHEN pr->>'type'='gift' THEN 'gift:' || COALESCE(pr->>'gift','') ELSE (pr->>'type') || ':' || COALESCE(pr->>'value','0') END,
				COALESCE((pr->>'value')::bigint, 0), c.id::text
			FROM chest_rounds c
			CROSS JOIN LATERAL jsonb_each_text(c.opened) o
			CROSS JOIN LATERAL (SELECT c.prizes -> (o.value)::int AS pr) x
			WHERE c.player_id=$1
		) s WHERE ($2='' OR game=$2) ORDER BY at DESC, id DESC LIMIT $3 OFFSET $4`, id, game, limit+1, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	_, names, _ := d.Games.Gifts(r.Context())
	out := []gameSession{}
	for rows.Next() {
		var s gameSession
		var key string
		if err := rows.Scan(&s.Game, &s.At, &s.Detail, &s.Spent, &key, &s.PrizeValue, &s.Ref); err != nil {
			return err
		}
		typ, rest, _ := strings.Cut(key, ":")
		s.PrizeType = typ
		if typ == "gift" {
			s.Gift = rest
			s.GiftName = rest
			if g, ok := names[rest]; ok {
				s.GiftName = g.Name(apperr.EN)
			}
		} else if v, err := strconv.ParseInt(rest, 10, 64); err == nil {
			s.PrizeValue = v
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	httpx.JSON(w, 200, map[string]any{"sessions": out, "more": more})
	return nil
}
