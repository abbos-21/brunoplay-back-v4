// Package jobs runs the periodic background tasks. Each run takes a PostgreSQL advisory lock, so several worker
// processes can run side by side without ever running the same job twice at the same time.
package jobs

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Job struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
	// Timeout bounds one run (default: Every, at most 10 minutes).
	Timeout time.Duration
}

type Runner struct {
	pool *pgxpool.Pool
	jobs []Job
}

func NewRunner(pool *pgxpool.Pool) *Runner { return &Runner{pool: pool} }

func (r *Runner) Add(j Job) { r.jobs = append(r.jobs, j) }

func lockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("brunoplay:" + name))
	return int64(h.Sum64() >> 1)
}

// RunOnce runs one job if no other process holds its lock. Returns whether it ran.
func (r *Runner) RunOnce(ctx context.Context, j Job) (bool, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	key := lockKey(j.Name)
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		return false, err
	}
	if !got {
		return false, nil
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, key) }()
	to := j.Timeout
	if to == 0 {
		to = j.Every
		if to > 10*time.Minute {
			to = 10 * time.Minute
		}
	}
	rctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	return true, j.Run(rctx)
}

// Start launches every job on its own ticker until ctx is cancelled.
func (r *Runner) Start(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, j := range r.jobs {
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			t := time.NewTicker(j.Every)
			defer t.Stop()
			first := time.After(2 * time.Second)
			run := func() {
				ran, err := r.RunOnce(ctx, j)
				if err != nil && ctx.Err() == nil {
					slog.Error("job failed", "job", j.Name, "err", err)
				} else if ran {
					slog.Debug("job done", "job", j.Name)
				}
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-first:
					run()
				case <-t.C:
					run()
				}
			}
		}(j)
	}
	return &wg
}
