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

// ExchangeInfo is what the exchange screen needs: the rate set in the admin panel and the smallest amount.
type ExchangeInfo struct {
	CoinsPerStar float64 `json:"coinsPerStar"`
	MinStars     int64   `json:"minStars"`
	Enabled      bool    `json:"enabled"`
}

// ExchangeResult is the answer to a finished exchange.
type ExchangeResult struct {
	Stars        int64 `json:"stars"`        // stars taken
	Coins        int64 `json:"coins"`        // coins given
	StarsBalance int64 `json:"starsBalance"` // new star balance
	CoinsBalance int64 `json:"coinsBalance"` // new coin balance
}

// ExchangeInfo reads the exchange settings. A rate of 0 switches the exchange off.
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

// ExchangeStars turns stars into coins at the rate of the moment. Both balance changes are one transaction,
// so a player never loses stars without getting the coins. The coins are not counted as earned (they do not raise the level).
func (s *Service) ExchangeStars(ctx context.Context, playerID, stars int64) (*ExchangeResult, error) {
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
	coins := int64(math.Floor(float64(stars) * info.CoinsPerStar))
	if coins < 1 || coins > math.MaxInt64/4 {
		return nil, apperr.Validation
	}
	out := &ExchangeResult{Stars: stars, Coins: coins}
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, e := s.players.Lock(ctx, tx, playerID); e != nil {
			return e
		}
		meta := map[string]any{"stars": stars, "coins": coins, "coinsPerStar": info.CoinsPerStar}
		var e error
		out.StarsBalance, e = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: -stars,
			Reason: ledger.ReasonExchangeStars, RefType: "exchange", Meta: meta})
		if e != nil {
			return e
		}
		out.CoinsBalance, e = ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: coins,
			Reason: ledger.ReasonExchangeCoins, RefType: "exchange", Meta: meta})
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
