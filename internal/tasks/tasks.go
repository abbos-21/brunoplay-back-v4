// Package tasks has the player tasks: join a channel, invite friends, open a link, watch ads. Each pays once per period.
package tasks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/ads"
	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/ledger"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
)

const (
	TypeChannel = "CHANNEL_JOIN"
	TypeInvite  = "INVITE_FRIENDS"
	TypeLink    = "OPEN_LINK"
	TypeAds     = "WATCH_ADS"
)

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	players  *players.Store
	ads      *ads.Service
	tg       telegram.API
	Now      func() time.Time

	OnCompleted []func(ctx context.Context, tx pgx.Tx, playerID, taskID int64, coins, stars int64) error
}

func New(pool *pgxpool.Pool, st *settings.Service, ps *players.Store, a *ads.Service, tg telegram.API) *Service {
	return &Service{pool: pool, settings: st, players: ps, ads: a, tg: tg, Now: time.Now}
}

func (s *Service) SetTelegram(tg telegram.API) { s.tg = tg }
func (s *Service) now() time.Time              { return s.Now() }

type Task struct {
	ID          int64      `json:"id"`
	Category    string     `json:"category"`
	Type        string     `json:"type"`
	TitleEN     string     `json:"titleEn,omitempty"`
	TitleRU     string     `json:"titleRu,omitempty"`
	DescEN      string     `json:"descEn,omitempty"`
	DescRU      string     `json:"descRu,omitempty"`
	URL         string     `json:"url"`
	Channel     string     `json:"channelUsername"`
	Required    int        `json:"required"`
	RewardCoins int64      `json:"rewardCoins"`
	RewardStars int64      `json:"rewardStars"`
	Repeat      string     `json:"repeat"`
	StartsAt    *time.Time `json:"startsAt,omitempty"`
	EndsAt      *time.Time `json:"endsAt"`
	Active      bool       `json:"isActive"`
	SortOrder   int        `json:"sortOrder"`
	CreatedAt   time.Time  `json:"createdAt,omitempty"`
}

const taskCols = `id, category, type, title_en, title_ru, desc_en, desc_ru, url, channel, required, reward_coins, reward_stars, repeat_mode,
	starts_at, ends_at, active, sort_order, created_at`

func scanTask(r pgx.Row) (*Task, error) {
	var t Task
	err := r.Scan(&t.ID, &t.Category, &t.Type, &t.TitleEN, &t.TitleRU, &t.DescEN, &t.DescRU, &t.URL, &t.Channel, &t.Required, &t.RewardCoins,
		&t.RewardStars, &t.Repeat, &t.StartsAt, &t.EndsAt, &t.Active, &t.SortOrder, &t.CreatedAt)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound
	}
	return &t, err
}

// period returns the key of the current period and its start time.
func period(repeat string, now time.Time) (string, time.Time) {
	switch repeat {
	case "DAILY":
		return gameday.Key(now), gameday.Of(now)
	case "WEEKLY":
		d := gameday.Of(now)
		back := (int(d.Weekday()) + 6) % 7 // Monday = 0
		start := d.AddDate(0, 0, -back)
		y, w := now.UTC().ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w), start
	}
	return "once", time.Time{}
}

// TaskView is what the app shows for one task.
type TaskView struct {
	ID              int64      `json:"id"`
	Category        string     `json:"category"`
	Type            string     `json:"type"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	ChannelUsername string     `json:"channelUsername"`
	URL             string     `json:"url"`
	RequiredInvites int        `json:"requiredInvites"`
	Required        int        `json:"required"`
	RewardCoins     int64      `json:"rewardCoins"`
	RewardStars     int64      `json:"rewardStars"`
	IsActive        bool       `json:"isActive"`
	SortOrder       int        `json:"sortOrder"`
	IsCompleted     bool       `json:"isCompleted"`
	Progress        int        `json:"progress"`
	IsNew           bool       `json:"isNew"`
	Repeat          string     `json:"repeat"`
	EndsAt          *time.Time `json:"endsAt"`
}

func (s *Service) inviteCount(ctx context.Context, q db.Querier, playerID int64, since time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM players WHERE referred_by=$1 AND referral_counted AND ($2::timestamptz IS NULL OR referral_counted_at >= $2)`,
		playerID, nullTime(since)).Scan(&n)
	return n, err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// List returns the tasks currently open to the player, with their progress.
