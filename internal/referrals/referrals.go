// Package referrals pays the friends system: a friend counts at player level 2, the inviter earns a reward for every level the
// friend reaches and a capped share of the friend's mining, and the friend gets the (per-inviter) join bonus.
package referrals

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/db"
	"brunoplay/internal/fishing"
	"brunoplay/internal/ledger"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
	"brunoplay/internal/shop"
	"brunoplay/internal/topup"
)

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	botName  string
	appName  string // the Mini App short name of the bot
	Now      func() time.Time

	// OnFriendCounted / OnReward run inside the transaction (bot notifications).
	OnFriendCounted []func(ctx context.Context, tx pgx.Tx, inviterID, friendID int64) error
	OnReward        []func(ctx context.Context, tx pgx.Tx, inviterID, friendID int64, kind string, level int, coins int64) error
	// OnFriendInvested runs after a friend's Stars top-up: stars is what the inviter got (0 = nothing), newlyCounted tells that the
	// top-up made the friend count.
	OnFriendInvested []func(ctx context.Context, tx pgx.Tx, inviterID, friendID, stars int64, newlyCounted bool) error
}

func New(pool *pgxpool.Pool, st *settings.Service, botName, appShortName string) *Service {
	if appShortName == "" {
		appShortName = "app"
	}
	return &Service{pool: pool, settings: st, botName: botName, appName: appShortName, Now: time.Now}
}

func (s *Service) now() time.Time { return s.Now() }

func (s *Service) tiers(ctx context.Context) map[int]int64 {
	raw, err := settings.Get[map[string]int64](ctx, s.settings, settings.KeyRefLevelRewards)
	if err != nil {
		return nil
	}
	out := map[int]int64{}
	for k, v := range raw {
		if l, err := strconv.Atoi(k); err == nil {
			out[l] = v
		}
	}
	return out
}

