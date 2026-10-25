package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
)

// Pending is what a validator sees: the message about to be sent.
type Pending struct {
	ID       int64
	PlayerID int64
	Type     string
	Params   map[string]any
}

// Validator says whether a queued message is still true at the moment of sending ("your tank is full" after the player collected is not).
type Validator func(ctx context.Context, q db.Querier, n Pending) (bool, error)

type Service struct {
	pool       *pgxpool.Pool
	settings   *settings.Service
	tg         telegram.API
	webAppURL  string
	Now        func() time.Time
	limiter    *rate.Limiter // Telegram allows about 30 messages per second: stay below
	Validators map[string]Validator

	tplMu    sync.Mutex
	tplCache map[string]tpl
	tplAt    time.Time
}

type tpl struct{ text, button string }

func New(pool *pgxpool.Pool, st *settings.Service, tg telegram.API, webAppURL string) *Service {
	return &Service{pool: pool, settings: st, tg: tg, webAppURL: strings.TrimRight(webAppURL, "/"), Now: time.Now,
		limiter: rate.NewLimiter(25, 5), Validators: map[string]Validator{}}
}

func (s *Service) now() time.Time { return s.Now() }

// SetAPI swaps the Telegram client (tests).
func (s *Service) SetAPI(tg telegram.API) { s.tg = tg }

// SetRate changes the global send rate per second (tests use a high value).
func (s *Service) SetRate(perSecond float64) {
	s.limiter = rate.NewLimiter(rate.Limit(perSecond), int(perSecond)+1)
}

// ---- queue

// Enqueue schedules a message. With a dedupe key the same message is never queued twice.
func (s *Service) Enqueue(ctx context.Context, q db.Querier, playerID int64, typ string, params map[string]any, at time.Time, dedupe string) error {
	if _, ok := TypeOf(typ); !ok {
		return fmt.Errorf("notify: unknown type %q", typ)
	}
	b, _ := json.Marshal(params)
	var dk any
	if dedupe != "" {
		dk = dedupe
	}
	_, err := q.Exec(ctx, `INSERT INTO notifications (player_id, type, params, notify_at, dedupe_key, created_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (dedupe_key) DO NOTHING`,
		playerID, typ, b, at, dk, s.now())
	return err
}

// Now queues a message for immediate sending.
func (s *Service) Send(ctx context.Context, q db.Querier, playerID int64, typ string, params map[string]any, dedupe string) error {
	return s.Enqueue(ctx, q, playerID, typ, params, s.now(), dedupe)
}

// Cancel withdraws the player's pending messages of the given types (the situation changed).
func (s *Service) Cancel(ctx context.Context, q db.Querier, playerID int64, typs ...string) error {
	_, err := q.Exec(ctx, `UPDATE notifications SET status='CANCELLED', reason='situation changed' WHERE player_id=$1 AND status='PENDING' AND type = ANY($2)`, playerID, typs)
	return err
}

type row struct {
	n        Pending
	attempts int
	bcast    *int64
	at       time.Time
}

