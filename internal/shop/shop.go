// Package shop holds the two upgrade cards (mining rate, coin capacity), the player level they decide, and the boosters.
package shop

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/fishing"
	"brunoplay/internal/gamecfg"
	"brunoplay/internal/ledger"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
)

// ReqEvaluator decides whether a card requirement is met (task_done is registered by the tasks module).
type ReqEvaluator func(ctx context.Context, q db.Querier, p *players.Player, value int64) (bool, error)

// LevelUpEvent is passed to the hooks when the player's level rises.
type LevelUpEvent struct {
	PlayerID   int64
	OldLevel   int
	NewLevel   int
	RewardCoin int64
	RewardStar int64
}

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	cfg      gamecfg.Config
	players  *players.Store
	fishing  *fishing.Service
	Now      func() time.Time

	Evaluators map[string]ReqEvaluator
	// OnLevelUp hooks run inside the upgrade transaction (referral rewards, notifications).
	OnLevelUp []func(ctx context.Context, tx pgx.Tx, ev LevelUpEvent) error
	// OnBooster runs inside the purchase transaction (the bot schedules the "ends soon" messages).
	OnBooster []func(ctx context.Context, tx pgx.Tx, playerID int64, typ string, expires time.Time) error
}

func NewService(pool *pgxpool.Pool, st *settings.Service, ps *players.Store, f *fishing.Service) *Service {
	s := &Service{pool: pool, settings: st, cfg: gamecfg.Config{S: st}, players: ps, fishing: f, Now: time.Now}
	s.Evaluators = map[string]ReqEvaluator{
		"invite_friends": func(ctx context.Context, q db.Querier, p *players.Player, v int64) (bool, error) {
			var n int64
			err := q.QueryRow(ctx, `SELECT count(*) FROM players WHERE referred_by=$1 AND referral_counted`, p.ID).Scan(&n)
			return n >= v, err
		},
		"player_level": func(ctx context.Context, q db.Querier, p *players.Player, v int64) (bool, error) {
			return int64(p.Level) >= v, nil
		},
	}
	return s
}

func (s *Service) now() time.Time { return s.Now() }

// ---- cards

type ReqView struct {
	Description      string `json:"description"`
	RequirementType  string `json:"requirementType"`
	RequirementValue string `json:"requirementValue"`
	Met              bool   `json:"met"`
}

type CardInfo struct {
	CardName        string    `json:"cardName"`
	CurrentLevel    int       `json:"currentLevel"`
	CurrentValue    float64   `json:"currentValue"`
	NextLevel       *int      `json:"nextLevel"`
	NextValue       *float64  `json:"nextValue"`
	UpgradeCost     *int64    `json:"upgradeCost"`
	UpgradeCurrency string    `json:"upgradeCurrency"`
	CanUpgrade      bool      `json:"canUpgrade"`
	Requirements    []ReqView `json:"requirements"`
}

func cardLevel(p *players.Player, card string) int {
	if card == gamecfg.CardMining {
		return p.MiningLevel
	}
	return p.CapacityLevel
}

func otherCard(card string) string {
	if card == gamecfg.CardMining {
		return gamecfg.CardCapacity
	}
	return gamecfg.CardMining
}

// requirementText is the line shown when a condition is unmet; the admin's own text wins.
func requirementText(r gamecfg.Requirement, lang apperr.Lang) string {
	if lang == apperr.RU && r.DescRU != "" {
		return r.DescRU
	}
	if lang != apperr.RU && r.DescEN != "" {
		return r.DescEN
	}
	if r.DescEN != "" && lang == apperr.RU {
		return r.DescEN
	}
	ru := lang == apperr.RU
	switch r.Type {
	case "invite_friends":
		if ru {
			return fmt.Sprintf("Пригласите друзей: %d", r.Value)
		}
		return fmt.Sprintf("Invite %d friends", r.Value)
	case "player_level":
		if ru {
			return fmt.Sprintf("Достигните уровня %d", r.Value)
		}
		return fmt.Sprintf("Reach level %d", r.Value)
	case "other_card_level":
		if ru {
			return fmt.Sprintf("Улучшите вторую карточку до уровня %d", r.Value)
		}
		return fmt.Sprintf("Upgrade the other card to level %d", r.Value)
	case "task_done":
		if ru {
			return "Выполните задание"
		}
		return "Complete the task"
	}
	return r.Type
}

