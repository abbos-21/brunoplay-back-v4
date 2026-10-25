// Package players stores the player accounts and handles sign-in (create on first entry, referral attribution).
package players

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/settings"
	"brunoplay/internal/tgauth"
)

type Player struct {
	ID               int64
	TelegramID       int64
	Username         string
	FirstName        string
	LastName         string
	PhotoURL         string
	Language         string // EN | RU
	IsPremium        bool
	Coins            int64
	Stars            int64
	TotalCoinsEarned int64
	Level            int
	MiningLevel      int
	CapacityLevel    int
	BestRideScore    int64
	IsBlocked        bool
	BlockReason      string
	HoldWithdrawals  bool
	HoldRewards      bool
	RiskScore        int
	ReferredBy       *int64
	ReferralCounted  bool
	InviteBonusCoins *int64
	FriendBonusCoins int64
	BotStarted       bool
	BotBlocked       bool
	NotifAllOff      bool
	CreatedAt        time.Time
	LastSeenAt       time.Time
}

func (p *Player) Lang() apperr.Lang { return apperr.ParseLang(p.Language) }

const cols = `id, telegram_id, username, first_name, last_name, photo_url, language, is_premium, coins, stars,
	total_coins_earned, level, mining_level, capacity_level, best_ride_score, is_blocked, block_reason,
	hold_withdrawals, hold_rewards, risk_score, referred_by, referral_counted, invite_bonus_coins, friend_bonus_coins,
	bot_started, bot_blocked, notif_all_off, created_at, last_seen_at`

func scan(r pgx.Row) (*Player, error) {
	var p Player
	err := r.Scan(&p.ID, &p.TelegramID, &p.Username, &p.FirstName, &p.LastName, &p.PhotoURL, &p.Language, &p.IsPremium,
		&p.Coins, &p.Stars, &p.TotalCoinsEarned, &p.Level, &p.MiningLevel, &p.CapacityLevel, &p.BestRideScore,
		&p.IsBlocked, &p.BlockReason, &p.HoldWithdrawals, &p.HoldRewards, &p.RiskScore, &p.ReferredBy,
		&p.ReferralCounted, &p.InviteBonusCoins, &p.FriendBonusCoins, &p.BotStarted, &p.BotBlocked, &p.NotifAllOff,
		&p.CreatedAt, &p.LastSeenAt)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, apperr.NotFound
		}
		return nil, err
	}
	return &p, nil
}

type Store struct {
	pool     *pgxpool.Pool
	settings *settings.Service
}

func NewStore(pool *pgxpool.Pool, s *settings.Service) *Store {
	return &Store{pool: pool, settings: s}
}

func (s *Store) Get(ctx context.Context, q db.Querier, id int64) (*Player, error) {
	return scan(q.QueryRow(ctx, `SELECT `+cols+` FROM players WHERE id=$1 AND deleted_at IS NULL`, id))
}

func (s *Store) ByTelegram(ctx context.Context, q db.Querier, tgID int64) (*Player, error) {
	return scan(q.QueryRow(ctx, `SELECT `+cols+` FROM players WHERE telegram_id=$1 AND deleted_at IS NULL`, tgID))
}

// Lock selects the player row FOR UPDATE inside a transaction (serialises money operations of one player).
func (s *Store) Lock(ctx context.Context, tx db.Querier, id int64) (*Player, error) {
	return scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM players WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id))
}

type LoginInput struct {
	User       tgauth.User
	StartParam string
	IP         string
	Now        time.Time
}