func (s *Service) claim(ctx context.Context, batch int) ([]row, error) {
	var out []row
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = nil
		rows, err := tx.Query(ctx, `UPDATE notifications SET status='SENDING' WHERE id IN (
			SELECT id FROM notifications WHERE status='PENDING' AND notify_at <= $1 ORDER BY notify_at LIMIT $2 FOR UPDATE SKIP LOCKED)
			RETURNING id, player_id, type, params, attempts, broadcast_id, notify_at`, s.now(), batch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r row
			var p []byte
			if err := rows.Scan(&r.n.ID, &r.n.PlayerID, &r.n.Type, &p, &r.attempts, &r.bcast, &r.at); err != nil {
				return err
			}
			_ = json.Unmarshal(p, &r.n.Params)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

type target struct {
	tgID       int64
	lang       apperr.Lang
	botStarted bool
	botBlocked bool
	blocked    bool
	allOff     bool
	off        map[string]bool
}

func (s *Service) loadTarget(ctx context.Context, id int64) (*target, error) {
	var t target
	var lang string
	var off []byte
	err := s.pool.QueryRow(ctx, `SELECT telegram_id, language, bot_started, bot_blocked, is_blocked, notif_all_off, notif_off FROM players WHERE id=$1 AND deleted_at IS NULL`, id).
		Scan(&t.tgID, &lang, &t.botStarted, &t.botBlocked, &t.blocked, &t.allOff, &off)
	if err != nil {
		return nil, err
	}
	t.lang = apperr.ParseLang(lang)
	var list []string
	_ = json.Unmarshal(off, &list)
	t.off = map[string]bool{}
	for _, c := range list {
		t.off[c] = true
	}
	return &t, nil
}

func (s *Service) finish(ctx context.Context, id int64, status, reason string) {
	var sent any
	if status == "SENT" {
		sent = s.now()
	}
	if _, err := s.pool.Exec(ctx, `UPDATE notifications SET status=$2, reason=$3, sent_at=COALESCE($4, sent_at) WHERE id=$1`, id, status, reason, sent); err != nil {
		slog.Error("notification status update failed", "id", id, "err", err)
	}
}

func (s *Service) reschedule(ctx context.Context, id int64, at time.Time, bumpAttempt bool, reason string) {
	inc := 0
	if bumpAttempt {
		inc = 1
	}
	if _, err := s.pool.Exec(ctx, `UPDATE notifications SET status='PENDING', notify_at=$2, attempts=attempts+$3, reason=$4 WHERE id=$1`, id, at, inc, reason); err != nil {
		slog.Error("notification reschedule failed", "id", id, "err", err)
	}
}

// quietEnd returns when the quiet hours (UTC) end if `now` is inside them.
func (s *Service) quietEnd(ctx context.Context, now time.Time) (time.Time, bool) {
	q, err := settings.Get[map[string]int](ctx, s.settings, settings.KeyNotifQuiet)
	if err != nil || q["from"] < 0 || q["to"] < 0 || q["from"] == q["to"] {
		return time.Time{}, false
	}
	h := now.UTC().Hour()
	from, to := q["from"], q["to"]
	in := h >= from && h < to
	if from > to { // wraps midnight, e.g. 22 -> 7
		in = h >= from || h < to
	}
	if !in {
		return time.Time{}, false
	}
	end := time.Date(now.Year(), now.Month(), now.Day(), to, 0, 0, 0, time.UTC)
	if !end.After(now) {
		end = end.AddDate(0, 0, 1)
	}
	return end, true
}

// RunDue sends what is due. It is safe to run in several processes: each message is claimed by one of them.
func (s *Service) RunDue(ctx context.Context, batch int) (int, error) {
	rows, err := s.claim(ctx, batch)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, r := range rows {
		if ctx.Err() != nil {
			s.reschedule(context.WithoutCancel(ctx), r.n.ID, s.now(), false, "shutdown")
			continue
		}
		if s.process(ctx, r) {
			sent++
		}
	}
	return sent, nil
}

func (s *Service) process(ctx context.Context, r row) bool {
	id := r.n.ID
	ty, ok := TypeOf(r.n.Type)
	if !ok {
		s.finish(ctx, id, "SKIPPED", "unknown type")
		return false
	}
	t, err := s.loadTarget(ctx, r.n.PlayerID)
	if err != nil {
		s.finish(ctx, id, "SKIPPED", "player not found")
		return false
	}
	switch {
	case t.blocked && r.n.Type != "account_blocked" && r.n.Type != "security_alert":
		s.finish(ctx, id, "SKIPPED", "account blocked")
		return false
	case !t.botStarted || t.botBlocked:
		s.finish(ctx, id, "SKIPPED", "bot not available")
		return false
	case !ty.Mandatory() && (t.allOff || t.off[string(ty.Cat)]):
		s.finish(ctx, id, "SKIPPED", "switched off by the player")
		return false
	}
	if v := s.Validators[r.n.Type]; v != nil {
		ok, err := v(ctx, s.pool, r.n)
		if err != nil {
			s.reschedule(ctx, id, s.now().Add(time.Minute), true, "validator error")
			return false
		}
		if !ok {
			s.finish(ctx, id, "SKIPPED", "no longer relevant")
			return false
		}
	}
	if !ty.Mandatory() {
		if end, in := s.quietEnd(ctx, s.now()); in {
			s.reschedule(ctx, id, end, false, "quiet hours")
			return false
		}
		if ty.Cooldown > 0 {
			var recent bool
			_ = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications WHERE player_id=$1 AND type=$2 AND status='SENT' AND sent_at > $3)`,
				r.n.PlayerID, r.n.Type, s.now().Add(-ty.Cooldown)).Scan(&recent)
			if recent {
				s.finish(ctx, id, "SKIPPED", "cooldown")
				return false
			}
		}
		capDay, _ := s.settings.Int(ctx, settings.KeyNotifDailyCap)
		var today int64
		day := time.Date(s.now().Year(), s.now().Month(), s.now().Day(), 0, 0, 0, 0, time.UTC)
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM notifications n WHERE n.player_id=$1 AND n.status='SENT' AND n.sent_at >= $2 AND n.type NOT LIKE 'withdrawal_%' AND n.type NOT LIKE 'topup_%' AND n.type NOT LIKE 'account_%' AND n.type <> 'security_alert'`,
			r.n.PlayerID, day).Scan(&today)
		if capDay > 0 && today >= capDay {
			s.finish(ctx, id, "SKIPPED", "daily cap")
			return false
		}
	}

	text, btn, page, photo, err := s.render(ctx, r, ty, t.lang)
	if err != nil {
		s.finish(ctx, id, "SKIPPED", err.Error())
		return false
	}
	if err := s.limiter.Wait(ctx); err != nil {
		s.reschedule(context.WithoutCancel(ctx), id, s.now(), false, "shutdown")
		return false
	}
	opts := telegram.SendOpts{PhotoURL: photo}
	if btn != "" && s.webAppURL != "" {
		opts.Buttons = [][]telegram.Button{{{Text: btn, WebApp: s.webAppURL + page}}}
	}
	err = s.tg.SendMessage(ctx, t.tgID, text, opts)
	var rl telegram.RateLimited
	switch {
	case err == nil:
		s.finish(ctx, id, "SENT", "")
		return true
	case errors.Is(err, telegram.ErrBlocked), errors.Is(err, telegram.ErrForbidden), errors.Is(err, telegram.ErrNotFound):
		_, _ = s.pool.Exec(ctx, `UPDATE players SET bot_blocked=TRUE WHERE id=$1`, r.n.PlayerID)
		s.finish(ctx, id, "SKIPPED", "the player blocked the bot")
	case errors.As(err, &rl):
		s.reschedule(ctx, id, s.now().Add(rl.RetryAfter+time.Second), false, "rate limited by Telegram")
	default:
		if r.attempts+1 >= 5 {
			s.finish(ctx, id, "FAILED", err.Error())
		} else {
			s.reschedule(ctx, id, s.now().Add(time.Duration(30<<uint(r.attempts))*time.Second), true, err.Error())
		}
	}
	return false
}

// render builds the text and button of a message in the player's language (admin templates win over the defaults).
func (s *Service) render(ctx context.Context, r row, ty Type, lang apperr.Lang) (text, btn, page, photo string, err error) {
	page = ty.Page
	if r.n.Type == "broadcast" {
		if r.bcast == nil {
			return "", "", "", "", errors.New("broadcast missing")
		}
		var en, ru, ben, bru, bpath, ph string
		if e := s.pool.QueryRow(ctx, `SELECT text_en, text_ru, button_en, button_ru, button_path, photo_url FROM broadcasts WHERE id=$1`, *r.bcast).Scan(&en, &ru, &ben, &bru, &bpath, &ph); e != nil {
			return "", "", "", "", errors.New("broadcast missing")
		}
		text, btn, photo = en, ben, ph
		if lang == apperr.RU {
			text, btn = ru, bru
		}
		if btn == "" {
			btn = map[bool]string{true: ty.BtnRU, false: ty.BtnEN}[lang == apperr.RU]
		}
		if bpath != "" {
			page = bpath
		}
		return text, btn, page, photo, nil
	}
	text, btn = ty.EN, ty.BtnEN
	if lang == apperr.RU {
		text, btn = ty.RU, ty.BtnRU
	}
	if o, ok := s.override(ctx, r.n.Type, lang); ok {
		text = o.text
		if o.button != "" {
			btn = o.button
		}
	}
	return fill(text, localize(r.n.Params, lang)), btn, page, "", nil
}

func (s *Service) override(ctx context.Context, typ string, lang apperr.Lang) (tpl, bool) {
	s.tplMu.Lock()
	defer s.tplMu.Unlock()
	if s.tplCache == nil || s.now().Sub(s.tplAt) > 30*time.Second {
		rows, err := s.pool.Query(ctx, `SELECT type, lang, text, button FROM notification_templates`)
		if err == nil {
			cache := map[string]tpl{}
			for rows.Next() {
				var t, l, tx, b string
				if rows.Scan(&t, &l, &tx, &b) == nil {
					cache[t+"|"+l] = tpl{tx, b}
				}
			}
			rows.Close()
			s.tplCache, s.tplAt = cache, s.now()
		}
	}
	o, ok := s.tplCache[typ+"|"+string(lang)]
	return o, ok
}

// ResetStuck puts messages whose sender died back in the queue.
func (s *Service) ResetStuck(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE notifications SET status='PENDING', attempts=attempts+1, reason='sender stopped' WHERE status='SENDING' AND notify_at < $1`, s.now().Add(-10*time.Minute))
	return err
}

// Purge deletes old finished rows.
func (s *Service) Purge(ctx context.Context, olderThan time.Duration) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM notifications WHERE status IN ('SENT','SKIPPED','FAILED','CANCELLED') AND created_at < $1`, s.now().Add(-olderThan))
	return err
}

