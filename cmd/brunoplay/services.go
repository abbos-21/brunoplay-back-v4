package main

import (
	"context"
	"fmt"
	"time"

	"brunoplay/internal/api"
	"brunoplay/internal/httpx"
	"brunoplay/internal/jobs"
	"brunoplay/internal/settings"
)

// services holds the long-running pieces of the worker. Milestones add their jobs in registerJobs.
type services struct {
	deps *api.Deps
}

func newServices(d *api.Deps) *services { return &services{deps: d} }

func (s *services) registerJobs(r *jobs.Runner) {
	r.Add(jobs.Job{Name: "purge-idempotency", Every: time.Hour, Run: func(ctx context.Context) error {
		return httpx.PurgeIdempotency(ctx, s.deps.Pool, 48*time.Hour)
	}})
	r.Add(jobs.Job{Name: "autocollect", Every: 15 * time.Second, Run: func(ctx context.Context) error {
		for {
			n, err := s.deps.Fishing.RunAutocollect(ctx, 200)
			if err != nil || n < 200 {
				return err
			}
		}
	}})
	r.Add(jobs.Job{Name: "chest-stale", Every: 5 * time.Minute, Run: func(ctx context.Context) error {
		_, err := s.deps.Games.FinishStaleRounds(ctx)
		return err
	}})
	d := s.deps
	r.Add(jobs.Job{Name: "notify-send", Every: 3 * time.Second, Timeout: 2 * time.Minute, Run: func(ctx context.Context) error {
		for i := 0; i < 20; i++ {
			n, err := d.Notify.RunDue(ctx, 100)
			if err != nil || n == 0 {
				return err
			}
		}
		return nil
	}})
	r.Add(jobs.Job{Name: "notify-maintenance", Every: time.Minute, Run: func(ctx context.Context) error {
		if err := d.Notify.ResetStuck(ctx); err != nil {
			return err
		}
		return d.Notify.RunBroadcasts(ctx)
	}})
	r.Add(jobs.Job{Name: "notify-daily", Every: 2 * time.Minute, Timeout: 5 * time.Minute, Run: func(ctx context.Context) error {
		if _, err := d.Notify.DailyReady(ctx); err != nil {
			return err
		}
		if _, err := d.Notify.DailyWarnings(ctx); err != nil {
			return err
		}
		_, err := d.Notify.StreakLost(ctx)
		return err
	}})
	r.Add(jobs.Job{Name: "notify-purge", Every: 6 * time.Hour, Run: func(ctx context.Context) error {
		days, err := d.Settings.Int(ctx, settings.KeyRetentionLogs)
		if err != nil {
			return err
		}
		if err := d.Notify.Purge(ctx, time.Duration(days)*24*time.Hour); err != nil {
			return err
		}
		return d.Notify.PurgeJobMarks(ctx)
	}})
	r.Add(jobs.Job{Name: "tournaments-announce", Every: time.Minute, Run: func(ctx context.Context) error {
		return d.AnnounceTournaments(ctx)
	}})
	r.Add(jobs.Job{Name: "stats", Every: 10 * time.Minute, Timeout: 8 * time.Minute, Run: d.Stats.Run})
	r.Add(jobs.Job{Name: "risk-scan", Every: time.Hour, Timeout: 20 * time.Minute, Run: func(ctx context.Context) error {
		_, _, err := d.Risk.Scan(ctx)
		return err
	}})
	r.Add(jobs.Job{Name: "ops-alerts", Every: 5 * time.Minute, Run: func(ctx context.Context) error {
		_, err := d.Ops.Check(ctx)
		return err
	}})
	r.Add(jobs.Job{Name: "ledger-audit", Every: 6 * time.Hour, Timeout: 30 * time.Minute, Run: func(ctx context.Context) error {
		m, err := d.Risk.AuditLedger(ctx, 20)
		if err == nil && len(m) > 0 {
			d.Alert(ctx, fmt.Sprintf("LEDGER MISMATCH: %d player balance(s) differ from their ledger (first: player %d, %s). Investigate before paying out.", len(m), m[0].PlayerID, m[0].Currency))
		}
		return err
	}})
	r.Add(jobs.Job{Name: "ton-topups", Every: 10 * time.Second, Run: func(ctx context.Context) error {
		_, err := s.deps.Topup.PollTon(ctx)
		return err
	}})
	r.Add(jobs.Job{Name: "topup-expire", Every: time.Minute, Run: s.deps.Topup.ExpireOrders})
	r.Add(jobs.Job{Name: "payouts", Every: 20 * time.Second, Timeout: 5 * time.Minute, Run: func(ctx context.Context) error {
		_, err := s.deps.Withdrawals.RunPayouts(ctx, 20)
		return err
	}})
	r.Add(jobs.Job{Name: "payout-reconcile", Every: time.Minute, Run: func(ctx context.Context) error {
		_, err := s.deps.Withdrawals.Reconcile(ctx)
		return err
	}})
	r.Add(jobs.Job{Name: "purge-ad-views", Every: 6 * time.Hour, Run: func(ctx context.Context) error {
		days, err := s.deps.Settings.Int(ctx, settings.KeyRetentionAds)
		if err != nil {
			return err
		}
		return s.deps.Ads.Purge(ctx, time.Duration(days)*24*time.Hour)
	}})
	r.Add(jobs.Job{Name: "purge-admin-sessions", Every: 6 * time.Hour, Run: func(ctx context.Context) error {
		_, err := s.deps.Pool.Exec(ctx, `DELETE FROM admin_sessions WHERE expires_at < now() - interval '7 days' OR revoked_at < now() - interval '7 days'`)
		return err
	}})
}
