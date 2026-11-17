package games

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// Roulette: one spin costs roulette.cost_stars and always wins one prize of roulette.prizes (a gift, coins or stars), drawn on the
// server by weight. A gift goes into the player's collection, coins and stars straight to the balance. The payout guard is the same
// safety net as in the other games: when paying a prize would push the game's payout ratio over guard.max_payout_ratio, the
// player gets the cheapest prize of the list instead.

const (
	GameRoulette = "roulette"
	// rouletteTier is the one price level of the roulette in the payout statistics
	rouletteTier = "main"
)

type roulettePrizeCfg struct {
	Type   string  `json:"type"`
	Gift   string  `json:"gift"`
	Value  int64   `json:"value"`
	Weight float64 `json:"weight"`
}

// roulettePrizes reads the prize list and gives every prize its value: a gift is worth its catalogue star value, a name is added.
// A gift that is not in the catalogue is an owner mistake and stops the game (nobody pays for a prize that cannot be given).
func (s *Service) roulettePrizes(ctx context.Context, lang apperr.Lang) ([]Prize, error) {
	cfg, err := settings.Get[[]roulettePrizeCfg](ctx, s.settings, settings.KeyRoulettePrizes)
	if err != nil {
		return nil, err
	}
	_, gifts, err := s.Gifts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Prize, 0, len(cfg))
	for _, c := range cfg {
		if c.Weight <= 0 {
			continue
		}
		p := Prize{Type: c.Type, Weight: c.Weight}
		switch c.Type {
		case TypeGift:
			g, ok := gifts[c.Gift]
			if !ok {
				return nil, apperr.GiftCatalog.With("why", fmt.Sprintf("roulette.prizes names the gift %q, which is not in the gift catalogue", c.Gift))
			}
			p.Gift, p.Value, p.Label = g.Key, g.StarValue, g.Name(lang)
		case TypeCoins, TypeStars:
			p.Value = c.Value
			p.Label = prizeLabel(p, lang)
		default:
			continue
		}
		out = append(out, p)
	}
	if len(out) < 2 {
		return nil, apperr.ConfigMissing.With("key", settings.KeyRoulettePrizes)
	}
	return out, nil
}

// drawPrize picks one prize by weight.
func drawPrize(list []Prize) Prize {
	var total float64
	for _, p := range list {
		total += p.Weight
	}
	roll := randFloat() * total
	for _, p := range list {
		if roll < p.Weight {
			return p
		}
		roll -= p.Weight
	}
	return list[len(list)-1]
}

// cheapest is the prize a guarded draw is turned into: the least valuable one.
func (s *Service) cheapest(list []Prize, coinStars float64) Prize {
	best := list[0]
	for _, p := range list[1:] {
		if s.starValue(p, coinStars) < s.starValue(best, coinStars) {
			best = p
		}
	}
	return best
}

type RouletteInfo struct {
	CostStars int64       `json:"costStars"`
	Prizes    []PrizeView `json:"prizes"`
}

// Roulette is what the screen needs: the price and the prizes, best first. The weights are shown only when the owner chose to publish
// the odds (spin.publish_odds); otherwise every prize is listed with weight 1.
func (s *Service) Roulette(ctx context.Context, lang apperr.Lang) (*RouletteInfo, error) {
	cost, err := s.settings.Int(ctx, settings.KeyRouletteCost)
	if err != nil {
		return nil, err
	}
	list, err := s.roulettePrizes(ctx, lang)
	if err != nil {
		return nil, err
	}
	coin := s.coinStars(ctx)
	sort.SliceStable(list, func(i, j int) bool { return s.starValue(list[i], coin) > s.starValue(list[j], coin) })
	publish := s.settings.BoolOr(ctx, settings.KeySpinPublishOdds, false)
	out := &RouletteInfo{CostStars: cost, Prizes: make([]PrizeView, 0, len(list))}
	for _, p := range list {
		w := 1.0
		if publish {
			w = p.Weight
		}
		out.Prizes = append(out.Prizes, PrizeView{Type: p.Type, Value: p.Value, Weight: w, Label: p.Label, Gift: p.Gift})
	}
	return out, nil
}

type RouletteResult struct {
	Prize SpinPrize `json:"prize"`
	Spent int64     `json:"spent"`
	Coins int64     `json:"coins"`
	Stars int64     `json:"stars"`
}

// RouletteSpin charges one spin, draws the prize on the server and pays it, all in one transaction.
func (s *Service) RouletteSpin(ctx context.Context, playerID int64, lang apperr.Lang) (*RouletteResult, error) {
	cost, err := s.settings.Int(ctx, settings.KeyRouletteCost)
	if err != nil {
		return nil, err
	}
	list, err := s.roulettePrizes(ctx, lang)
	if err != nil {
		return nil, err
	}
	coin := s.coinStars(ctx)
	batch := batchID()
	var res *RouletteResult
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
			return err
		}
		if cost > 0 {
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: -cost, Reason: ledger.ReasonRouletteCharge,
				RefType: "roulette", RefID: batch}); err != nil {
				return err
			}
			if err := s.recordSpend(ctx, tx, GameRoulette, rouletteTier, cost); err != nil {
				return err
			}
			if err := s.spent(ctx, tx, playerID, GameRoulette, cost); err != nil {
				return err
			}
		}
		drawn := drawPrize(list)
		prize := drawn
		// the guard looks at what the prize is worth in stars; a refused prize becomes the cheapest one
		if v := s.starValue(drawn, coin); v > 0 {
			ok, err := s.winGuard(ctx, tx, playerID, GameRoulette, rouletteTier, v)
			if err != nil {
				return err
			}
			if !ok {
				prize = s.cheapest(list, coin)
			}
		}
		worth := s.starValue(prize, coin)
		if err := s.recordPaid(ctx, tx, GameRoulette, rouletteTier, worth); err != nil {
			return err
		}
		var giftID int64
		switch prize.Type {
		case TypeCoins:
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: prize.Value, Reason: ledger.ReasonRoulettePrize,
				RefType: "roulette", RefID: batch, IdemKey: "roulette:" + batch, Earned: true}); err != nil && err != ledger.ErrDuplicate {
				return err
			}
		case TypeStars:
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: prize.Value, Reason: ledger.ReasonRoulettePrize,
				RefType: "roulette", RefID: batch, IdemKey: "roulette:" + batch}); err != nil && err != ledger.ErrDuplicate {
				return err
			}
		case TypeGift:
			id, err := s.grant(ctx, tx, playerID, prize, GameRoulette, batch)
			if err != nil {
				return err
			}
			giftID = id
		}
		if _, err := tx.Exec(ctx, `INSERT INTO spin_draws (player_id, tier, batch, gift_key, star_value, price, drawn_key, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			playerID, GameRoulette, batch, prize.Key(), worth, cost, drawn.Key(), s.now()); err != nil {
			return err
		}
		if err := s.feed(ctx, tx, playerID, GameRoulette, prize, cost); err != nil {
			return err
		}
		coins, stars, err := ledger.Balances(ctx, tx, playerID)
		if err != nil {
			return err
		}
		res = &RouletteResult{
			Prize: SpinPrize{Type: prize.Type, Value: prize.Value, Label: prize.Label, Gift: prize.Gift, GiftID: giftID, StarValue: worth},
			Spent: cost, Coins: coins, Stars: stars,
		}
		return nil
	})
	return res, err
}
