package games

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
)

const (
	GiftOwned     = "OWNED"
	GiftSold      = "SOLD"
	GiftRequested = "CLAIM_REQUESTED"
	GiftDelivered = "DELIVERED"
)

type PlayerGift struct {
	ID           int64      `json:"id"`
	Gift         string     `json:"gift"`
	Name         string     `json:"name"`
	StarValue    int64      `json:"starValue"`
	Status       string     `json:"status"`
	Source       string     `json:"source"`
	RejectReason string     `json:"rejectReason,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	SoldAt       *time.Time `json:"soldAt"`
	ClaimedAt    *time.Time `json:"claimRequestedAt"`
	DeliveredAt  *time.Time `json:"deliveredAt"`
}

// grant puts a won gift into the player's collection (inside the winning transaction).
func (s *Service) grant(ctx context.Context, tx db.Querier, playerID int64, p Prize, source, ref string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO player_gifts (player_id, gift_key, star_value, status, source, source_ref, created_at) VALUES ($1,$2,$3,'OWNED',$4,$5,$6) RETURNING id`,
		playerID, p.Gift, p.Value, source, ref, s.now()).Scan(&id)
	return id, err
}

// GrantGift gives a gift outside the games (tournament prizes).
func (s *Service) GrantGift(ctx context.Context, tx db.Querier, playerID int64, giftKey, source, ref string) (int64, error) {
	_, m, err := s.Gifts(ctx)
	if err != nil {
		return 0, err
	}
	g, ok := m[giftKey]
	if !ok {
		return 0, apperr.NotFound
	}
	return s.grant(ctx, tx, playerID, Prize{Type: TypeGift, Gift: g.Key, Value: g.StarValue}, source, ref)
}

const giftCols = `id, gift_key, star_value, status, source, reject_reason, created_at, sold_at, claim_requested_at, delivered_at`

func (s *Service) scanGift(r pgx.Row, names map[string]Gift, lang apperr.Lang) (*PlayerGift, error) {
	var g PlayerGift
	if err := r.Scan(&g.ID, &g.Gift, &g.StarValue, &g.Status, &g.Source, &g.RejectReason, &g.CreatedAt, &g.SoldAt, &g.ClaimedAt, &g.DeliveredAt); err != nil {
		if db.IsNoRows(err) {
			return nil, apperr.GiftNotOwned
		}
		return nil, err
	}
	if x, ok := names[g.Gift]; ok {
		g.Name = x.Name(lang)
	} else {
		g.Name = g.Gift
	}
	return &g, nil
}

// Collection lists the player's gifts, newest first.
func (s *Service) Collection(ctx context.Context, playerID int64, lang apperr.Lang, limit int, beforeID int64) ([]PlayerGift, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	_, names, _ := s.Gifts(ctx)
	rows, err := s.pool.Query(ctx, `SELECT `+giftCols+` FROM player_gifts WHERE player_id=$1 AND ($2=0 OR id<$2) ORDER BY id DESC LIMIT $3`, playerID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlayerGift{}
	for rows.Next() {
		g, err := s.scanGift(rows, names, lang)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// SellGift turns an owned gift into stars at its full star value. A gift can be sold or claimed only once.
func (s *Service) SellGift(ctx context.Context, playerID, giftID int64) (stars int64, balance int64, err error) {
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, e := s.players.Lock(ctx, tx, playerID); e != nil {
			return e
		}
		var value int64
		e := tx.QueryRow(ctx, `UPDATE player_gifts SET status='SOLD', sold_at=$3 WHERE id=$1 AND player_id=$2 AND status='OWNED' RETURNING star_value`, giftID, playerID, s.now()).Scan(&value)
		if db.IsNoRows(e) {
			return apperr.GiftNotOwned
		}
		if e != nil {
			return e
		}
		balance, e = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: value, Reason: ledger.ReasonGiftSell,
			RefType: "gift", RefID: ledger.RefID(giftID), IdemKey: fmt.Sprintf("giftsell:%d", giftID)})
		stars = value
		return e
	})
	return
}

// ClaimGift asks for the real Telegram gift; an admin delivers it by hand.
func (s *Service) ClaimGift(ctx context.Context, playerID, giftID int64, lang apperr.Lang) (*PlayerGift, error) {
	var out *PlayerGift
	_, names, _ := s.Gifts(ctx)
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, e := s.players.Lock(ctx, tx, playerID); e != nil {
			return e
		}
		g, e := s.scanGift(tx.QueryRow(ctx, `UPDATE player_gifts SET status='CLAIM_REQUESTED', claim_requested_at=$3, reject_reason='' WHERE id=$1 AND player_id=$2 AND status='OWNED' RETURNING `+giftCols,
			giftID, playerID, s.now()), names, lang)
		out = g
		return e
	})
	return out, err
}

// ---- admin

type ClaimRow struct {
	PlayerGift
	PlayerID   int64  `json:"playerId"`
	Username   string `json:"username"`
	TelegramID int64  `json:"telegramId"`
}

// Claims lists gift claims waiting for delivery (status CLAIM_REQUESTED) or any status.
func (s *Service) Claims(ctx context.Context, status string, beforeID int64, limit int) ([]ClaimRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if status == "" {
		status = GiftRequested
	}
	_, names, _ := s.Gifts(ctx)
	rows, err := s.pool.Query(ctx, `SELECT g.id, g.gift_key, g.star_value, g.status, g.source, g.reject_reason, g.created_at, g.sold_at, g.claim_requested_at, g.delivered_at,
		p.id, p.username, p.telegram_id FROM player_gifts g JOIN players p ON p.id=g.player_id WHERE g.status=$1 AND ($2=0 OR g.id<$2) ORDER BY g.id DESC LIMIT $3`, status, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClaimRow{}
	for rows.Next() {
		var c ClaimRow
		if err := rows.Scan(&c.ID, &c.Gift, &c.StarValue, &c.Status, &c.Source, &c.RejectReason, &c.CreatedAt, &c.SoldAt, &c.ClaimedAt, &c.DeliveredAt, &c.PlayerID, &c.Username, &c.TelegramID); err != nil {
			return nil, err
		}
		c.Name = c.Gift
		if g, ok := names[c.Gift]; ok {
			c.Name = g.NameEN
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeliverGift marks a claimed gift as delivered (the admin sent it in Telegram). Returns the owner's id.
func (s *Service) DeliverGift(ctx context.Context, giftID, adminID int64) (int64, error) {
	var owner int64
	err := s.pool.QueryRow(ctx, `UPDATE player_gifts SET status='DELIVERED', delivered_at=$2, admin_id=$3 WHERE id=$1 AND status='CLAIM_REQUESTED' RETURNING player_id`, giftID, s.now(), adminID).Scan(&owner)
	if db.IsNoRows(err) {
		return 0, apperr.GiftNotOwned
	}
	return owner, err
}

// RejectGift sends a claimed gift back to the collection with a reason.
func (s *Service) RejectGift(ctx context.Context, giftID, adminID int64, reason string) (int64, error) {
	var owner int64
	err := s.pool.QueryRow(ctx, `UPDATE player_gifts SET status='OWNED', reject_reason=$2, admin_id=$3 WHERE id=$1 AND status='CLAIM_REQUESTED' RETURNING player_id`, giftID, reason, adminID).Scan(&owner)
	if db.IsNoRows(err) {
		return 0, apperr.GiftNotOwned
	}
	return owner, err
}
