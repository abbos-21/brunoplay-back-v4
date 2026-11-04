package games

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// The wheel has two outcomes. A spin costs the tier's price and either pays nothing or, with the chance of spin.win_chance_percent,
// pays the price times spin.win_multiplier in stars (a multiplier of 2 doubles the stars). Every spin of a pack is its own draw.

// SpinCounts are the pack sizes the wheel sells.
var SpinCounts = []int{1, 3, 5, 10}

type tierCfg struct {
	Tier   string `json:"tier"`
	Price  int64  `json:"price_stars"`
	NameEN string `json:"name_en"`
	NameRU string `json:"name_ru"`
}

func (s *Service) tiers(ctx context.Context) ([]tierCfg, error) {
	t, err := settings.Get[[]tierCfg](ctx, s.settings, settings.KeySpinTiers)
	if err != nil {
		return nil, err
	}
	if len(t) == 0 {
		return nil, apperr.ConfigMissing.With("key", settings.KeySpinTiers)
	}
	return t, nil
}

// spinRules reads the win chance (percent) and the multiplier of the wheel.
func (s *Service) spinRules(ctx context.Context) (chance, mult float64, err error) {
	if chance, err = s.settings.Float(ctx, settings.KeySpinWinChance); err != nil {
		return 0, 0, err
	}
	if mult, err = s.settings.Float(ctx, settings.KeySpinWinMult); err != nil {
		return 0, 0, err
	}
	return chance, mult, nil
}

// winAmount is what a win pays: the price times the multiplier, in whole stars.
func winAmount(price int64, mult float64) int64 {
	return int64(math.Round(float64(price) * mult))
}

// prizeSlug names a prize in the draw log: "stars:60" or "empty".
func prizeSlug(p Prize) string {
	if p.Type == TypeEmpty {
		return TypeEmpty
	}
	return fmt.Sprintf("%s:%d", p.Type, p.Value)
}

// prizeLabel is the text shown for a prize in the player's language.
func prizeLabel(p Prize, lang apperr.Lang) string {
	switch p.Type {
	case TypeEmpty:
		if lang == apperr.RU {
			return "Пусто"
		}
		return "Empty"
	case TypeCoins:
		if lang == apperr.RU {
			return "Монеты"
		}
		return "Coins"
	}
	if lang == apperr.RU {
		return "Звёзды"
	}
	return "Stars"
}

type PrizeView struct {
	Type   string  `json:"type"`
	Value  int64   `json:"value"`
	Weight float64 `json:"weight"`
	Label  string  `json:"label"`
	Gift   string  `json:"gift,omitempty"`
}

type TierView struct {
	Tier       string      `json:"tier"`
	Name       string      `json:"name"`
	CostStars  int64       `json:"costStars"`
	Multiplier float64     `json:"multiplier"`
	Prizes     []PrizeView `json:"prizes"`
}

// Tiers lists the wheel's price levels. Each has two outcomes: the win (the price times the multiplier, in stars) and nothing.
// The chances are shown only when the admin chose to publish them.
func (s *Service) Tiers(ctx context.Context, lang apperr.Lang) ([]TierView, error) {
	ts, err := s.tiers(ctx)
	if err != nil {
		return nil, err
	}
	chance, mult, err := s.spinRules(ctx)
	if err != nil {
		return nil, err
	}
	publish := s.settings.BoolOr(ctx, settings.KeySpinPublishOdds, false)
	winW, lossW := 1.0, 1.0
	if publish {
		winW, lossW = chance, 100-chance
	}
	out := make([]TierView, 0, len(ts))
	for i, t := range ts {
		name := t.NameEN
		if lang == apperr.RU && t.NameRU != "" {
			name = t.NameRU
		}
		if name == "" {
			name = fmt.Sprintf("Tier %d", i+1)
		}
		win := Prize{Type: TypeStars, Value: winAmount(t.Price, mult)}
		none := Prize{Type: TypeEmpty}
		out = append(out, TierView{Tier: t.Tier, Name: name, CostStars: t.Price, Multiplier: mult, Prizes: []PrizeView{
			{Type: win.Type, Value: win.Value, Weight: winW, Label: prizeLabel(win, lang)},
			{Type: none.Type, Weight: lossW, Label: prizeLabel(none, lang)},
		}})
	}
	return out, nil
}

