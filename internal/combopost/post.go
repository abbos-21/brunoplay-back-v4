package combopost

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
)

// Service posts the combo of the game day to the ambassadors channel.
type Service struct {
	pool    *pgxpool.Pool
	st      *settings.Service
	tg      telegram.API
	pattern func(ctx context.Context) ([]int16, error) // today's secret combo (the game's own, created when the admin set none)
	botLink string                                     // https://t.me/<bot>/<app>: the button under the post (a channel allows only link buttons)
	Now     func() time.Time
	Alert   func(ctx context.Context, text string)
}

func New(pool *pgxpool.Pool, st *settings.Service, tg telegram.API, pattern func(ctx context.Context) ([]int16, error), botLink string) *Service {
	return &Service{pool: pool, st: st, tg: tg, pattern: pattern, botLink: botLink, Now: time.Now}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type picture struct{ nameEN, nameRU, icon string }

// pictures loads the names and the emoji of the combo pictures by id.
func (s *Service) pictures(ctx context.Context, ids []int16) ([]picture, error) {
	out := make([]picture, 0, len(ids))
	for _, id := range ids {
		var p picture
		if err := s.pool.QueryRow(ctx, `SELECT name_en, name_ru, icon FROM combo_images WHERE id=$1`, id).Scan(&p.nameEN, &p.nameRU, &p.icon); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Caption is the text under the picture: Russian first, English below, the pictures in order.
func Caption(day time.Time, pics []picture) string {
	line := func(name func(picture) string) string {
		parts := make([]string, 0, len(pics))
		for _, p := range pics {
			t := html.EscapeString(name(p))
			// an emoji icon is shown before the name; an image link (set in the admin) is not an emoji
			if p.icon != "" && !strings.ContainsAny(p.icon, "/:.") {
				t = html.EscapeString(p.icon) + " " + t
			}
			parts = append(parts, t)
		}
		return strings.Join(parts, " → ")
	}
	return fmt.Sprintf("🎣 <b>Ежедневное комбо · %s</b>\n%s\n\n🎣 <b>Daily combo · %s</b>\n%s",
		day.Format("02.01.2006"), line(func(p picture) string { return p.nameRU }),
		day.Format("2 Jan 2006"), line(func(p picture) string { return p.nameEN }))
}

// Posted is what a post did.
type Posted struct {
	MessageID int64  `json:"messageId"`
	Chat      string `json:"chat"`
	Pinned    bool   `json:"pinned"`
	PinError  string `json:"pinError,omitempty"`
}

func (s *Service) channel(ctx context.Context) (string, error) {
	ch := strings.TrimSpace(s.st.StrOr(ctx, settings.KeyAmbassadorsChat, ""))
	if ch == "" {
		return "", apperr.ConfigMissing.With("key", settings.KeyAmbassadorsChat)
	}
	return ch, nil
}

// send draws and posts today's combo, pins it and unpins the older posts.
func (s *Service) send(ctx context.Context, day time.Time, chat string) (Posted, error) {
	items, err := s.pattern(ctx)
	if err != nil {
		return Posted{}, err
	}
	pics, err := s.pictures(ctx, items)
	if err != nil {
		return Posted{}, err
	}
	img, err := Render(items)
	if err != nil {
		return Posted{}, err
	}
	var buttons [][]telegram.Button
	if s.botLink != "" {
		buttons = [][]telegram.Button{{{Text: "🎮 Играть · Play", URL: s.botLink}}}
	}
	id, err := s.tg.SendPhotoToChat(ctx, chat, img, Caption(day, pics), telegram.SendOpts{Buttons: buttons})
	if err != nil {
		return Posted{}, err
	}
	// the older posts (the earlier days, or an earlier post of today), read before the new id is saved
	var old []int64
	if rows, err := s.pool.Query(ctx, `SELECT message_id FROM combo_posts WHERE message_id>0 AND chat=$1 AND posted_at > now() - interval '3 days'`, chat); err == nil {
		for rows.Next() {
			var m int64
			if rows.Scan(&m) == nil {
				old = append(old, m)
			}
		}
		rows.Close()
	}
	// the message is out: remember it before anything else can fail
	if _, err := s.pool.Exec(ctx, `UPDATE combo_posts SET message_id=$2, posted_at=now() WHERE day=$1`, day, id); err != nil {
		slog.Error("combo post: cannot save the message id", "err", err, "message", id)
	}
	res := Posted{MessageID: id, Chat: chat}
	for _, m := range old {
		_ = s.tg.UnpinChatMessage(ctx, chat, m) // already unpinned or deleted: nothing to do
	}
	if err := s.tg.PinChatMessage(ctx, chat, id); err != nil {
		res.PinError = err.Error()
		slog.Warn("combo post: cannot pin (the bot needs the right to pin messages)", "err", err)
	} else {
		res.Pinned = true
	}
	return res, nil
}

// Run is the worker job (every minute): when the switch is on and today's combo has not been posted, post it. A day is claimed first, so
// two workers never post twice; a post that was claimed but never finished is tried again after three minutes.
func (s *Service) Run(ctx context.Context) error {
	if !s.st.BoolOr(ctx, settings.KeyAmbassadorsOn, false) {
		return nil
	}
	chat, err := s.channel(ctx)
	if err != nil {
		return nil // switched on without a channel: nothing to do until it is set
	}
	day := gameday.Of(s.now())
	var attempts int
	err = s.pool.QueryRow(ctx, `INSERT INTO combo_posts (day, chat, attempts) VALUES ($1,$2,1)
		ON CONFLICT (day) DO UPDATE SET claimed_at=now(), chat=EXCLUDED.chat, attempts=combo_posts.attempts+1
		WHERE combo_posts.message_id=0 AND combo_posts.claimed_at < now() - interval '3 minutes'
		RETURNING attempts`, day, chat).Scan(&attempts)
	if db.IsNoRows(err) {
		return nil // posted already, or another worker is on it
	}
	if err != nil {
		return err
	}
	if _, err := s.send(ctx, day, chat); err != nil {
		slog.Error("combo post failed", "err", err, "attempt", attempts)
		if attempts == 3 && s.Alert != nil {
			s.Alert(ctx, "The daily combo could not be posted to the ambassadors channel (3 tries): "+err.Error()+". Check the channel setting and that the bot is an administrator there.")
		}
		return err
	}
	return nil
}

// PostNow posts today's combo right away, whatever the switch says (the admin's test button, or a repeat of the post).
func (s *Service) PostNow(ctx context.Context) (Posted, error) {
	chat, err := s.channel(ctx)
	if err != nil {
		return Posted{}, err
	}
	day := gameday.Of(s.now())
	if _, err := s.pool.Exec(ctx, `INSERT INTO combo_posts (day, chat, attempts) VALUES ($1,$2,1)
		ON CONFLICT (day) DO UPDATE SET chat=EXCLUDED.chat, claimed_at=now()`, day, chat); err != nil {
		return Posted{}, err
	}
	res, err := s.send(ctx, day, chat)
	if err != nil {
		return Posted{}, apperr.BadRequest.With("reason", err.Error())
	}
	return res, nil
}