func (s *Service) evalReqs(ctx context.Context, q db.Querier, p *players.Player, card string, reqs []gamecfg.Requirement) ([]ReqView, bool, error) {
	out := make([]ReqView, 0, len(reqs))
	all := true
	for _, r := range reqs {
		var met bool
		var err error
		if r.Type == "other_card_level" {
			met = int64(cardLevel(p, otherCard(card))) >= r.Value
		} else if ev, ok := s.Evaluators[r.Type]; ok {
			met, err = ev(ctx, q, p, r.Value)
			if err != nil {
				return nil, false, err
			}
		}
		if !met {
			all = false
		}
		out = append(out, ReqView{Description: requirementText(r, p.Lang()), RequirementType: r.Type, RequirementValue: fmt.Sprint(r.Value), Met: met})
	}
	return out, all, nil
}

// Cards lists both cards with the current and next level.
func (s *Service) Cards(ctx context.Context, q db.Querier, p *players.Player) ([]CardInfo, error) {
	var out []CardInfo
	for _, card := range []string{gamecfg.CardMining, gamecfg.CardCapacity} {
		t, err := s.cfg.Card(ctx, card)
		if err != nil {
			return nil, err
		}
		lvl := cardLevel(p, card)
		if lvl > len(t) {
			lvl = len(t)
		}
		ci := CardInfo{CardName: card, CurrentLevel: lvl, CurrentValue: t[lvl-1].Value, Requirements: []ReqView{}}
		if lvl < len(t) {
			next := t[lvl]
			nl, nv, cost := next.Level, next.Value, next.Cost
			ci.NextLevel, ci.NextValue, ci.UpgradeCost, ci.UpgradeCurrency = &nl, &nv, &cost, next.Currency
			reqs, all, err := s.evalReqs(ctx, q, p, card, next.Requirements)
			if err != nil {
				return nil, err
			}
			ci.Requirements = reqs
			bal := p.Coins
			if next.Currency == "STAR" {
				bal = p.Stars
			}
			ci.CanUpgrade = all && bal >= cost
		}
		out = append(out, ci)
	}
	return out, nil
}

type UpgradeResult struct {
	CardName     string  `json:"cardName"`
	NewLevel     int     `json:"newLevel"`
	NewValue     float64 `json:"newValue"`
	NewUserLevel int     `json:"newUserLevel"`
	LevelUp      bool    `json:"levelUp"`
	RewardCoins  int64   `json:"rewardCoins"`
	RewardStars  int64   `json:"rewardStars"`
	Coins        int64   `json:"coins"`
	Stars        int64   `json:"stars"`
}