func (s *Service) List(ctx context.Context, p *players.Player) ([]TaskView, int, error) {
	now := s.now()
	rows, err := s.pool.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE active AND (starts_at IS NULL OR starts_at <= $1) AND (ends_at IS NULL OR ends_at > $1)
		ORDER BY category, sort_order, id`, now)
	if err != nil {
		return nil, 0, err
	}
	var list []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		list = append(list, t)
	}
	rows.Close()
	var seen *time.Time
	err = s.pool.QueryRow(ctx, `SELECT last_seen_at FROM task_seen WHERE player_id=$1`, p.ID).Scan(&seen)
	if err != nil && !db.IsNoRows(err) {
		return nil, 0, err
	}
	out := make([]TaskView, 0, len(list))
	newCount := 0
	for _, t := range list {
		key, start := period(t.Repeat, now)
		var prog int
		var done bool
		err := s.pool.QueryRow(ctx, `SELECT progress, completed FROM task_progress WHERE player_id=$1 AND task_id=$2 AND period_key=$3`, p.ID, t.ID, key).Scan(&prog, &done)
		if err != nil && !db.IsNoRows(err) {
			return nil, 0, err
		}
		if t.Type == TypeInvite && !done {
			if n, err := s.inviteCount(ctx, s.pool, p.ID, start); err == nil {
				prog = min(n, t.Required)
			}
		}
		v := TaskView{ID: t.ID, Category: t.Category, Type: t.Type, ChannelUsername: t.Channel, URL: t.URL, RequiredInvites: t.Required, Required: t.Required,
			RewardCoins: t.RewardCoins, RewardStars: t.RewardStars, IsActive: true, SortOrder: t.SortOrder, IsCompleted: done, Progress: prog, Repeat: t.Repeat, EndsAt: t.EndsAt}
		v.Title, v.Description = t.TitleEN, t.DescEN
		if p.Lang() == apperr.RU {
			v.Title, v.Description = t.TitleRU, t.DescRU
		}
		v.IsNew = !done && (seen == nil || t.CreatedAt.After(*seen))
		if v.IsNew {
			newCount++
		}
		out = append(out, v)
	}
	return out, newCount, nil
}

// MarkSeen records that the player looked at the list (clears the "NEW" tags and the badge).
func (s *Service) MarkSeen(ctx context.Context, playerID int64) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO task_seen (player_id, last_seen_at) VALUES ($1,$2) ON CONFLICT (player_id) DO UPDATE SET last_seen_at=EXCLUDED.last_seen_at`, playerID, s.now())
	return err
}

