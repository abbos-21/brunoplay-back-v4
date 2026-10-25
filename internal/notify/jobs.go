package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
)

// once runs something at most once per (name, key): the first caller gets true.
func (s *Service) once(ctx context.Context, name, key string) (bool, error) {
	ct, err := s.pool.Exec(ctx, `INSERT INTO job_marks (name, key, at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, name, key, s.now())
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

const reachable = `p.bot_started AND NOT p.bot_blocked AND NOT p.is_blocked AND p.deleted_at IS NULL`

// BulkAll queues one message for every reachable player (parameters may carry name_en / name_ru style texts).
func (s *Service) BulkAll(ctx context.Context, typ string, params map[string]any, dedupePrefix string) (int64, error) {
	if _, ok := TypeOf(typ); !ok {
		return 0, fmt.Errorf("notify: unknown type %q", typ)
	}
	b, _ := json.Marshal(params)
	ct, err := s.pool.Exec(ctx, `INSERT INTO notifications (player_id, type, params, notify_at, dedupe_key, created_at)
		SELECT p.id, $1, $2, $3, $4 || p.id::text, $3 FROM players p WHERE `+reachable+` ON CONFLICT (dedupe_key) DO NOTHING`, typ, b, s.now(), dedupePrefix)
	return ct.RowsAffected(), err
}

// BulkPlayers queues one message for the given players.
func (s *Service) BulkPlayers(ctx context.Context, typ string, params map[string]any, ids []int64, dedupePrefix string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	b, _ := json.Marshal(params)
	ct, err := s.pool.Exec(ctx, `INSERT INTO notifications (player_id, type, params, notify_at, dedupe_key, created_at)
		SELECT p.id, $1, $2, $3, $4 || p.id::text, $3 FROM players p WHERE p.id = ANY($5) AND `+reachable+` ON CONFLICT (dedupe_key) DO NOTHING`,
		typ, b, s.now(), dedupePrefix, ids)
	return ct.RowsAffected(), err
}

func (s *Service) dailyConfigured(ctx context.Context) (streak, combo bool) {
	_, e1 := s.settings.Raw(ctx, settings.KeyStreakRewards)
	_, e2 := s.settings.Raw(ctx, settings.KeyComboPerCorrect)
	return e1 == nil, e2 == nil
}

// DailyReady sends "your daily reward / combo is ready" near each player's own best hour (the hour they usually open the app).
// It runs often; the work for one UTC hour is done once.
func (s *Service) DailyReady(ctx context.Context) (int64, error) {
	now := s.now()
	hour := now.UTC().Hour()
	day := gameday.Key(now)
	ok, err := s.once(ctx, "daily-ready", fmt.Sprintf("%s:%d", day, hour))
	if err != nil || !ok {
		return 0, err
	}
	streakOn, comboOn := s.dailyConfigured(ctx)
	if !streakOn && !comboOn {
		return 0, nil
	}
	def, err := s.settings.Int(ctx, settings.KeyNotifDefaultHr)
	if err != nil {
		def = 9
	}
	today := gameday.Of(now)
	ct, err := s.pool.Exec(ctx, `INSERT INTO notifications (player_id, type, params, notify_at, dedupe_key, created_at)
		SELECT x.id, CASE WHEN x.s AND x.c THEN 'daily_ready' WHEN x.s THEN 'streak_ready' ELSE 'combo_ready' END, '{}'::jsonb, $1, 'dailyready:' || x.id || ':' || $2, $1
		FROM (SELECT p.id,
			($3 AND NOT EXISTS (SELECT 1 FROM daily_streaks ds WHERE ds.player_id=p.id AND ds.last_claim_day=$5)) AS s,
			($4 AND NOT EXISTS (SELECT 1 FROM combo_days cd WHERE cd.player_id=p.id AND cd.day=$5 AND cd.attempts>0)) AS c
			FROM players p WHERE `+reachable+` AND COALESCE(p.pref_hour, $6::int) = $7) x
		WHERE x.s OR x.c ON CONFLICT (dedupe_key) DO NOTHING`, now, day, streakOn, comboOn, today, def, hour)
	return ct.RowsAffected(), err
}

// DailyWarnings sends the "don't lose your streak" and "last chance for the combo" messages N hours before the reset.
func (s *Service) DailyWarnings(ctx context.Context) (int64, error) {
	now := s.now()
	hours, err := settings.Get[[]int](ctx, s.settings, settings.KeyNotifWarnHours)
	if err != nil {
		return 0, nil
	}
	day := gameday.Key(now)
	today := gameday.Of(now)
	var total int64
	for _, w := range hours {
		if w < 1 || w > 23 || now.UTC().Hour() != 24-w {
			continue
		}
		ok, err := s.once(ctx, "daily-warn", fmt.Sprintf("%s:%d", day, w))
		if err != nil || !ok {
			continue
		}
		streakOn, comboOn := s.dailyConfigured(ctx)
		if streakOn {
			ct, err := s.pool.Exec(ctx, `INSERT INTO notifications (player_id, type, params, notify_at, dedupe_key, created_at)
				SELECT ds.player_id, 'streak_at_risk', jsonb_build_object('streak', ds.current), $1, 'streakrisk:' || ds.player_id || ':' || $2 || ':' || $3, $1
				FROM daily_streaks ds JOIN players p ON p.id=ds.player_id
				WHERE ds.last_claim_day = $4 AND ds.current >= 2 AND `+reachable+` ON CONFLICT (dedupe_key) DO NOTHING`, now, day, fmt.Sprint(w), today.AddDate(0, 0, -1))
			if err != nil {
				return total, err
			}
			total += ct.RowsAffected()
		}
		if comboOn {
			ct, err := s.pool.Exec(ctx, `INSERT INTO notifications (player_id, type, params, notify_at, dedupe_key, created_at)
				SELECT p.id, 'combo_reminder', '{}'::jsonb, $1, 'comborem:' || p.id || ':' || $2 || ':' || $3, $1
				FROM players p WHERE `+reachable+` AND p.last_seen_at > $4
				AND NOT EXISTS (SELECT 1 FROM combo_days cd WHERE cd.player_id=p.id AND cd.day=$5 AND cd.attempts>0) ON CONFLICT (dedupe_key) DO NOTHING`,
				now, day, fmt.Sprint(w), now.AddDate(0, 0, -7), today)
			if err != nil {
				return total, err
			}
			total += ct.RowsAffected()
		}
	}
	return total, nil
}

// StreakLost tells players whose streak of at least two days just broke (once per day).
func (s *Service) StreakLost(ctx context.Context) (int64, error) {
	now := s.now()
	ok, err := s.once(ctx, "streak-lost", gameday.Key(now))
	if err != nil || !ok {
		return 0, err
	}
	if on, _ := s.dailyConfigured(ctx); !on {
		return 0, nil
	}
	ct, err := s.pool.Exec(ctx, `INSERT INTO notifications (player_id, type, params, notify_at, dedupe_key, created_at)
		SELECT ds.player_id, 'streak_lost', '{}'::jsonb, $1, 'streaklost:' || ds.player_id || ':' || ds.last_claim_day::text, $1
		FROM daily_streaks ds JOIN players p ON p.id=ds.player_id
		WHERE ds.last_claim_day = $2 AND ds.current >= 2 AND `+reachable+` ON CONFLICT (dedupe_key) DO NOTHING`, now, gameday.Of(now).AddDate(0, 0, -2))
	return ct.RowsAffected(), err
}

// PurgeJobMarks removes old run markers.
func (s *Service) PurgeJobMarks(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM job_marks WHERE at < $1`, s.now().AddDate(0, 0, -3))
	return err
}

