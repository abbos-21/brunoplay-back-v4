// Package ops watches the system and tells the owner (a Telegram admin chat) when something needs a human.
package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool  *pgxpool.Pool
	Now   func() time.Time
	Alert func(ctx context.Context, text string)
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool, Now: time.Now} }

func (s *Service) now() time.Time { return s.Now() }

// once makes an alert fire at most once per hour per condition.
func (s *Service) once(ctx context.Context, key string) bool {
	ct, err := s.pool.Exec(ctx, `INSERT INTO job_marks (name, key, at) VALUES ('alert', $1, $2) ON CONFLICT DO NOTHING`, key+":"+s.now().UTC().Format("2006-01-02T15"), s.now())
	return err == nil && ct.RowsAffected() > 0
}

func (s *Service) tell(ctx context.Context, key, text string) int {
	if s.Alert == nil || !s.once(ctx, key) {
		return 0
	}
	s.Alert(ctx, text)
	return 1
}

// Check looks for the conditions the owner must hear about. It returns how many alerts were raised.
func (s *Service) Check(ctx context.Context) (int, error) {
	now := s.now()
	n := 0
	var lag, review, late, stuckPay, pendingTopups int64
	if err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM notifications WHERE status='PENDING' AND notify_at < $1),
		(SELECT count(*) FROM withdrawals WHERE needs_review AND status IN ('PENDING','PROCESSING')),
		(SELECT count(*) FROM withdrawals WHERE status='PENDING' AND created_at < $2),
		(SELECT count(*) FROM withdrawals WHERE status='PROCESSING' AND auto AND payout_started_at < $3),
		(SELECT count(*) FROM topup_orders WHERE status='PENDING' AND method='TON' AND created_at < $4 AND expires_at > $1)`,
		now.Add(-5*time.Minute), now.Add(-48*time.Hour), now.Add(-30*time.Minute), now.Add(-time.Hour)).
		Scan(&lag, &review, &late, &stuckPay, &pendingTopups); err != nil {
		return 0, err
	}
	if lag > 200 {
		n += s.tell(ctx, "queue-lag", fmt.Sprintf("The bot's message queue is lagging: %d messages are overdue.", lag))
	}
	if review > 0 {
		n += s.tell(ctx, "wd-review", fmt.Sprintf("%d withdrawal(s) need a human decision (an automatic payout ended unclear).", review))
	}
	if late > 0 {
		n += s.tell(ctx, "wd-late", fmt.Sprintf("%d withdrawal(s) are waiting longer than 48 hours.", late))
	}
	if stuckPay > 0 {
		n += s.tell(ctx, "payout-stuck", fmt.Sprintf("%d automatic payout(s) have been processing for over 30 minutes.", stuckPay))
	}
	if pendingTopups > 0 {
		n += s.tell(ctx, "ton-pending", fmt.Sprintf("%d TON top-up order(s) are unpaid after an hour (the transfer may be missing or wrong).", pendingTopups))
	}
	return n, nil
}