func (s *Service) open(ctx context.Context, q db.Querier, id int64, now time.Time) (*Task, error) {
	t, err := scanTask(q.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=$1 AND active AND (starts_at IS NULL OR starts_at <= $2) AND (ends_at IS NULL OR ends_at > $2)`, id, now))
	return t, err
}

// Open records that the player tapped a link task (the timer of OPEN_LINK starts here).
func (s *Service) Open(ctx context.Context, playerID, taskID int64) error {
	now := s.now()
	t, err := s.open(ctx, s.pool, taskID, now)
	if err != nil {
		return err
	}
	key, _ := period(t.Repeat, now)
	_, err = s.pool.Exec(ctx, `INSERT INTO task_progress (player_id, task_id, period_key, opened_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (player_id, task_id, period_key) DO UPDATE SET opened_at = COALESCE(task_progress.opened_at, EXCLUDED.opened_at)`, playerID, taskID, key, now)
	return err
}

type CheckResult struct {
	Completed   bool   `json:"completed"`
	Reward      int64  `json:"reward"`
	RewardStars int64  `json:"rewardStars"`
	Error       string `json:"error,omitempty"`
	Message     string `json:"message,omitempty"`
	Progress    int    `json:"progress"`
	Required    int    `json:"required"`
}

// Check verifies a task and pays it when done. Not done is a normal answer (completed=false), not an HTTP error.
func (s *Service) Check(ctx context.Context, p *players.Player, taskID int64) (*CheckResult, error) {
	now := s.now()
	t, err := s.open(ctx, s.pool, taskID, now)
	if err != nil {
		return nil, err
	}
	key, start := period(t.Repeat, now)
	var done bool
	var prog int
	var opened *time.Time
	err = s.pool.QueryRow(ctx, `SELECT completed, progress, opened_at FROM task_progress WHERE player_id=$1 AND task_id=$2 AND period_key=$3`, p.ID, taskID, key).Scan(&done, &prog, &opened)
	if err != nil && !db.IsNoRows(err) {
		return nil, err
	}
	if done {
		return nil, apperr.TaskDone
	}
	res := &CheckResult{Required: t.Required}
	notDone := func() (*CheckResult, error) {
		res.Error = apperr.TaskNotDone.Code
		res.Message = apperr.TaskNotDone.Message(p.Lang())
		return res, nil
	}
	switch t.Type {
	case TypeChannel:
		if t.Channel == "" {
			return nil, apperr.ConfigMissing.With("key", "task channel")
		}
		chat := t.Channel
		if !strings.HasPrefix(chat, "@") && !strings.HasPrefix(chat, "-") {
			chat = "@" + chat
		}
		var tg int64
		if err := s.pool.QueryRow(ctx, `SELECT telegram_id FROM players WHERE id=$1`, p.ID).Scan(&tg); err != nil {
			return nil, err
		}
		status, err := s.tg.GetChatMemberStatus(ctx, chat, tg)
		if err != nil {
			return nil, apperr.TaskCannotVerify.Wrap(err)
		}
		switch status {
		case "member", "administrator", "creator", "restricted":
		default:
			return notDone()
		}
		prog = t.Required
	case TypeInvite:
		n, err := s.inviteCount(ctx, s.pool, p.ID, start)
		if err != nil {
			return nil, err
		}
		prog = min(n, t.Required)
		res.Progress = prog
		if n < t.Required {
			return notDone()
		}
	case TypeLink:
		wait, _ := s.settings.Int(ctx, settings.KeyTaskLinkSeconds)
		if opened == nil || now.Sub(*opened) < time.Duration(wait)*time.Second {
			return notDone()
		}
		prog = t.Required
	case TypeAds:
		res.Progress = prog
		if prog < t.Required {
			return notDone() // progress grows with AdProgress
		}
	}
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error { return s.complete(ctx, tx, p.ID, t, key, now, t.Required) })
	if err != nil {
		return nil, err
	}
	res.Completed, res.Reward, res.RewardStars, res.Progress = true, t.RewardCoins, t.RewardStars, t.Required
	return res, nil
}

// complete marks the task done for the period and pays the reward, once.
func (s *Service) complete(ctx context.Context, tx pgx.Tx, playerID int64, t *Task, key string, now time.Time, progress int) error {
	if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
		return err
	}
	var done bool
	err := tx.QueryRow(ctx, `INSERT INTO task_progress (player_id, task_id, period_key, progress, completed, completed_at) VALUES ($1,$2,$3,$4,FALSE,NULL)
		ON CONFLICT (player_id, task_id, period_key) DO UPDATE SET progress=GREATEST(task_progress.progress, EXCLUDED.progress)
		RETURNING completed`, playerID, t.ID, key, progress).Scan(&done)
	if err != nil {
		return err
	}
	if done {
		return apperr.TaskDone
	}
	if _, err := tx.Exec(ctx, `UPDATE task_progress SET completed=TRUE, completed_at=$4 WHERE player_id=$1 AND task_id=$2 AND period_key=$3`, playerID, t.ID, key, now); err != nil {
		return err
	}
	idem := fmt.Sprintf("task:%d:%d:%s", t.ID, playerID, key)
	if t.RewardCoins > 0 {
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: t.RewardCoins, Reason: ledger.ReasonTaskReward,
			RefType: "task", RefID: fmt.Sprint(t.ID), IdemKey: idem + ":coin", Earned: true}); err != nil && err != ledger.ErrDuplicate {
			return err
		}
	}
	if t.RewardStars > 0 {
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: t.RewardStars, Reason: ledger.ReasonTaskReward,
			RefType: "task", RefID: fmt.Sprint(t.ID), IdemKey: idem + ":star"}); err != nil && err != ledger.ErrDuplicate {
			return err
		}
	}
	for _, h := range s.OnCompleted {
		if err := h(ctx, tx, playerID, t.ID, t.RewardCoins, t.RewardStars); err != nil {
			return err
		}
	}
	return nil
}

// AdProgress counts one watched ad towards a WATCH_ADS task (and pays it when the last one is watched).
func (s *Service) AdProgress(ctx context.Context, p *players.Player, taskID int64, adID string) (*CheckResult, error) {
	now := s.now()
	t, err := s.open(ctx, s.pool, taskID, now)
	if err != nil {
		return nil, err
	}
	if t.Type != TypeAds {
		return nil, apperr.BadRequest
	}
	key, _ := period(t.Repeat, now)
	res := &CheckResult{Required: t.Required}
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, p.ID); err != nil {
			return err
		}
		var prog int
		var done bool
		err := tx.QueryRow(ctx, `SELECT progress, completed FROM task_progress WHERE player_id=$1 AND task_id=$2 AND period_key=$3 FOR UPDATE`, p.ID, t.ID, key).Scan(&prog, &done)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		if done {
			return apperr.TaskDone
		}
		if err := s.ads.Consume(ctx, tx, p.ID, adID, ads.TaskAds, fmt.Sprintf("task_ad:%d", t.ID)); err != nil {
			return err
		}
		prog++
		res.Progress = prog
		if prog >= t.Required {
			if err := s.complete(ctx, tx, p.ID, t, key, now, prog); err != nil {
				return err
			}
			res.Completed, res.Reward, res.RewardStars = true, t.RewardCoins, t.RewardStars
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO task_progress (player_id, task_id, period_key, progress) VALUES ($1,$2,$3,$4)
			ON CONFLICT (player_id, task_id, period_key) DO UPDATE SET progress=EXCLUDED.progress`, p.ID, t.ID, key, prog)
		return err
	})
	return res, err
}