// Upgrade raises a card by one level: conditions, price, level, player level and its reward, and the running session — all in one transaction.
func (s *Service) Upgrade(ctx context.Context, playerID int64, card string) (*UpgradeResult, error) {
	if card != gamecfg.CardMining && card != gamecfg.CardCapacity {
		return nil, apperr.NotFound
	}
	t, err := s.cfg.Card(ctx, card)
	if err != nil {
		return nil, err
	}
	rewards, err := s.cfg.LevelRewards(ctx)
	if err != nil {
		return nil, err
	}
	var res *UpgradeResult
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.players.Lock(ctx, tx, playerID)
		if err != nil {
			return err
		}
		lvl := cardLevel(p, card)
		if lvl >= len(t) {
			return apperr.CardMaxLevel
		}
		next := t[lvl]
		_, all, err := s.evalReqs(ctx, tx, p, card, next.Requirements)
		if err != nil {
			return err
		}
		if !all {
			return apperr.ReqNotMet
		}
		cur := ledger.Coin
		if next.Currency == "STAR" {
			cur = ledger.Star
		}
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: cur, Amount: -next.Cost, Reason: ledger.ReasonCardUpgrade,
			RefType: "card", RefID: fmt.Sprintf("%s:%d", card, next.Level), Meta: map[string]any{"card": card, "level": next.Level}}); err != nil {
			return err
		}
		col := "capacity_level"
		if card == gamecfg.CardMining {
			col = "mining_level"
		}
		if _, err := tx.Exec(ctx, `UPDATE players SET `+col+`=$2 WHERE id=$1`, playerID, next.Level); err != nil {
			return err
		}
		p, err = s.players.Get(ctx, tx, playerID)
		if err != nil {
			return err
		}
		newLevel := min(p.MiningLevel, p.CapacityLevel)
		res = &UpgradeResult{CardName: card, NewLevel: next.Level, NewValue: next.Value, NewUserLevel: newLevel}
		if newLevel > p.Level {
			old := p.Level
			if _, err := tx.Exec(ctx, `UPDATE players SET level=$2 WHERE id=$1`, playerID, newLevel); err != nil {
				return err
			}
			p.Level = newLevel
			res.LevelUp = true
			ev := LevelUpEvent{PlayerID: playerID, OldLevel: old, NewLevel: newLevel}
			// pay the reward of every level passed (normally one)
			for l := old + 1; l <= newLevel; l++ {
				r, ok := rewards[l]
				if !ok {
					continue
				}
				if r.Coins > 0 {
					if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: r.Coins, Reason: ledger.ReasonLevelReward,
						RefType: "level", RefID: fmt.Sprint(l), IdemKey: fmt.Sprintf("levelreward:coin:%d:%d", playerID, l), Earned: true}); err != nil && err != ledger.ErrDuplicate {
						return err
					} else if err == nil {
						res.RewardCoins += r.Coins
					}
				}
				if r.Stars > 0 {
					if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: r.Stars, Reason: ledger.ReasonLevelReward,
						RefType: "level", RefID: fmt.Sprint(l), IdemKey: fmt.Sprintf("levelreward:star:%d:%d", playerID, l)}); err != nil && err != ledger.ErrDuplicate {
						return err
					} else if err == nil {
						res.RewardStars += r.Stars
					}
				}
			}
			ev.RewardCoin, ev.RewardStar = res.RewardCoins, res.RewardStars
			for _, h := range s.OnLevelUp {
				if err := h(ctx, tx, ev); err != nil {
					return err
				}
			}
		}
		if err := s.fishing.Resettle(ctx, tx, p, s.now()); err != nil {
			return err
		}
		res.Coins, res.Stars, err = ledger.Balances(ctx, tx, playerID)
		return err
	})
	return res, err
}

// ---- levels

type LevelView struct {
	Level       int    `json:"level"`
	Name        string `json:"name"`
	NameEN      string `json:"nameEn"`
	NameRU      string `json:"nameRu"`
	RewardCoins int64  `json:"rewardCoins"`
	RewardStars int64  `json:"rewardStars"`
}

func (s *Service) Levels(ctx context.Context, lang apperr.Lang) ([]LevelView, error) {
	rewards, err := s.cfg.LevelRewards(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LevelView, 0, gamecfg.MaxLevel)
	for i, n := range gamecfg.LevelNames {
		l := i + 1
		name := n[0]
		if lang == apperr.RU {
			name = n[1]
		}
		r := rewards[l]
		out = append(out, LevelView{Level: l, Name: name, NameEN: n[0], NameRU: n[1], RewardCoins: r.Coins, RewardStars: r.Stars})
	}
	return out, nil
}

// ---- boosters

type boosterDef struct {
	Type     string
	NameEN   string
	NameRU   string
	DescEN   string
	DescRU   string
	PriceKey string
}

