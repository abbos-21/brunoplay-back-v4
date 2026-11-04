package games

import (
	"context"
	"math"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// ExchangeInfo is what the exchange screen needs: the price of one star in coins (set in the admin panel) and the smallest purchase.
type ExchangeInfo struct {
	CoinsPerStar float64 `json:"coinsPerStar"` // coins that one star costs
	MinStars     int64   `json:"minStars"`
	Enabled      bool    `json:"enabled"`
}

// ExchangeResult is the answer to a finished exchange.
type ExchangeResult struct {
	Stars        int64 `json:"stars"`        // stars given
	Coins        int64 `json:"coins"`        // coins taken
	StarsBalance int64 `json:"starsBalance"` // new star balance
	CoinsBalance int64 `json:"coinsBalance"` // new coin balance
}

// ExchangeInfo reads the exchange settings. A price of 0 switches the exchange off.
func (s *Service) ExchangeInfo(ctx context.Context) (ExchangeInfo, error) {
	rate, err := s.settings.Float(ctx, settings.KeyExchangeRate)
	if err != nil {
		return ExchangeInfo{}, err
	}
	minStars, err := s.settings.Int(ctx, settings.KeyExchangeMinStars)
	if err != nil {
		return ExchangeInfo{}, err
	}
	if minStars < 1 {
		minStars = 1
	}
	return ExchangeInfo{CoinsPerStar: rate, MinStars: minStars, Enabled: rate > 0}, nil
}

// ExchangeCoins buys `stars` stars with coins at the price of the moment (rounded up to a whole coin, so a fraction never favours the player).
// Both balance changes are one transaction, so a player never loses coins without getting the stars.
func (s *Service) ExchangeCoins(ctx context.Context, playerID, stars int64) (*ExchangeResult, error) {
	info, err := s.ExchangeInfo(ctx)
	if err != nil {
		return nil, err
	}
	if !info.Enabled {
		return nil, apperr.ConfigMissing
	}
	if stars < info.MinStars {
		return nil, apperr.Validation
	}
	cost := math.Ceil(float64(stars) * info.CoinsPerStar)
	if cost < 1 || cost > math.MaxInt64/4 {
		return nil, apperr.Validation
	}
	coins := int64(cost)
	out := &ExchangeResult{Stars: stars, Coins: coins}
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, e := s.players.Lock(ctx, tx, playerID); e != nil {
			return e
		}
		meta := map[string]any{"stars": stars, "coins": coins, "coinsPerStar": info.CoinsPerStar}
		var e error
		out.CoinsBalance, e = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: -coins,
			Reason: ledger.ReasonExchangeCoins, RefType: "exchange", Meta: meta})
		if e != nil {
			return e
		}
		out.StarsBalance, e = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: stars,
			Reason: ledger.ReasonExchangeStars, RefType: "exchange", Meta: meta})
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
