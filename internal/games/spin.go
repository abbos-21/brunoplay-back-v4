package games

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

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

// prizeFromKey reads one entry of the odds table: a gift key ("heart"), or an amount of coins ("coins:500") or stars ("stars:25").
func prizeFromKey(key string, gifts map[string]Gift) (Prize, bool) {
	if kind, amount, ok := strings.Cut(key, ":"); ok {
		n, err := strconv.ParseInt(amount, 10, 64)
		if err != nil || n <= 0 {
			return Prize{}, false
		}
		switch kind {
		case "coins":
			return Prize{Type: TypeCoins, Value: n}, true
		case "stars":
			return Prize{Type: TypeStars, Value: n}, true
		}
		return Prize{}, false
	}
	g, ok := gifts[key]
	if !ok {
		return Prize{}, false
	}
	return Prize{Type: TypeGift, Gift: key, Value: g.StarValue}, true
}

// prizeSlug names a prize in the draw log: the gift key, or "coins:500" / "stars:25".
func prizeSlug(p Prize) string {
	if p.Type == TypeGift {
		return p.Gift
	}
	return fmt.Sprintf("%s:%d", p.Type, p.Value)
}

// prizeLabel is the text shown for a prize in the player's language.
func prizeLabel(p Prize, gifts map[string]Gift, lang apperr.Lang) string {
	switch p.Type {
	case TypeCoins:
		if lang == apperr.RU {
			return "Монеты"
		}
		return "Coins"
	case TypeStars:
		if lang == apperr.RU {
			return "Звёзды"
		}
		return "Stars"
	}
	return gifts[p.Gift].Name(lang)
}

// tierPool builds the prizes of one tier from the odds table: gifts, coins and stars.
func (s *Service) tierPool(ctx context.Context, tier string) ([]Prize, error) {
	odds, err := settings.Get[map[string]map[string]float64](ctx, s.settings, settings.KeySpinOdds)
	if err != nil {
		return nil, err
	}
	row, ok := odds[tier]
	if !ok || len(row) == 0 {
		return nil, apperr.ConfigMissing.With("key", settings.KeySpinOdds)
	}
	_, gifts, err := s.Gifts(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Prize
	for _, k := range keys {
		p, ok := prizeFromKey(k, gifts)
		if !ok || row[k] <= 0 {
			continue
		}
		p.Weight = row[k]
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, apperr.ConfigMissing.With("key", settings.KeySpinOdds)
	}
	return out, nil
}

type PrizeView struct {
	Type   string  `json:"type"`
	Value  int64   `json:"value"`
	Weight float64 `json:"weight"`
	Label  string  `json:"label"`
	Gift   string  `json:"gift,omitempty"`
}

type TierView struct {
	Tier      string      `json:"tier"`
	Name      string      `json:"name"`
	CostStars int64       `json:"costStars"`
	Prizes    []PrizeView `json:"prizes"`
}

// Tiers lists the wheel's price levels with the gifts each can give (odds only when the admin chose to publish them).
func (s *Service) Tiers(ctx context.Context, lang apperr.Lang) ([]TierView, error) {
	ts, err := s.tiers(ctx)
	if err != nil {
		return nil, err
	}
	_, gifts, err := s.Gifts(ctx)
	if err != nil {
		return nil, err
	}
	publish := s.settings.BoolOr(ctx, settings.KeySpinPublishOdds, false)
	out := make([]TierView, 0, len(ts))
	for i, t := range ts {
		pool, err := s.tierPool(ctx, t.Tier)
		if err != nil {
			return nil, err
		}
		name := t.NameEN
		if lang == apperr.RU && t.NameRU != "" {
			name = t.NameRU
		}
		if name == "" {
			name = fmt.Sprintf("Tier %d", i+1)
		}
		v := TierView{Tier: t.Tier, Name: name, CostStars: t.Price}
		for _, p := range pool {
			w := 1.0
			if publish {
				w = p.Weight
			}
			v.Prizes = append(v.Prizes, PrizeView{Type: p.Type, Value: p.Value, Weight: w, Label: prizeLabel(p, gifts, lang), Gift: p.Gift})
		}
		out = append(out, v)
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

// Spin charges the whole pack, then draws every spin. Each result is independent; the payout guard may step a big prize down.
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
	pool, err := s.tierPool(ctx, tier)
	if err != nil {
		return nil, err
	}
	_, gifts, err := s.Gifts(ctx)
	if err != nil {
		return nil, err
	}
	// the guard may step down to any gift, not only those the tier's odds give
	allGifts, _, err := s.Gifts(ctx)
	if err != nil {
		return nil, err
	}
	guardPool := make([]Prize, 0, len(allGifts))
	for _, g := range allGifts {
		guardPool = append(guardPool, Prize{Type: TypeGift, Gift: g.Key, Value: g.StarValue})
	}
	// the coin and star prizes of this tier are valid step-down choices too
	for _, p := range pool {
		if p.Type != TypeGift {
			guardPool = append(guardPool, Prize{Type: p.Type, Value: p.Value})
		}
	}
	coinStars := s.coinStars(ctx)
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
			drawn, err := pick(pool)
			if err != nil {
				return err
			}
			prize, _, err := s.guard(ctx, tx, playerID, GameSpin, tier, drawn, guardPool, count-1-i)
			if err != nil {
				return err
			}
			worth := s.starValue(prize, coinStars)
			if err := s.recordPaid(ctx, tx, GameSpin, tier, worth); err != nil {
				return err
			}
			gid, err := s.payPrize(ctx, tx, playerID, prize, batch, i)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO spin_draws (player_id, tier, batch, gift_key, star_value, price, drawn_key, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				playerID, tier, batch, prizeSlug(prize), worth, cfg.Price, prizeSlug(drawn), s.now()); err != nil {
				return err
			}
			if err := s.feed(ctx, tx, playerID, GameSpin, prize, cfg.Price); err != nil {
				return err
			}
			switch prize.Type {
			case TypeCoins:
				res.TotalCoinsWon += prize.Value
			case TypeStars:
				res.TotalStarsWon += prize.Value
			}
			res.Results = append(res.Results, SpinPrize{Type: prize.Type, Value: prize.Value, Label: prizeLabel(prize, gifts, lang), Gift: prize.Gift, GiftID: gid, StarValue: worth})
		}
		_, res.Stars, err = ledger.Balances(ctx, tx, playerID)
		return err
	})
	return res, err
}

// payPrize gives one wheel prize: a gift goes to the player's collection (it returns the gift's id), coins and stars straight
// to the balance. Each draw of a purchase has its own idempotency key, so a repeated request can never pay it twice.
func (s *Service) payPrize(ctx context.Context, tx pgx.Tx, playerID int64, p Prize, batch string, idx int) (int64, error) {
	key := fmt.Sprintf("spin:%s:%d", batch, idx)
	switch p.Type {
	case TypeCoins:
		_, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: p.Value, Reason: ledger.ReasonSpinPrize,
			RefType: "spin", RefID: batch, IdemKey: key, Earned: true})
		if err != nil && err != ledger.ErrDuplicate {
			return 0, err
		}
		return 0, nil
	case TypeStars:
		_, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: p.Value, Reason: ledger.ReasonSpinPrize,
			RefType: "spin", RefID: batch, IdemKey: key})
		if err != nil && err != ledger.ErrDuplicate {
			return 0, err
		}
		return 0, nil
	}
	return s.grant(ctx, tx, playerID, p, "spin", batch)
}
