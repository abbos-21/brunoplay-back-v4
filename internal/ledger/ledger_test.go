package ledger_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
	"brunoplay/internal/testutil"
)

func newPlayer(t *testing.T, pool *pgxpool.Pool, tg int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `INSERT INTO players (telegram_id) VALUES ($1) RETURNING id`, tg).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func apply(ctx context.Context, pool *pgxpool.Pool, e ledger.Entry) (int64, error) {
	var bal int64
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		bal, err = ledger.Apply(ctx, tx, e)
		return err
	})
	return bal, err
}

func TestCreditAndDebit(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	id := newPlayer(t, pool, 1)

	bal, err := apply(ctx, pool, ledger.Entry{PlayerID: id, Currency: ledger.Coin, Amount: 100, Reason: ledger.ReasonMining, Earned: true})
	if err != nil || bal != 100 {
		t.Fatalf("credit: %v %d", err, bal)
	}
	bal, err = apply(ctx, pool, ledger.Entry{PlayerID: id, Currency: ledger.Coin, Amount: -30, Reason: ledger.ReasonCardUpgrade})
	if err != nil || bal != 70 {
		t.Fatalf("debit: %v %d", err, bal)
	}
	if _, err = apply(ctx, pool, ledger.Entry{PlayerID: id, Currency: ledger.Coin, Amount: -71, Reason: ledger.ReasonCardUpgrade}); !apperr.Is(err, apperr.InsufficientCoins) {
		t.Fatalf("overdraw must fail with INSUFFICIENT_COINS, got %v", err)
	}
	if _, err = apply(ctx, pool, ledger.Entry{PlayerID: id, Currency: ledger.Star, Amount: -1, Reason: ledger.ReasonBoosterBuy}); !apperr.Is(err, apperr.InsufficientStars) {
		t.Fatalf("stars overdraw must fail, got %v", err)
	}
	var earned int64
	_ = pool.QueryRow(ctx, `SELECT total_coins_earned FROM players WHERE id=$1`, id).Scan(&earned)
	if earned != 100 {
		t.Fatalf("lifetime earnings = %d, want 100", earned)
	}
	var rows int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries WHERE player_id=$1`, id).Scan(&rows)
	if rows != 2 {
		t.Fatalf("failed attempts must not write rows, got %d", rows)
	}
	if _, err = apply(ctx, pool, ledger.Entry{PlayerID: 99999, Currency: ledger.Coin, Amount: 1, Reason: "x"}); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("unknown player: %v", err)
	}
}

func TestNegativeStarsOnlyWhenAllowed(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	id := newPlayer(t, pool, 2)
	bal, err := apply(ctx, pool, ledger.Entry{PlayerID: id, Currency: ledger.Star, Amount: -50, Reason: ledger.ReasonTopupRefund, AllowNegative: true})
	if err != nil || bal != -50 {
		t.Fatalf("refund below zero must be allowed: %v %d", err, bal)
	}
}

func TestConcurrentDebitsNeverOverdraw(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	id := newPlayer(t, pool, 3)
	if _, err := apply(ctx, pool, ledger.Entry{PlayerID: id, Currency: ledger.Coin, Amount: 20, Reason: "seed"}); err != nil {
		t.Fatal(err)
	}
	var ok, insufficient int64
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := apply(ctx, pool, ledger.Entry{PlayerID: id, Currency: ledger.Coin, Amount: -1, Reason: "spend"})
			switch {
			case err == nil:
				atomic.AddInt64(&ok, 1)
			case apperr.Is(err, apperr.InsufficientCoins):
				atomic.AddInt64(&insufficient, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 20 || insufficient != 40 {
		t.Fatalf("ok=%d insufficient=%d, want 20/40", ok, insufficient)
	}
	var coins int64
	_ = pool.QueryRow(ctx, `SELECT coins FROM players WHERE id=$1`, id).Scan(&coins)
	if coins != 0 {
		t.Fatalf("balance %d, want 0", coins)
	}
}

func TestIdempotencyKey(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	id := newPlayer(t, pool, 4)
	e := ledger.Entry{PlayerID: id, Currency: ledger.Coin, Amount: 50, Reason: ledger.ReasonDailyStreak, IdemKey: "streak:4:2026-10-24"}
	if _, err := apply(ctx, pool, e); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(ctx, pool, e); err != ledger.ErrDuplicate {
		t.Fatalf("second apply must be a duplicate, got %v", err)
	}
	// racing duplicates: exactly one wins
	e2 := e
	e2.IdemKey = "race"
	var wins int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := apply(ctx, pool, e2); err == nil {
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	wg.Wait()
	var coins int64
	_ = pool.QueryRow(ctx, `SELECT coins FROM players WHERE id=$1`, id).Scan(&coins)
	if wins != 1 || coins != 100 {
		t.Fatalf("wins=%d coins=%d, want 1 win and 100 coins", wins, coins)
	}
}

func TestLedgerIsAppendOnly(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	id := newPlayer(t, pool, 5)
	if _, err := apply(ctx, pool, ledger.Entry{PlayerID: id, Currency: ledger.Coin, Amount: 5, Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ledger_entries SET amount=999`); err == nil {
		t.Fatal("UPDATE on the ledger must be refused")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ledger_entries`); err == nil {
		t.Fatal("DELETE on the ledger must be refused")
	}
}