// Done reports whether a task was ever completed (the "task_done" card requirement).
func (s *Service) Done(ctx context.Context, q db.Querier, playerID, taskID int64) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_progress WHERE player_id=$1 AND task_id=$2 AND completed)`, playerID, taskID).Scan(&ok)
	return ok, err
}

// ---- admin

type Input struct {
	Category    string     `json:"category" validate:"required,oneof=MAIN OTHER"`
	Type        string     `json:"type" validate:"required,oneof=CHANNEL_JOIN INVITE_FRIENDS OPEN_LINK WATCH_ADS"`
	TitleEN     string     `json:"titleEn" validate:"required,max=200"`
	TitleRU     string     `json:"titleRu" validate:"required,max=200"`
	DescEN      string     `json:"descEn" validate:"max=500"`
	DescRU      string     `json:"descRu" validate:"max=500"`
	URL         string     `json:"url" validate:"max=500"`
	Channel     string     `json:"channelUsername" validate:"max=100"`
	Required    int        `json:"required" validate:"gte=1,lte=1000"`
	RewardCoins int64      `json:"rewardCoins" validate:"gte=0"`
	RewardStars int64      `json:"rewardStars" validate:"gte=0"`
	Repeat      string     `json:"repeat" validate:"omitempty,oneof=ONCE DAILY WEEKLY"`
	StartsAt    *time.Time `json:"startsAt"`
	EndsAt      *time.Time `json:"endsAt"`
	Active      *bool      `json:"isActive"`
	SortOrder   int        `json:"sortOrder"`
}

func (in *Input) check() error {
	if in.Repeat == "" {
		in.Repeat = "ONCE"
	}
	if in.Type == TypeChannel && in.Channel == "" {
		return apperr.Validation.With("field", "channelUsername")
	}
	if in.Type == TypeLink && in.URL == "" {
		return apperr.Validation.With("field", "url")
	}
	if in.RewardCoins == 0 && in.RewardStars == 0 {
		return apperr.Validation.With("field", "rewardCoins")
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in Input) (*Task, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	return scanTask(s.pool.QueryRow(ctx, `INSERT INTO tasks (category, type, title_en, title_ru, desc_en, desc_ru, url, channel, required, reward_coins, reward_stars,
		repeat_mode, starts_at, ends_at, active, sort_order, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING `+taskCols,
		in.Category, in.Type, in.TitleEN, in.TitleRU, in.DescEN, in.DescRU, in.URL, in.Channel, in.Required, in.RewardCoins, in.RewardStars, in.Repeat, in.StartsAt, in.EndsAt, active, in.SortOrder, s.now()))
}

func (s *Service) Update(ctx context.Context, id int64, in Input) (*Task, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	return scanTask(s.pool.QueryRow(ctx, `UPDATE tasks SET category=$2, type=$3, title_en=$4, title_ru=$5, desc_en=$6, desc_ru=$7, url=$8, channel=$9, required=$10,
		reward_coins=$11, reward_stars=$12, repeat_mode=$13, starts_at=$14, ends_at=$15, active=$16, sort_order=$17 WHERE id=$1 RETURNING `+taskCols,
		id, in.Category, in.Type, in.TitleEN, in.TitleRU, in.DescEN, in.DescRU, in.URL, in.Channel, in.Required, in.RewardCoins, in.RewardStars, in.Repeat, in.StartsAt, in.EndsAt, active, in.SortOrder))
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM tasks WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return apperr.NotFound
	}
	return nil
}

type AdminTask struct {
	Task
	Completed int64 `json:"completedCount"`
}

func (s *Service) AdminList(ctx context.Context) ([]AdminTask, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+taskCols+`, (SELECT count(*) FROM task_progress tp WHERE tp.task_id=tasks.id AND tp.completed) FROM tasks ORDER BY category, sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminTask{}
	for rows.Next() {
		var t AdminTask
		if err := rows.Scan(&t.ID, &t.Category, &t.Type, &t.TitleEN, &t.TitleRU, &t.DescEN, &t.DescRU, &t.URL, &t.Channel, &t.Required, &t.RewardCoins,
			&t.RewardStars, &t.Repeat, &t.StartsAt, &t.EndsAt, &t.Active, &t.SortOrder, &t.CreatedAt, &t.Completed); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