// OnLevelUp is registered on the shop: it runs when ANY player's level rises.
func (s *Service) OnLevelUp(ctx context.Context, tx pgx.Tx, ev shop.LevelUpEvent) error {
	var inviter *int64
	var counted bool
	var bonus int64
	if err := tx.QueryRow(ctx, `SELECT referred_by, referral_counted, friend_bonus_coins FROM players WHERE id=$1 FOR UPDATE`, ev.PlayerID).Scan(&inviter, &counted, &bonus); err != nil {
		return err
	}
	if inviter == nil || ev.NewLevel < 2 {
		return nil
	}
	var inviterBlocked, inviterHeld bool
	if err := tx.QueryRow(ctx, `SELECT is_blocked, hold_rewards FROM players WHERE id=$1`, *inviter).Scan(&inviterBlocked, &inviterHeld); err != nil {
		return err
	}
	if !counted {
		if _, err := tx.Exec(ctx, `UPDATE players SET referral_counted=TRUE, referral_counted_at=$2 WHERE id=$1`, ev.PlayerID, s.now()); err != nil {
			return err
		}
		for _, h := range s.OnFriendCounted {
			if err := h(ctx, tx, *inviter, ev.PlayerID); err != nil {
				return err
			}
		}
	}
	// the friend's own join bonus belongs to level 2 even if the friend was counted earlier by a top-up (the ledger key keeps it single)
	if bonus > 0 {
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: ev.PlayerID, Currency: ledger.Coin, Amount: bonus, Reason: ledger.ReasonFriendBonus,
			RefType: "referral", RefID: ledger.RefID(*inviter), IdemKey: fmt.Sprintf("friendbonus:%d", ev.PlayerID), Earned: true}); err != nil && err != ledger.ErrDuplicate {
			return err
		}
	}
	if inviterBlocked || inviterHeld {
		return nil
	}
	tiers := s.tiers(ctx)
	for l := max(2, ev.OldLevel+1); l <= ev.NewLevel; l++ {
		reward := tiers[l]
		if reward <= 0 {
			continue
		}
		ref := fmt.Sprintf("level:%d:%d", ev.PlayerID, l)
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: *inviter, Currency: ledger.Coin, Amount: reward, Reason: ledger.ReasonReferralLevel,
			RefType: "referral", RefID: ledger.RefID(ev.PlayerID), IdemKey: "reflevel:" + ref[len("level:"):], Earned: true,
			Meta: map[string]any{"friend": ev.PlayerID, "level": l}}); err != nil {
			if err == ledger.ErrDuplicate {
				continue
			}
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO referral_payments (inviter_id, friend_id, kind, level, coins, ref) VALUES ($1,$2,'level',$3,$4,$5) ON CONFLICT (ref) DO NOTHING`,
			*inviter, ev.PlayerID, l, reward, ref); err != nil {
			return err
		}
		for _, h := range s.OnReward {
			if err := h(ctx, tx, *inviter, ev.PlayerID, "level", l, reward); err != nil {
				return err
			}
		}
	}
	return nil
}

// OnCollected pays the inviter their share of a friend's collect (extra coins from the system, capped per friend).
func (s *Service) OnCollected(ctx context.Context, tx pgx.Tx, ev fishing.CollectEvent) error {
	if ev.Coins <= 0 {
		return nil
	}
	pct, err := s.settings.Float(ctx, settings.KeyRefMiningPct)
	if err != nil || pct <= 0 {
		return nil
	}
	capPer, err := s.settings.Int(ctx, settings.KeyRefMiningCap)
	if err != nil || capPer <= 0 {
		return nil
	}
	var inviter *int64
	var counted bool
	if err := tx.QueryRow(ctx, `SELECT referred_by, referral_counted FROM players WHERE id=$1`, ev.PlayerID).Scan(&inviter, &counted); err != nil {
		return err
	}
	if inviter == nil || !counted {
		return nil
	}
	var blocked, held bool
	if err := tx.QueryRow(ctx, `SELECT is_blocked, hold_rewards FROM players WHERE id=$1`, *inviter).Scan(&blocked, &held); err != nil {
		return err
	}
	if blocked || held {
		return nil
	}
	share := int64(float64(ev.Coins) * pct / 100)
	if share <= 0 {
		return nil
	}
	var earned int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(coins),0) FROM referral_payments WHERE inviter_id=$1 AND friend_id=$2 AND kind='mining'`, *inviter, ev.PlayerID).Scan(&earned); err != nil {
		return err
	}
	if left := capPer - earned; share > left {
		share = left
	}
	if share <= 0 {
		return nil
	}
	ref := fmt.Sprintf("mining:%d", ev.SessionID)
	if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: *inviter, Currency: ledger.Coin, Amount: share, Reason: ledger.ReasonReferralMining,
		RefType: "referral", RefID: ledger.RefID(ev.PlayerID), IdemKey: "refmine:" + ref, Earned: true, Meta: map[string]any{"friend": ev.PlayerID}}); err != nil {
		if err == ledger.ErrDuplicate {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO referral_payments (inviter_id, friend_id, kind, coins, ref) VALUES ($1,$2,'mining',$3,$4) ON CONFLICT (ref) DO NOTHING`,
		*inviter, ev.PlayerID, share, ref); err != nil {
		return err
	}
	for _, h := range s.OnReward {
		if err := h(ctx, tx, *inviter, ev.PlayerID, "mining", 0, share); err != nil {
			return err
		}
	}
	return nil
}

// OnTopupPaid pays the inviter a share (in stars, rounded down) of a friend's Telegram Stars top-up. It needs no level: the first
// top-up also makes the friend count. A blocked inviter, or one whose rewards are held, gets nothing (never paid later).
func (s *Service) OnTopupPaid(ctx context.Context, tx pgx.Tx, ev topup.PaidEvent) error {
	if ev.Method != topup.MethodTG || ev.Stars <= 0 {
		return nil
	}
	pct, err := s.settings.Float(ctx, settings.KeyRefTopupPct)
	if err != nil || pct <= 0 {
		return nil
	}
	var inviter *int64
	var counted bool
	if err := tx.QueryRow(ctx, `SELECT referred_by, referral_counted FROM players WHERE id=$1 FOR UPDATE`, ev.PlayerID).Scan(&inviter, &counted); err != nil {
		return err
	}
	if inviter == nil {
		return nil
	}
	if !counted {
		if _, err := tx.Exec(ctx, `UPDATE players SET referral_counted=TRUE, referral_counted_at=$2 WHERE id=$1`, ev.PlayerID, s.now()); err != nil {
			return err
		}
	}
	var blocked, held bool
	if err := tx.QueryRow(ctx, `SELECT is_blocked, hold_rewards FROM players WHERE id=$1`, *inviter).Scan(&blocked, &held); err != nil {
		return err
	}
	var paid int64
	if !blocked && !held {
		share := int64(math.Floor(float64(ev.Stars) * pct / 100))
		if share > 0 {
			ref := "topup:" + ev.OrderID
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: *inviter, Currency: ledger.Star, Amount: share, Reason: ledger.ReasonReferralTopup,
				RefType: "referral", RefID: ledger.RefID(ev.PlayerID), IdemKey: "reftopup:" + ev.OrderID, Meta: map[string]any{"friend": ev.PlayerID, "order": ev.OrderID}}); err != nil {
				if err != ledger.ErrDuplicate {
					return err
				}
			} else {
				paid = share
				if _, err := tx.Exec(ctx, `INSERT INTO referral_payments (inviter_id, friend_id, kind, coins, stars, ref) VALUES ($1,$2,'topup',0,$3,$4) ON CONFLICT (ref) DO NOTHING`,
					*inviter, ev.PlayerID, share, ref); err != nil {
					return err
				}
			}
		}
	}
	for _, h := range s.OnFriendInvested {
		if err := h(ctx, tx, *inviter, ev.PlayerID, paid, !counted); err != nil {
			return err
		}
	}
	return nil
}

// OnTopupRefunded takes the inviter's share of a refunded top-up back, as far as the inviter still has the stars.
func (s *Service) OnTopupRefunded(ctx context.Context, tx pgx.Tx, playerID int64, orderID string, _ int64) error {
	var inviter, stars int64
	err := tx.QueryRow(ctx, `SELECT inviter_id, stars FROM referral_payments WHERE ref=$1 AND kind='topup'`, "topup:"+orderID).Scan(&inviter, &stars)
	if err != nil {
		if db.IsNoRows(err) {
			return nil // no bonus was paid for this order
		}
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM players WHERE id=$1 FOR UPDATE`, inviter); err != nil {
		return err
	}
	_, have, err := ledger.Balances(ctx, tx, inviter)
	if err != nil {
		return err
	}
	back := min(stars, have)
	if back <= 0 {
		return nil
	}
	if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: inviter, Currency: ledger.Star, Amount: -back, Reason: ledger.ReasonReferralTopupBack,
		RefType: "referral", RefID: ledger.RefID(playerID), IdemKey: "reftopupback:" + orderID, Meta: map[string]any{"friend": playerID, "order": orderID}}); err != nil {
		if err == ledger.ErrDuplicate {
			return nil
		}
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO referral_payments (inviter_id, friend_id, kind, coins, stars, ref) VALUES ($1,$2,'topup',0,$3,$4) ON CONFLICT (ref) DO NOTHING`,
		inviter, playerID, -back, "topuprefund:"+orderID)
	return err
}

// ---- what the Friends screen reads

type Friend struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	FirstName string    `json:"firstName"`
	Level     int       `json:"level"`
	Counted   bool      `json:"counted"`
	CreatedAt time.Time `json:"createdAt"`
	// TopupStars is what this friend's top-ups earned the inviter so far (refunds taken off)
	TopupStars int64 `json:"topupStars"`
}

type Stats struct {
	TotalReferrals   int64 `json:"totalReferrals"`   // counted friends
	PendingReferrals int64 `json:"pendingReferrals"` // joined, not at level 2 yet
	ReferralEarnings int64 `json:"referralEarnings"`
	// TopupStars: stars earned from the friends' top-ups (refunds taken off)
	TopupStars int64 `json:"topupStars"`
}

func (s *Service) Stats(ctx context.Context, playerID int64) (*Stats, error) {
	var st Stats
	err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE referral_counted), count(*) FILTER (WHERE NOT referral_counted)
		FROM players WHERE referred_by=$1 AND deleted_at IS NULL`, playerID).Scan(&st.TotalReferrals, &st.PendingReferrals)
	if err != nil {
		return nil, err
	}
	err = s.pool.QueryRow(ctx, `SELECT COALESCE(sum(coins),0), COALESCE(sum(stars),0) FROM referral_payments WHERE inviter_id=$1`, playerID).Scan(&st.ReferralEarnings, &st.TopupStars)
	return &st, err
}

