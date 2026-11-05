// Package payoutfeed posts every successful payout (a TON withdrawal that was paid, a star withdrawal that was delivered) to a public
// Telegram channel. The post is queued in the same transaction that completes the payout and sent by the worker, so a payout is
// posted exactly once and a failed send is retried.
package payoutfeed

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/db"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
)

const (
	KindTon   = "ton"
	KindStars = "stars"

	maxAttempts = 10
)

// Sender is the part of the Telegram client the feed needs (a channel is named by "@username" or its numeric id).
type Sender interface {
	SendToChat(ctx context.Context, chat string, text string, o telegram.SendOpts) error
}

// Payload is what a post is made of (stored with the queued row, so the text can be built when it is sent).
type Payload struct {
	Name  string `json:"name"`            // the player's name or @username as the game knows it; masked when posted
	Coins int64  `json:"coins,omitempty"` // TON payouts: the coins that were paid out
	Ton   string `json:"ton,omitempty"`   // TON payouts: the amount in TON, as text ("0.002")
	Tx    string `json:"tx,omitempty"`    // TON payouts: the transaction hash
	Stars int64  `json:"stars,omitempty"` // star payouts: the stars that were delivered
	At    string `json:"at,omitempty"`    // the date of the payout, only on posts made afterwards (backfill); live posts leave it out
}

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
	tg       Sender
	Now      func() time.Time
	Alert    func(ctx context.Context, text string)
	// PlayURL is the link of the Play button under a post (empty = no button)
	PlayURL string
}

func New(pool *pgxpool.Pool, st *settings.Service, tg Sender, playURL string) *Service {
	return &Service{pool: pool, settings: st, tg: tg, Now: time.Now, PlayURL: playURL}
}

// SetSender swaps the Telegram client (tests).
func (s *Service) SetSender(tg Sender) { s.tg = tg }

func (s *Service) now() time.Time { return s.Now() }

// channel is the channel the posts go to ("" = the feed is off).
func (s *Service) channel(ctx context.Context) string {
	return strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyPayoutsChannel, ""))
}