// ---- admin templates

type Template struct {
	Type    string `json:"type"`
	Lang    string `json:"lang"`
	Text    string `json:"text"`
	Button  string `json:"button"`
	Default string `json:"default"`
	Edited  bool   `json:"edited"`
}

func (s *Service) Templates(ctx context.Context) ([]Template, error) {
	var out []Template
	for _, name := range Names() {
		ty, _ := TypeOf(name)
		if name == "broadcast" {
			continue
		}
		for _, l := range []apperr.Lang{apperr.EN, apperr.RU} {
			def, btn := ty.EN, ty.BtnEN
			if l == apperr.RU {
				def, btn = ty.RU, ty.BtnRU
			}
			t := Template{Type: name, Lang: string(l), Text: def, Button: btn, Default: def}
			if o, ok := s.override(ctx, name, l); ok {
				t.Text, t.Edited = o.text, true
				if o.button != "" {
					t.Button = o.button
				}
			}
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *Service) SetTemplate(ctx context.Context, typ, lang, text, button string, adminID int64) error {
	if _, ok := TypeOf(typ); !ok || typ == "broadcast" {
		return apperr.NotFound
	}
	lang = strings.ToUpper(lang)
	if lang != "EN" && lang != "RU" {
		return apperr.Validation.With("field", "lang")
	}
	if strings.TrimSpace(text) == "" || len(text) > 1000 {
		return apperr.Validation.With("field", "text")
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO notification_templates (type, lang, text, button, updated_by, updated_at) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (type, lang) DO UPDATE SET text=EXCLUDED.text, button=EXCLUDED.button, updated_by=EXCLUDED.updated_by, updated_at=EXCLUDED.updated_at`,
		typ, lang, text, button, adminID, s.now())
	s.tplMu.Lock()
	s.tplCache = nil
	s.tplMu.Unlock()
	return err
}

func (s *Service) ResetTemplate(ctx context.Context, typ, lang string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM notification_templates WHERE type=$1 AND lang=$2`, typ, strings.ToUpper(lang))
	s.tplMu.Lock()
	s.tplCache = nil
	s.tplMu.Unlock()
	return err
}

// ---- stats for the admin

type Stat struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

func (s *Service) Stats(ctx context.Context, since time.Time) ([]Stat, error) {
	rows, err := s.pool.Query(ctx, `SELECT type, status, count(*) FROM notifications WHERE created_at >= $1 GROUP BY type, status ORDER BY type, status`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Stat{}
	for rows.Next() {
		var x Stat
		if err := rows.Scan(&x.Type, &x.Status, &x.Count); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SetWebApp changes the Mini App address used by the message buttons (tests).
func (s *Service) SetWebApp(url string) { s.webAppURL = strings.TrimRight(url, "/") }
