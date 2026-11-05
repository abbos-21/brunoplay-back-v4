package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"brunoplay/internal/config"
	"brunoplay/internal/db"
	"brunoplay/internal/payoutfeed"
	"brunoplay/internal/settings"
)

// payoutsBackfill queues a post in the payouts channel for every payout that was paid before the channel was set up.
// The running worker sends the posts. Without --apply it only counts them.
func payoutsBackfill(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("payouts-backfill", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "queue the posts (without it nothing is written)")
	spacing := fs.Duration("spacing", 5*time.Second, "time between two posts (Telegram allows about 20 a minute into one channel)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *spacing < time.Second {
		return fmt.Errorf("--spacing must be at least 1s")
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc := payoutfeed.New(pool, settings.New(pool), nil, "")
	rep, err := svc.Backfill(ctx, *apply, *spacing)
	if err != nil {
		return err
	}
	fmt.Println(rep.Summary(*spacing))
	return nil
}