// ParseReferral extracts the inviter id from "ref_123" (Mini App start_param or bot /start payload).
func ParseReferral(param string) (int64, bool) {
	param = strings.TrimSpace(param)
	if !strings.HasPrefix(param, "ref_") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(param, "ref_"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func languageFor(code string) string {
	if strings.HasPrefix(strings.ToLower(code), "ru") {
		return "RU"
	}
	return "EN"
}

// Login creates the player on first entry or refreshes the profile. It returns whether the player is new.
func (s *Store) Login(ctx context.Context, in LoginInput) (*Player, bool, error) {
	var out *Player
	isNew := false
	run := func() error {
		return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
			isNew = false
			p, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM players WHERE telegram_id=$1 AND deleted_at IS NULL FOR UPDATE`, in.User.ID))
			if err != nil && !apperr.Is(err, apperr.NotFound) {
				return err
			}
			if p != nil {
				_, err = tx.Exec(ctx, `UPDATE players p SET username=$2, first_name=$3, last_name=$4,
					photo_url=CASE WHEN $5 <> '' THEN $5 ELSE p.photo_url END, is_premium=$6, last_seen_at=$7, last_ip=$8,
					active_hours = x.a, pref_hour = (SELECT h FROM unnest(x.a) h GROUP BY h ORDER BY count(*) DESC, h LIMIT 1),
					bot_started = p.bot_started OR EXISTS (SELECT 1 FROM bot_users b WHERE b.telegram_id = p.telegram_id)
					FROM (SELECT (active_hours || ARRAY[$9::int])[greatest(1, cardinality(active_hours)-8):cardinality(active_hours)+1] AS a FROM players WHERE id=$1) x
					WHERE p.id=$1`,
					p.ID, in.User.Username, in.User.FirstName, in.User.LastName, in.User.PhotoURL, in.User.IsPremium,
					in.Now, in.IP, in.Now.UTC().Hour())
				if err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `INSERT INTO player_days (player_id, day) VALUES ($1,$2) ON CONFLICT DO NOTHING`, p.ID, in.Now.UTC().Truncate(24*time.Hour)); err != nil {
					return err
				}
				out, err = s.Get(ctx, tx, p.ID)
				return err
			}

			// first entry: attribute the inviter (only brand-new players can be attributed)
			var referredBy *int64
			var friendBonus int64
			inviterID, ok := ParseReferral(in.StartParam)
			if !ok {
				// the person tapped the bot's invite link first: use what the bot remembered (for a week)
				err := tx.QueryRow(ctx, `SELECT inviter_id FROM pending_referrals WHERE telegram_id=$1 AND created_at > $2`, in.User.ID, in.Now.Add(-7*24*time.Hour)).Scan(&inviterID)
				if err != nil && !db.IsNoRows(err) {
					return err
				}
				ok = err == nil
			}
			if ok {
				var inviterBonus *int64
				err := tx.QueryRow(ctx, `SELECT invite_bonus_coins FROM players WHERE id=$1 AND deleted_at IS NULL AND NOT is_blocked`, inviterID).Scan(&inviterBonus)
				if err == nil {
					referredBy = &inviterID
					if inviterBonus != nil {
						friendBonus = *inviterBonus
					} else if v, serr := s.settings.Int(ctx, settings.KeyRefJoinBonus); serr == nil {
						friendBonus = v
					}
				} else if !db.IsNoRows(err) {
					return err
				}
				_, _ = tx.Exec(ctx, `DELETE FROM pending_referrals WHERE telegram_id=$1`, in.User.ID)
			}
			var id int64
			err = tx.QueryRow(ctx, `INSERT INTO players (telegram_id, username, first_name, last_name, photo_url, language, is_premium,
				referred_by, friend_bonus_coins, last_ip, active_hours, pref_hour, bot_started, created_at, last_seen_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, ARRAY[$11::int], $11::int, EXISTS (SELECT 1 FROM bot_users b WHERE b.telegram_id=$1), $12, $12) RETURNING id`,
				in.User.ID, in.User.Username, in.User.FirstName, in.User.LastName, in.User.PhotoURL,
				languageFor(in.User.LanguageCode), in.User.IsPremium, referredBy, friendBonus, in.IP,
				in.Now.UTC().Hour(), in.Now).Scan(&id)
			if err != nil {
				return err
			}
			isNew = true
			if _, err = tx.Exec(ctx, `INSERT INTO player_days (player_id, day) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, in.Now.UTC().Truncate(24*time.Hour)); err != nil {
				return err
			}
			out, err = s.Get(ctx, tx, id)
			return err
		})
	}
	err := run()
	if err != nil && db.IsUnique(err, "players_telegram_id_key") { // two first requests at once
		err = run()
	}
	return out, isNew, err
}

// SetLanguage stores EN or RU.
func (s *Store) SetLanguage(ctx context.Context, id int64, lang string) error {
	lang = strings.ToUpper(lang)
	if lang != "EN" && lang != "RU" {
		return apperr.Validation.With("field", "language")
	}
	_, err := s.pool.Exec(ctx, `UPDATE players SET language=$2 WHERE id=$1`, id, lang)
	return err
}

// SetBlocked blocks or unblocks a player (admin action).
func (s *Store) SetBlocked(ctx context.Context, id int64, blocked bool, reason string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE players SET is_blocked=$2, block_reason=$3 WHERE id=$1`, id, blocked, reason)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return apperr.NotFound
	}
	return nil
}