// ---- broadcasts

type Segment struct {
	Language      string `json:"language"`     // EN | RU | "" (all)
	MinLevel      int    `json:"minLevel"`     // 0 = any
	MaxLevel      int    `json:"maxLevel"`     // 0 = any
	InactiveDays  int    `json:"inactiveDays"` // not seen for at least N days (0 = any)
	HasBooster    *bool  `json:"hasBooster"`   // an active booster (true) or none (false)
	MinFriends    int    `json:"minFriends"`   // counted friends at least N
	OpenGiftClaim *bool  `json:"openGiftClaim"`
}

type Broadcast struct {
	ID          int64      `json:"id"`
	Title       string     `json:"title"`
	TextEN      string     `json:"textEn"`
	TextRU      string     `json:"textRu"`
	ButtonEN    string     `json:"buttonEn"`
	ButtonRU    string     `json:"buttonRu"`
	ButtonPath  string     `json:"buttonPath"`
	PhotoURL    string     `json:"photoUrl"`
	Segment     Segment    `json:"segment"`
	Status      string     `json:"status"`
	ScheduledAt *time.Time `json:"scheduledAt"`
	TestSentAt  *time.Time `json:"testSentAt"`
	Queued      int64      `json:"queued"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt"`
}

type BroadcastInput struct {
	Title      string  `json:"title" validate:"required,max=120"`
	TextEN     string  `json:"textEn" validate:"required,max=1000"`
	TextRU     string  `json:"textRu" validate:"required,max=1000"`
	ButtonEN   string  `json:"buttonEn" validate:"max=40"`
	ButtonRU   string  `json:"buttonRu" validate:"max=40"`
	ButtonPath string  `json:"buttonPath" validate:"max=100"`
	PhotoURL   string  `json:"photoUrl" validate:"max=500"`
	Segment    Segment `json:"segment"`
}

