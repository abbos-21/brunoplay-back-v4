package topup

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"brunoplay/internal/settings"
	"brunoplay/internal/ton"
)

// Quoter tells what an amount of stars costs in nanoTON (fragment-api.uz).
type Quoter interface {
	StarsPriceNano(ctx context.Context, stars int64) (int64, error)
}

const (
	// a quote older than this is not used: the manual rate takes over (and the owner is told)
	quoteMaxAge = 30 * time.Minute
	// a new quote that differs from the previous one by more than this is refused (a wrong answer must not set the prices)
	quoteMaxJump = 0.5
	// prices are rounded up to 0.0001 TON, the precision Fragment gives
	priceStepNano = int64(100_000)
)

type quoteState struct {
	mu      sync.Mutex
	alerted bool
}

// autoOn: the owner chose the live price and a Fragment client exists.
func (s *Service) autoOn(ctx context.Context) bool {
	return s.Quoter != nil && s.settings.BoolOr(ctx, settings.KeyTopupTonAuto, false)
}

// withMargin adds the owner's margin to Fragment's price and rounds up to 0.0001 TON.
func withMargin(nano int64, percent float64) int64 {
	if percent < 0 {
		percent = 0
	}
	v := float64(nano) * (1 + percent/100)
	return int64(math.Ceil(v/float64(priceStepNano))) * priceStepNano
}

// freshQuote is Fragment's price for exactly this many stars, when it is recent enough.
func (s *Service) freshQuote(ctx context.Context, stars int64) (int64, bool) {
	var nano int64
	var at time.Time
	if err := s.pool.QueryRow(ctx, `SELECT price_nano, fetched_at FROM ton_quotes WHERE stars=$1`, stars).Scan(&nano, &at); err != nil {
		return 0, false
	}
	if s.now().Sub(at) > quoteMaxAge {
		return 0, false
	}
	return nano, nano > 0
}

// tonPriceFor is the TON price of a pack and whether it is the live one. With the automatic price on, the pack is priced as stars plus its bonus
// (100 + 10 is Fragment's price for 110 stars) plus the margin; while there is no recent quote the manual rate is used, on the same 110 stars.
// With it off, nothing changed: the manual rate on the pack's stars.
func (s *Service) tonPriceFor(ctx context.Context, packStars int64, bonusPct, starsPerTon float64) (nano int64, live bool) {
	if !s.autoOn(ctx) {
		if starsPerTon > 0 {
			return tonPrice(packStars, starsPerTon), false
		}
		return 0, false
	}
	total := packStars + bonusOf(packStars, bonusPct)
	if q, ok := s.freshQuote(ctx, total); ok {
		margin, _ := s.settings.Float(ctx, settings.KeyTopupTonMargin)
		return withMargin(q, margin), true
	}
	if starsPerTon > 0 {
		return tonPrice(total, starsPerTon), false
	}
	return 0, false
}

// RefreshQuotes (worker job, every few minutes) asks Fragment for the price of every pack's stars plus bonus and keeps the answers.
func (s *Service) RefreshQuotes(ctx context.Context) error {
	if !s.autoOn(ctx) {
		return nil
	}
	ps, err := s.packs(ctx)
	if err != nil {
		return err
	}
	pct, _ := s.settings.Float(ctx, settings.KeyTopupTonBonus)
	var firstErr error
	totals := make([]int64, 0, len(ps))
	for _, p := range ps {
		total := p.Stars + bonusOf(p.Stars, pct)
		totals = append(totals, total)
		if err := s.refreshOne(ctx, total); err != nil {
			slog.Warn("ton quote failed", "stars", total, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	// quotes of amounts that are no longer a pack are dropped
	_, _ = s.pool.Exec(ctx, `DELETE FROM ton_quotes WHERE NOT (stars = ANY($1))`, totals)

	healthy := true
	for _, t := range totals {
		if _, ok := s.freshQuote(ctx, t); !ok {
			healthy = false
		}
	}
	s.qs.mu.Lock()
	defer s.qs.mu.Unlock()
	switch {
	case healthy:
		s.qs.alerted = false
	case !s.qs.alerted:
		s.qs.alerted = true
		reason := "no answer yet"
		if firstErr != nil {
			reason = firstErr.Error()
		}
		s.alert(ctx, "The automatic TON price is not working: Fragment's price is missing or older than %d minutes (%s). Top-ups use the manual 'stars per TON' rate meanwhile. Check FRAGMENT_API_KEY and the server's IP in the Fragment API project.", int(quoteMaxAge.Minutes()), reason)
	}
	return firstErr
}

func (s *Service) refreshOne(ctx context.Context, total int64) error {
	nano, err := s.Quoter.StarsPriceNano(ctx, total)
	if err != nil {
		return err
	}
	var prev int64
	if err := s.pool.QueryRow(ctx, `SELECT price_nano FROM ton_quotes WHERE stars=$1`, total).Scan(&prev); err == nil && prev > 0 {
		if math.Abs(float64(nano-prev))/float64(prev) > quoteMaxJump {
			return fmt.Errorf("the price of %d stars jumped from %s to %s TON: not used", total, ton.TonString(prev), ton.TonString(nano))
		}
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO ton_quotes (stars, price_nano, fetched_at) VALUES ($1,$2,$3)
		ON CONFLICT (stars) DO UPDATE SET price_nano=EXCLUDED.price_nano, fetched_at=EXCLUDED.fetched_at`, total, nano, s.now())
	return err
}

// QuoteView is one row of the admin's "live TON price" table.
type QuoteView struct {
	PackStars    int64      `json:"packStars"`
	Stars        int64      `json:"stars"` // pack + bonus: what Fragment is asked about
	FragmentNano int64      `json:"fragmentNano"`
	PriceNano    int64      `json:"priceNano"` // what the player pays (with the margin), 0 when there is no recent quote
	FetchedAt    *time.Time `json:"fetchedAt"`
	Fresh        bool       `json:"fresh"`
}

// Quotes lists what the packs cost now.
func (s *Service) Quotes(ctx context.Context) ([]QuoteView, error) {
	ps, err := s.packs(ctx)
	if err != nil {
		return nil, err
	}
	pct, _ := s.settings.Float(ctx, settings.KeyTopupTonBonus)
	margin, _ := s.settings.Float(ctx, settings.KeyTopupTonMargin)
	out := make([]QuoteView, 0, len(ps))
	for _, p := range ps {
		total := p.Stars + bonusOf(p.Stars, pct)
		v := QuoteView{PackStars: p.Stars, Stars: total}
		var at time.Time
		if err := s.pool.QueryRow(ctx, `SELECT price_nano, fetched_at FROM ton_quotes WHERE stars=$1`, total).Scan(&v.FragmentNano, &at); err == nil {
			v.FetchedAt = &at
			if s.now().Sub(at) <= quoteMaxAge {
				v.Fresh = true
				v.PriceNano = withMargin(v.FragmentNano, margin)
			}
		}
		out = append(out, v)
	}
	return out, nil
}