func (s *Service) Friends(ctx context.Context, playerID int64, page, perPage int) ([]Friend, int64, error) {
	if perPage <= 0 || perPage > 50 {
		perPage = 20
	}
	if page < 1 {
		page = 1
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM players WHERE referred_by=$1 AND deleted_at IS NULL`, playerID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, username, first_name, level, referral_counted, created_at,
		(SELECT COALESCE(sum(stars),0) FROM referral_payments rp WHERE rp.inviter_id=$1 AND rp.friend_id=players.id AND rp.kind='topup')
		FROM players
		WHERE referred_by=$1 AND deleted_at IS NULL ORDER BY referral_counted DESC, level DESC, id DESC LIMIT $2 OFFSET $3`, playerID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Friend{}
	for rows.Next() {
		var f Friend
		if err := rows.Scan(&f.ID, &f.Username, &f.FirstName, &f.Level, &f.Counted, &f.CreatedAt, &f.TopupStars); err != nil {
			return nil, 0, err
		}
		out = append(out, f)
	}
	return out, total, rows.Err()
}

type Tier struct {
	Level  int   `json:"level"`
	Reward int64 `json:"reward"`
}

// Rewards is the table "friend reaches level N -> you get X coins".
func (s *Service) Rewards(ctx context.Context) []Tier {
	t := s.tiers(ctx)
	out := []Tier{}
	for l := 2; l <= 10; l++ {
		if r, ok := t[l]; ok {
			out = append(out, Tier{Level: l, Reward: r})
		}
	}
	return out
}

// TopupPercent is the share (percent) of a friend's Stars top-up the inviter earns in stars (0 = off).
func (s *Service) TopupPercent(ctx context.Context) float64 {
	pct, err := s.settings.Float(ctx, settings.KeyRefTopupPct)
	if err != nil || pct < 0 {
		return 0
	}
	return pct
}

type Links struct {
	MiniApp string `json:"inviteLink"`
	Bot     string `json:"botLink"`
}

func (s *Service) Links(playerID int64) Links {
	return Links{
		MiniApp: fmt.Sprintf("https://t.me/%s/%s?startapp=ref_%d", s.botName, s.appName, playerID),
		Bot:     fmt.Sprintf("https://t.me/%s?start=ref_%d", s.botName, playerID),
	}
}

// Remember stores an invite that came through the bot's /start before the person ever opened the app.
func (s *Service) Remember(ctx context.Context, telegramID, inviterID int64) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM players WHERE telegram_id=$1)`, telegramID).Scan(&exists); err != nil || exists {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO pending_referrals (telegram_id, inviter_id) VALUES ($1,$2) ON CONFLICT (telegram_id) DO NOTHING`, telegramID, inviterID)
	return err
}

var _ = players.ParseReferral
var _ = db.IsNoRows