const bcols = `id, title, text_en, text_ru, button_en, button_ru, button_path, photo_url, segment, status, scheduled_at, test_sent_at, queued, created_at, started_at`

func scanBroadcast(r interface{ Scan(...any) error }) (*Broadcast, error) {
	var b Broadcast
	var seg []byte
	if err := r.Scan(&b.ID, &b.Title, &b.TextEN, &b.TextRU, &b.ButtonEN, &b.ButtonRU, &b.ButtonPath, &b.PhotoURL, &seg, &b.Status, &b.ScheduledAt, &b.TestSentAt, &b.Queued, &b.CreatedAt, &b.StartedAt); err != nil {
		if db.IsNoRows(err) {
			return nil, apperr.NotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(seg, &b.Segment)
	return &b, nil
}

func (s *Service) CreateBroadcast(ctx context.Context, in BroadcastInput, adminID int64) (*Broadcast, error) {
	seg, _ := json.Marshal(in.Segment)
	return scanBroadcast(s.pool.QueryRow(ctx, `INSERT INTO broadcasts (title, text_en, text_ru, button_en, button_ru, button_path, photo_url, segment, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+bcols, in.Title, in.TextEN, in.TextRU, in.ButtonEN, in.ButtonRU, in.ButtonPath, in.PhotoURL, seg, adminID, s.now()))
}

// UpdateBroadcast edits a draft; a changed text needs a new test-send.
func (s *Service) UpdateBroadcast(ctx context.Context, id int64, in BroadcastInput) (*Broadcast, error) {
	seg, _ := json.Marshal(in.Segment)
	return scanBroadcast(s.pool.QueryRow(ctx, `UPDATE broadcasts SET title=$2, text_en=$3, text_ru=$4, button_en=$5, button_ru=$6, button_path=$7, photo_url=$8, segment=$9, test_sent_at=NULL
		WHERE id=$1 AND status='DRAFT' RETURNING `+bcols, id, in.Title, in.TextEN, in.TextRU, in.ButtonEN, in.ButtonRU, in.ButtonPath, in.PhotoURL, seg))
}

func (s *Service) GetBroadcast(ctx context.Context, id int64) (*Broadcast, error) {
	return scanBroadcast(s.pool.QueryRow(ctx, `SELECT `+bcols+` FROM broadcasts WHERE id=$1`, id))
}

func (s *Service) ListBroadcasts(ctx context.Context) ([]Broadcast, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+bcols+` FROM broadcasts ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Broadcast{}
	for rows.Next() {
		b, err := scanBroadcast(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// TestBroadcast sends both language versions to the admin's own Telegram and unlocks starting.
func (s *Service) TestBroadcast(ctx context.Context, id int64, adminTelegramID int64) error {
	b, err := s.GetBroadcast(ctx, id)
	if err != nil {
		return err
	}
	if adminTelegramID == 0 {
		return apperr.Validation.With("field", "admin telegramId")
	}
	for _, l := range []string{"EN", "RU"} {
		text, btn := b.TextEN, b.ButtonEN
		if l == "RU" {
			text, btn = b.TextRU, b.ButtonRU
		}
		if btn == "" {
			btn = "Open game"
		}
		opts := telegram.SendOpts{PhotoURL: b.PhotoURL}
		if s.webAppURL != "" {
			opts.Buttons = [][]telegram.Button{{{Text: btn, WebApp: s.webAppURL + pathOr(b.ButtonPath)}}}
		}
		if err := s.tg.SendMessage(ctx, adminTelegramID, "["+l+"] "+text, opts); err != nil {
			return err
		}
	}
	_, err = s.pool.Exec(ctx, `UPDATE broadcasts SET test_sent_at=$2 WHERE id=$1`, id, s.now())
	return err
}

func pathOr(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// StartBroadcast schedules (or starts now) a tested broadcast.
func (s *Service) StartBroadcast(ctx context.Context, id int64, at *time.Time) (*Broadcast, error) {
	b, err := s.GetBroadcast(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.Status != "DRAFT" {
		return nil, apperr.BadRequest
	}
	if b.TestSentAt == nil {
		return nil, apperr.BadRequest.With("reason", "send a test to yourself first")
	}
	when := s.now()
	if at != nil && at.After(when) {
		when = *at
	}
	if _, err := s.pool.Exec(ctx, `UPDATE broadcasts SET status='SCHEDULED', scheduled_at=$2 WHERE id=$1`, id, when); err != nil {
		return nil, err
	}
	return s.GetBroadcast(ctx, id)
}

func (s *Service) CancelBroadcast(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `UPDATE broadcasts SET status='CANCELLED' WHERE id=$1 AND status IN ('DRAFT','SCHEDULED','SENDING')`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return apperr.BadRequest
	}
	_, err = s.pool.Exec(ctx, `UPDATE notifications SET status='CANCELLED', reason='broadcast cancelled' WHERE broadcast_id=$1 AND status='PENDING'`, id)
	return err
}

// RunBroadcasts expands due broadcasts into queue rows (the segment is evaluated now) and closes finished ones.
func (s *Service) RunBroadcasts(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT `+bcols+` FROM broadcasts WHERE status='SCHEDULED' AND scheduled_at <= $1 ORDER BY id LIMIT 5`, s.now())
	if err != nil {
		return err
	}
	var due []*Broadcast
	for rows.Next() {
		b, err := scanBroadcast(rows)
		if err != nil {
			rows.Close()
			return err
		}
		due = append(due, b)
	}
	rows.Close()
	for _, b := range due {
		where, args := segmentSQL(b.Segment, s.now())
		args = append([]any{b.ID, s.now()}, args...)
		ct, err := s.pool.Exec(ctx, `INSERT INTO notifications (player_id, type, params, notify_at, dedupe_key, broadcast_id, created_at)
			SELECT p.id, 'broadcast', '{}'::jsonb, $2, 'bc:' || ($1::bigint)::text || ':' || p.id, $1::bigint, $2 FROM players p WHERE `+reachable+where+` ON CONFLICT (dedupe_key) DO NOTHING`, args...)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE broadcasts SET status='SENDING', started_at=$2, queued=$3 WHERE id=$1 AND status='SCHEDULED'`, b.ID, s.now(), ct.RowsAffected()); err != nil {
			return err
		}
	}
	_, err = s.pool.Exec(ctx, `UPDATE broadcasts b SET status='DONE' WHERE status='SENDING' AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.broadcast_id=b.id AND n.status IN ('PENDING','SENDING'))`)
	return err
}

// segmentSQL turns a segment into extra conditions ($1 and $2 are taken by the caller; arguments start at $3).
func segmentSQL(sg Segment, now time.Time) (string, []any) {
	var w strings.Builder
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		w.WriteString(" AND " + strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args)+2)))
	}
	if l := strings.ToUpper(sg.Language); l == "EN" || l == "RU" {
		add("p.language = ?", l)
	}
	if sg.MinLevel > 0 {
		add("p.level >= ?", sg.MinLevel)
	}
	if sg.MaxLevel > 0 {
		add("p.level <= ?", sg.MaxLevel)
	}
	if sg.InactiveDays > 0 {
		add("p.last_seen_at < ?", now.AddDate(0, 0, -sg.InactiveDays))
	}
	if sg.HasBooster != nil {
		args = append(args, now)
		n := len(args) + 2
		not := ""
		if !*sg.HasBooster {
			not = "NOT "
		}
		w.WriteString(fmt.Sprintf(" AND %sEXISTS (SELECT 1 FROM player_boosters b WHERE b.player_id=p.id AND b.starts_at <= $%d AND b.expires_at > $%d)", not, n, n))
	}
	if sg.MinFriends > 0 {
		add("(SELECT count(*) FROM players f WHERE f.referred_by=p.id AND f.referral_counted) >= ?", sg.MinFriends)
	}
	if sg.OpenGiftClaim != nil && *sg.OpenGiftClaim {
		w.WriteString(" AND EXISTS (SELECT 1 FROM player_gifts g WHERE g.player_id=p.id AND g.status='CLAIM_REQUESTED')")
	}
	return w.String(), args
}

// BroadcastStats counts a broadcast's messages by status.
func (s *Service) BroadcastStats(ctx context.Context, id int64) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM notifications WHERE broadcast_id=$1 GROUP BY status`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}