type SpinPrize struct {
	Type      string `json:"type"`
	Value     int64  `json:"value"`
	Label     string `json:"label"`
	Gift      string `json:"gift,omitempty"`
	GiftID    int64  `json:"giftId,omitempty"`
	StarValue int64  `json:"starValue"`
}

type SpinResult struct {
	Results       []SpinPrize `json:"results"`
	TotalCoinsWon int64       `json:"totalCoinsWon"`
	TotalStarsWon int64       `json:"totalStarsWon"`
	Spent         int64       `json:"spent"`
	Stars         int64       `json:"stars"`
}

func batchID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Spin charges the whole pack, then draws every spin. Each spin is independent; the payout guard may turn a win into a loss.
func (s *Service) Spin(ctx context.Context, playerID int64, tier string, count int, lang apperr.Lang) (*SpinResult, error) {
	okCount := false
	for _, c := range SpinCounts {
		if c == count {
			okCount = true
		}
	}
	if !okCount {
		return nil, apperr.SpinInvalid
	}
	ts, err := s.tiers(ctx)
	if err != nil {
		return nil, err
	}
	var cfg *tierCfg
	for i := range ts {
		if ts[i].Tier == tier {
			cfg = &ts[i]
		}
	}
	if cfg == nil {
		return nil, apperr.SpinInvalid
	}
	chance, mult, err := s.spinRules(ctx)
	if err != nil {
		return nil, err
	}
	win := winAmount(cfg.Price, mult)
	total := cfg.Price * int64(count)
	batch := batchID()
	var res *SpinResult
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
			return err
		}
		if total > 0 {
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: -total, Reason: ledger.ReasonSpinCharge,
				RefType: "spin", RefID: batch, Meta: map[string]any{"tier": tier, "count": count}}); err != nil {
				return err
			}
			if err := s.recordSpend(ctx, tx, GameSpin, tier, total); err != nil {
				return err
			}
			if err := s.spent(ctx, tx, playerID, GameSpin, total); err != nil {
				return err
			}
		}
		res = &SpinResult{Spent: total}
		for i := 0; i < count; i++ {
			drawn := Prize{Type: TypeEmpty}
			if randFloat()*100 < chance {
				drawn = Prize{Type: TypeStars, Value: win}
			}
			prize := drawn
			if drawn.Type == TypeStars {
				ok, err := s.winGuard(ctx, tx, playerID, GameSpin, tier, win)
				if err != nil {
					return err
				}
				if !ok {
					prize = Prize{Type: TypeEmpty}
				}
			}
			if err := s.recordPaid(ctx, tx, GameSpin, tier, prize.Value); err != nil {
				return err
			}
			if err := s.payPrize(ctx, tx, playerID, prize, batch, i); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO spin_draws (player_id, tier, batch, gift_key, star_value, price, drawn_key, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				playerID, tier, batch, prizeSlug(prize), prize.Value, cfg.Price, prizeSlug(drawn), s.now()); err != nil {
				return err
			}
			if err := s.feed(ctx, tx, playerID, GameSpin, prize, cfg.Price); err != nil {
				return err
			}
			if prize.Type == TypeStars {
				res.TotalStarsWon += prize.Value
			}
			res.Results = append(res.Results, SpinPrize{Type: prize.Type, Value: prize.Value, Label: prizeLabel(prize, lang), StarValue: prize.Value})
		}
		_, res.Stars, err = ledger.Balances(ctx, tx, playerID)
		return err
	})
	return res, err
}

// payPrize pays one spin: stars straight to the balance, nothing for an empty spin. Each draw of a purchase has its own
// idempotency key, so a repeated request can never pay it twice.
func (s *Service) payPrize(ctx context.Context, tx pgx.Tx, playerID int64, p Prize, batch string, idx int) error {
	if p.Type != TypeStars || p.Value <= 0 {
		return nil
	}
	_, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: p.Value, Reason: ledger.ReasonSpinPrize,
		RefType: "spin", RefID: batch, IdemKey: fmt.Sprintf("spin:%s:%d", batch, idx)})
	if err != nil && err != ledger.ErrDuplicate {
		return err
	}
	return nil
}