var boosterDefs = []boosterDef{
	{fishing.BoostAuto, "Auto Collect", "Автосбор", "Collects your coins for you, no tapping, no ads", "Собирает монеты за вас: без нажатий и без рекламы", settings.KeyBoosterAutoCost},
	{fishing.BoostMining, "Booster", "Бустер", "Fish 1.5 times faster", "Ловите рыбу в 1,5 раза быстрее", settings.KeyBoosterMineCost},
}

func boosterDefOf(t string) (boosterDef, bool) {
	for _, d := range boosterDefs {
		if d.Type == t {
			return d, true
		}
	}
	return boosterDef{}, false
}

type BoosterInfo struct {
	Type            string     `json:"type"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	CostStars       int64      `json:"costStars"`
	DurationMinutes int64      `json:"durationMinutes"`
	IsActive        bool       `json:"isActive"`
	ExpiresAt       *time.Time `json:"expiresAt"`
	CanBuy          bool       `json:"canBuy"`
}

func (s *Service) activeUntil(ctx context.Context, q db.Querier, playerID int64, typ string, now time.Time) (*time.Time, error) {
	var exp time.Time
	err := q.QueryRow(ctx, `SELECT expires_at FROM player_boosters WHERE player_id=$1 AND type=$2 AND starts_at <= $3 AND expires_at > $3 ORDER BY expires_at DESC LIMIT 1`,
		playerID, typ, now).Scan(&exp)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return &exp, err
}

func (s *Service) Boosters(ctx context.Context, q db.Querier, p *players.Player) ([]BoosterInfo, error) {
	days, err := s.settings.Int(ctx, settings.KeyBoosterDays)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out []BoosterInfo
	for _, d := range boosterDefs {
		price, perr := s.settings.Int(ctx, d.PriceKey)
		if perr != nil && !apperr.Is(perr, apperr.ConfigMissing) {
			return nil, perr
		}
		exp, err := s.activeUntil(ctx, q, p.ID, d.Type, now)
		if err != nil {
			return nil, err
		}
		bi := BoosterInfo{Type: d.Type, Name: d.NameEN, Description: d.DescEN, CostStars: price, DurationMinutes: days * 24 * 60,
			IsActive: exp != nil, ExpiresAt: exp, CanBuy: exp == nil && perr == nil && p.Stars >= price}
		if p.Lang() == apperr.RU {
			bi.Name, bi.Description = d.NameRU, d.DescRU
		}
		out = append(out, bi)
	}
	return out, nil
}

// BuyBooster charges the stars and starts a booster for the configured number of days. The same kind cannot be bought while active.
func (s *Service) BuyBooster(ctx context.Context, playerID int64, typ string) (*BoosterInfo, error) {
	d, ok := boosterDefOf(typ)
	if !ok {
		return nil, apperr.BoosterNotFound
	}
	price, err := s.settings.Int(ctx, d.PriceKey)
	if err != nil {
		return nil, err
	}
	days, err := s.settings.Int(ctx, settings.KeyBoosterDays)
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.players.Lock(ctx, tx, playerID)
		if err != nil {
			return err
		}
		if exp, err := s.activeUntil(ctx, tx, playerID, typ, now); err != nil {
			return err
		} else if exp != nil {
			return apperr.BoosterActive
		}
		if price > 0 {
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: -price, Reason: ledger.ReasonBoosterBuy,
				RefType: "booster", RefID: typ}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO player_boosters (player_id, type, starts_at, expires_at, price_stars) VALUES ($1,$2,$3,$4,$5)`,
			playerID, typ, now, now.Add(time.Duration(days)*24*time.Hour), price); err != nil {
			return err
		}
		for _, h := range s.OnBooster {
			if err := h(ctx, tx, playerID, typ, now.Add(time.Duration(days)*24*time.Hour)); err != nil {
				return err
			}
		}
		if typ == fishing.BoostMining {
			return s.fishing.Resettle(ctx, tx, p, now)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	p, err := s.players.Get(ctx, s.pool, playerID)
	if err != nil {
		return nil, err
	}
	list, err := s.Boosters(ctx, s.pool, p)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Type == typ {
			return &list[i], nil
		}
	}
	return nil, apperr.BoosterNotFound
}