// Enqueue queues the post of a payout, inside the caller's transaction. It does nothing when the feed is off for this kind
// of payout, and asking twice for the same payout is harmless.
func (s *Service) Enqueue(ctx context.Context, q db.Querier, kind string, refID int64, p Payload) error {
	if s.channel(ctx) == "" {
		return nil
	}
	key := settings.KeyPayoutsTon
	if kind == KindStars {
		key = settings.KeyPayoutsStars
	}
	if !s.settings.BoolOr(ctx, key, true) {
		return nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO payout_posts (kind, ref_id, payload, next_attempt_at) VALUES ($1,$2,$3,$4) ON CONFLICT (kind, ref_id) DO NOTHING`,
		kind, refID, raw, s.now())
	return err
}

// MaskName shows enough of a name to be recognised by its owner and by nobody else: "Bruno" -> "B***o".
func MaskName(name string) string {
	name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "@"))
	r := []rune(name)
	switch {
	case len(r) == 0:
		return "Player"
	case len(r) < 3:
		return string(r[:1]) + "***"
	}
	return string(r[0]) + "***" + string(r[len(r)-1])
}

var (
	hexHash = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	b64Hash = regexp.MustCompile(`^[A-Za-z0-9+/_-]{43}=?$`)
)

// explorerLink is the address of the transaction in a public explorer, or "" when what the admin typed does not look like a hash
// (a wrong link in a public channel is worse than no link).
func explorerLink(hash string) string {
	hash = strings.TrimSpace(hash)
	if !hexHash.MatchString(hash) && !b64Hash.MatchString(hash) {
		return ""
	}
	return "https://tonviewer.com/transaction/" + hash
}

func (s *Service) render(ctx context.Context, kind string, p Payload) (string, telegram.SendOpts) {
	name := "Player"
	if s.settings.BoolOr(ctx, settings.KeyPayoutsNames, true) {
		name = html.EscapeString(MaskName(p.Name))
	}
	var b strings.Builder
	b.WriteString("✅ <b>Выплата отправлена · Payout sent</b>\n\n")
	fmt.Fprintf(&b, "👤 %s\n", name)
	if p.At != "" {
		fmt.Fprintf(&b, "📅 %s\n", html.EscapeString(p.At))
	}
	if kind == KindStars {
		fmt.Fprintf(&b, "⭐ <b>%s Stars</b>\n", group(p.Stars))
	} else {
		fmt.Fprintf(&b, "💸 <b>%s TON</b>", html.EscapeString(p.Ton))
		if p.Coins > 0 {
			fmt.Fprintf(&b, "  (%s coins)", group(p.Coins))
		}
		b.WriteString("\n")
		if link := explorerLink(p.Tx); link != "" {
			fmt.Fprintf(&b, "\n🔗 <a href=\"%s\">Транзакция · Transaction</a>\n", html.EscapeString(link))
		}
	}
	var o telegram.SendOpts
	o.DisableWeb = true
	if s.PlayURL != "" {
		o.Buttons = [][]telegram.Button{{{Text: "🎮 Играть · Play", URL: s.PlayURL}}}
	}
	return b.String(), o
}

// group writes 1234567 as 1,234,567.
func group(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// SendTest posts a short test message to the channel so the owner can check the bot is allowed to write there.
func (s *Service) SendTest(ctx context.Context) error {
	ch := s.channel(ctx)
	if ch == "" {
		return fmt.Errorf("the payouts channel is not set (Settings → payouts.channel)")
	}
	return s.tg.SendToChat(ctx, ch, "✅ <b>BrunoPlay</b>: the payouts channel works. Every paid withdrawal will be posted here.\n✅ Канал выплат работает: здесь будут появляться все выплаты.", telegram.SendOpts{DisableWeb: true})
}

// Run sends the queued posts that are due. It is the worker job (every few seconds).
func (s *Service) Run(ctx context.Context) error {
	for i := 0; i < 50; i++ {
		n, err := s.sendOne(ctx)
		if err != nil || !n {
			return err
		}
	}
	return nil
}

type due struct {
	id       int64
	kind     string
	payload  []byte
	attempts int
}

func (s *Service) sendOne(ctx context.Context) (bool, error) {
	// claim one post: pushing next_attempt_at forward makes a second worker skip it while this one sends
	var d due
	err := s.pool.QueryRow(ctx, `UPDATE payout_posts SET next_attempt_at = $1 WHERE id = (
			SELECT id FROM payout_posts WHERE status='PENDING' AND next_attempt_at <= $2 ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, kind, payload, attempts`, s.now().Add(2*time.Minute), s.now()).Scan(&d.id, &d.kind, &d.payload, &d.attempts)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ch := s.channel(ctx)
	if ch == "" {
		_, err := s.pool.Exec(ctx, `UPDATE payout_posts SET status='SKIPPED', last_error='the payouts channel was switched off' WHERE id=$1`, d.id)
		return true, err
	}
	var p Payload
	if err := json.Unmarshal(d.payload, &p); err != nil {
		_, e := s.pool.Exec(ctx, `UPDATE payout_posts SET status='FAILED', last_error=$2 WHERE id=$1`, d.id, "bad payload: "+err.Error())
		return true, e
	}
	text, opts := s.render(ctx, d.kind, p)
	if serr := s.tg.SendToChat(ctx, ch, text, opts); serr != nil {
		return true, s.failed(ctx, d, serr)
	}
	_, err = s.pool.Exec(ctx, `UPDATE payout_posts SET status='SENT', sent_at=$2, attempts=attempts+1, last_error='' WHERE id=$1`, d.id, s.now())
	return true, err
}

// failed schedules a retry (a minute, two, four... at most an hour) and gives up after maxAttempts, telling the owner once.
func (s *Service) failed(ctx context.Context, d due, cause error) error {
	attempts := d.attempts + 1
	wait := time.Minute << min(attempts-1, 6)
	if wait > time.Hour {
		wait = time.Hour
	}
	if rl, ok := cause.(telegram.RateLimited); ok && rl.RetryAfter > wait {
		wait = rl.RetryAfter
	}
	status := "PENDING"
	if attempts >= maxAttempts {
		status = "FAILED"
	}
	_, err := s.pool.Exec(ctx, `UPDATE payout_posts SET status=$2, attempts=$3, next_attempt_at=$4, last_error=$5 WHERE id=$1`,
		d.id, status, attempts, s.now().Add(wait), truncate(cause.Error(), 300))
	slog.Warn("payout feed: sending failed", "id", d.id, "attempt", attempts, "err", cause)
	if status == "FAILED" && s.Alert != nil {
		s.Alert(ctx, fmt.Sprintf("The payouts channel post %d could not be sent after %d tries (%v). Is the bot an admin of the channel with the right to post? Check Settings → payouts.channel.", d.id, attempts, cause))
	}
	return err
}

func jsonMarshal(p Payload) ([]byte, error) { return json.Marshal(p) }

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
