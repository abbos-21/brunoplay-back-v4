package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/bot"
	"brunoplay/internal/db"
	"brunoplay/internal/fishing"
	"brunoplay/internal/gameday"
	"brunoplay/internal/httpx"
	"brunoplay/internal/notify"
	"brunoplay/internal/settings"
	"brunoplay/internal/shop"
	"brunoplay/internal/telegram"
	"brunoplay/internal/topup"
	"brunoplay/internal/tournaments"
	"brunoplay/internal/withdrawals"
)

// wireBot connects every game event to the notification queue and every notification to a check that it is still true.
func (d *Deps) wireBot() {
	st := d.Settings
	n := d.Notify

	// ---- fishing: the tank fills at a known moment, so the message is scheduled for it and re-scheduled when it changes
	d.Fishing.OnStateChanged = append(d.Fishing.OnStateChanged, func(ctx context.Context, tx pgx.Tx, playerID int64, fullAt *time.Time, running bool) error {
		if err := n.Cancel(ctx, tx, playerID, "tank_full", "collect_reminder", "fishing_idle"); err != nil {
			return err
		}
		if !running {
			h, err := st.Int(ctx, settings.KeyNotifIdleH)
			if err != nil {
				return nil
			}
			at := d.now().Add(time.Duration(h) * time.Hour)
			return n.Enqueue(ctx, tx, playerID, "fishing_idle", nil, at, fmt.Sprintf("idle:%d:%d", playerID, at.Unix()))
		}
		if fullAt == nil {
			return nil
		}
		at := *fullAt
		if at.Before(d.now()) {
			at = d.now()
		}
		if err := n.Enqueue(ctx, tx, playerID, "tank_full", nil, at, fmt.Sprintf("tankfull:%d:%d:%d", playerID, fullAt.Unix(), d.now().UnixNano())); err != nil {
			return err
		}
		if h, err := st.Int(ctx, settings.KeyNotifCollectH); err == nil {
			return n.Enqueue(ctx, tx, playerID, "collect_reminder", nil, at.Add(time.Duration(h)*time.Hour), fmt.Sprintf("collectrem:%d:%d:%d", playerID, fullAt.Unix(), d.now().UnixNano()))
		}
		return nil
	})
	d.Fishing.OnCollected = append(d.Fishing.OnCollected, func(ctx context.Context, tx pgx.Tx, ev fishing.CollectEvent) error {
		if err := d.Tournaments.OnCoinsMined(ctx, tx, ev.PlayerID, ev.Coins); err != nil {
			return err
		}
		if ev.Auto {
			return n.Send(ctx, tx, ev.PlayerID, "autocollect_done", map[string]any{"coins": ev.Coins}, fmt.Sprintf("autocollect:%d", ev.SessionID))
		}
		return nil
	})

	// ---- boosters
	d.Shop.OnBooster = append(d.Shop.OnBooster, func(ctx context.Context, tx pgx.Tx, playerID int64, typ string, expires time.Time) error {
		names := map[string][2]string{fishing.BoostAuto: {"Auto Collect", "Автосбор"}, fishing.BoostMining: {"1.5x", "1,5×"}}[typ]
		base := map[string]any{"booster_en": names[0], "booster_ru": names[1], "expires": expires.Unix(), "type": typ}
		days, err := settings.Get[[]int](ctx, st, settings.KeyNotifBoosterD)
		if err == nil {
			for _, dd := range days {
				at := expires.Add(-time.Duration(dd) * 24 * time.Hour)
				if !at.After(d.now()) {
					continue
				}
				p := map[string]any{"days": dd}
				for k, v := range base {
					p[k] = v
				}
				if err := n.Enqueue(ctx, tx, playerID, "booster_expiring", p, at, fmt.Sprintf("boostexp:%d:%s:%d:%d", playerID, typ, expires.Unix(), dd)); err != nil {
					return err
				}
			}
		}
		return n.Enqueue(ctx, tx, playerID, "booster_expired", base, expires, fmt.Sprintf("boostend:%d:%s:%d", playerID, typ, expires.Unix()))
	})

	// ---- level, friends
	d.Shop.OnLevelUp = append(d.Shop.OnLevelUp, func(ctx context.Context, tx pgx.Tx, ev shop.LevelUpEvent) error {
		en, ru := rewardText(ev.RewardCoin, ev.RewardStar)
		return n.Send(ctx, tx, ev.PlayerID, "level_up", map[string]any{"level": ev.NewLevel, "reward_en": en, "reward_ru": ru}, fmt.Sprintf("levelup:%d:%d", ev.PlayerID, ev.NewLevel))
	})
	d.Referrals.OnFriendCounted = append(d.Referrals.OnFriendCounted, func(ctx context.Context, tx pgx.Tx, inviterID, friendID int64) error {
		if err := d.Tournaments.OnFriendCounted(ctx, tx, inviterID); err != nil {
			return err
		}
		return n.Send(ctx, tx, inviterID, "friend_joined", map[string]any{"name": d.displayName(ctx, tx, friendID)}, fmt.Sprintf("friendjoined:%d:%d", inviterID, friendID))
	})
	d.Referrals.OnReward = append(d.Referrals.OnReward, func(ctx context.Context, tx pgx.Tx, inviterID, friendID int64, kind string, level int, coins int64) error {
		if kind != "level" {
			return nil
		}
		return n.Send(ctx, tx, inviterID, "friend_level_reward", map[string]any{"name": d.displayName(ctx, tx, friendID), "level": level, "coins": coins},
			fmt.Sprintf("friendlevel:%d:%d:%d", inviterID, friendID, level))
	})

	// ---- money
	d.Withdrawals.OnStatus = append(d.Withdrawals.OnStatus, func(ctx context.Context, tx pgx.Tx, w *withdrawals.Withdrawal, event string) error {
		tx2 := ""
		if w.TxHash != nil {
			tx2 = *w.TxHash
		}
		return n.Send(ctx, tx, w.PlayerID, "withdrawal_"+event, map[string]any{"coins": w.AmountCoins, "ton": w.AmountTon, "tx": tx2}, fmt.Sprintf("wd:%d:%s", w.ID, event))
	})
	d.Topup.OnPaid = append(d.Topup.OnPaid, func(ctx context.Context, tx pgx.Tx, ev topup.PaidEvent) error {
		return n.Send(ctx, tx, ev.PlayerID, "topup_paid", map[string]any{"stars": ev.Stars + ev.Bonus}, "topup:"+ev.OrderID)
	})
	d.Topup.OnRefunded = append(d.Topup.OnRefunded, func(ctx context.Context, tx pgx.Tx, playerID int64, orderID string, stars int64) error {
		return n.Send(ctx, tx, playerID, "topup_refunded", map[string]any{"stars": stars}, "topuprefund:"+orderID)
	})

	// ---- games and tournaments
	d.Games.OnSpend = append(d.Games.OnSpend, d.Tournaments.OnSpend)
	d.Games.OnRide = append(d.Games.OnRide, d.Tournaments.OnRide)
	d.Games.OnChestRound = append(d.Games.OnChestRound, func(ctx context.Context, tx pgx.Tx, playerID, roundID int64) error {
		h, err := st.Int(ctx, settings.KeyNotifChestH)
		if err != nil {
			return nil
		}
		return n.Enqueue(ctx, tx, playerID, "chest_round_open", map[string]any{"round": roundID}, d.now().Add(time.Duration(h)*time.Hour), fmt.Sprintf("chest:%d", roundID))
	})
	d.Tournaments.OnPaid = append(d.Tournaments.OnPaid, func(ctx context.Context, tx pgx.Tx, playerID, tid int64, place int, p tournaments.Prize) error {
		t, err := d.Tournaments.Get(ctx, tid)
		if err != nil {
			return nil
		}
		en, ru := rewardText(p.Coins, p.Stars)
		if p.Gift != "" {
			en, ru = strings.TrimSuffix(en+", gift "+p.Gift, ", "), strings.TrimSuffix(ru+", подарок "+p.Gift, ", ")
		}
		return n.Send(ctx, tx, playerID, "tournament_result", map[string]any{"name_en": t.NameEN, "name_ru": t.NameRU, "place": place, "prize_en": en, "prize_ru": ru}, fmt.Sprintf("tresult:%d:%d", tid, playerID))
	})

	d.registerValidators()
}

func rewardText(coins, stars int64) (en, ru string) {
	var e, r []string
	if coins > 0 {
		e = append(e, fmt.Sprintf("%d coins", coins))
		r = append(r, fmt.Sprintf("%d монет", coins))
	}
	if stars > 0 {
		e = append(e, fmt.Sprintf("%d stars", stars))
		r = append(r, fmt.Sprintf("%d звёзд", stars))
	}
	if len(e) == 0 {
		return "", ""
	}
	return "Reward: " + strings.Join(e, ", "), "Награда: " + strings.Join(r, ", ")
}

func (d *Deps) displayName(ctx context.Context, q db.Querier, playerID int64) string {
	var u, f string
	_ = q.QueryRow(ctx, `SELECT username, first_name FROM players WHERE id=$1`, playerID).Scan(&u, &f)
	switch {
	case u != "":
		return "@" + u
	case f != "":
		return f
	}
	return "Player"
}

// registerValidators re-checks a queued message at the moment of sending: a message that is no longer true is dropped.
func (d *Deps) registerValidators() {
	v := d.Notify.Validators
	// every check uses the game clock ($2), never the database clock
	exists := func(sql string) notify.Validator {
		return func(ctx context.Context, q db.Querier, p notify.Pending) (bool, error) {
			var ok bool
			err := q.QueryRow(ctx, sql, p.PlayerID).Scan(&ok)
			return ok, err
		}
	}
	existsNow := func(sql string) notify.Validator {
		return func(ctx context.Context, q db.Querier, p notify.Pending) (bool, error) {
			var ok bool
			err := q.QueryRow(ctx, sql, p.PlayerID, d.now()).Scan(&ok)
			return ok, err
		}
	}
	// With Autocollect the server empties the tank by itself, a few seconds after it fills. The message is due at the very moment
	// the tank fills, when the job has not run yet, so without the booster check every Autocollect player was told to "collect".
	v["tank_full"] = existsNow(`SELECT EXISTS(SELECT 1 FROM fishing_sessions WHERE player_id=$1 AND status='RUNNING' AND full_at <= $2::timestamptz + interval '2 seconds')
		AND NOT EXISTS(SELECT 1 FROM player_boosters b WHERE b.player_id=$1 AND b.type='` + fishing.BoostAuto + `' AND b.starts_at <= $2::timestamptz AND b.expires_at > $2::timestamptz)`)
	v["collect_reminder"] = v["tank_full"]
	v["fishing_idle"] = exists(`SELECT NOT EXISTS(SELECT 1 FROM fishing_sessions WHERE player_id=$1 AND status='RUNNING')`)
	v["chest_round_open"] = exists(`SELECT EXISTS(SELECT 1 FROM chest_rounds WHERE player_id=$1 AND status='OPEN')`)
	day := func() time.Time { return gameday.Of(d.now()) }
	streakOpen := func(ctx context.Context, q db.Querier, id int64) (bool, error) {
		var claimed bool
		err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daily_streaks WHERE player_id=$1 AND last_claim_day=$2)`, id, day()).Scan(&claimed)
		return !claimed, err
	}
	comboOpen := func(ctx context.Context, q db.Querier, id int64) (bool, error) {
		var done bool
		err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM combo_days WHERE player_id=$1 AND day=$2 AND attempts>0)`, id, day()).Scan(&done)
		return !done, err
	}
	v["streak_ready"] = func(ctx context.Context, q db.Querier, p notify.Pending) (bool, error) {
		return streakOpen(ctx, q, p.PlayerID)
	}
	v["streak_at_risk"] = v["streak_ready"]
	v["combo_ready"] = func(ctx context.Context, q db.Querier, p notify.Pending) (bool, error) {
		return comboOpen(ctx, q, p.PlayerID)
	}
	v["combo_reminder"] = v["combo_ready"]
	v["daily_ready"] = func(ctx context.Context, q db.Querier, p notify.Pending) (bool, error) {
		a, err := streakOpen(ctx, q, p.PlayerID)
		if err != nil || a {
			return a, err
		}
		return comboOpen(ctx, q, p.PlayerID)
	}
	boosterOn := func(ctx context.Context, q db.Querier, p notify.Pending) (bool, error) {
		typ, _ := p.Params["type"].(string)
		exp, _ := p.Params["expires"].(float64)
		var ok bool
		err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM player_boosters WHERE player_id=$1 AND type=$2 AND expires_at = to_timestamp($3) AND expires_at > $4)`, p.PlayerID, typ, exp, d.now()).Scan(&ok)
		return ok, err
	}
	v["booster_expiring"] = boosterOn
	v["booster_expired"] = func(ctx context.Context, q db.Querier, p notify.Pending) (bool, error) {
		// the booster was not renewed meanwhile
		typ, _ := p.Params["type"].(string)
		var active bool
		err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM player_boosters WHERE player_id=$1 AND type=$2 AND expires_at > $3)`, p.PlayerID, typ, d.now()).Scan(&active)
		return !active, err
	}
}

// ---- hooks that need the HTTP layer

func (d *Deps) onBlockChanged(r *http.Request, playerID int64, blocked bool) {
	typ := "account_unblocked"
	if blocked {
		typ = "account_blocked"
	}
	_ = d.Notify.Send(r.Context(), d.Pool, playerID, typ, nil, fmt.Sprintf("block:%d:%d", playerID, d.now().UnixNano()))
}

func (d *Deps) onGiftDelivered(r *http.Request, playerID, giftID int64, delivered bool, reason string) {
	var key string
	_ = d.Pool.QueryRow(r.Context(), `SELECT gift_key FROM player_gifts WHERE id=$1`, giftID).Scan(&key)
	_, names, _ := d.Games.Gifts(r.Context())
	g := names[key]
	typ := "gift_claim_rejected"
	if delivered {
		typ = "gift_claim_delivered"
	}
	_ = d.Notify.Send(r.Context(), d.Pool, playerID, typ, map[string]any{"gift_en": g.NameEN, "gift_ru": g.NameRU, "reason": reason}, fmt.Sprintf("giftclaim:%d:%s", giftID, typ))
}

func (d *Deps) botUpdate(r *http.Request, u *telegram.Update) error {
	return d.Bot.Handle(r.Context(), u)
}

// ---- player endpoints: tournaments and notification preferences

func (d *Deps) botRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(d.MW.PlayerAuth(false))
		r.Get("/tournaments", httpx.Wrap(d.tournamentList))
		r.Get("/tournaments/{id}/leaderboard", httpx.Wrap(d.tournamentBoard))
		r.Get("/me/notifications", httpx.Wrap(d.notifPrefs))
		r.Patch("/me/notifications", httpx.Wrap(d.notifPrefsSet))
		r.With(d.MW.Limit(d.MoneyLimit, "money"), d.MW.Idempotency).Post("/tournaments/{id}/join", httpx.Wrap(d.tournamentJoin))
	})
}

func (d *Deps) tournamentList(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Tournaments.List(r.Context(), httpx.PlayerFrom(r.Context()))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"tournaments": l})
	return nil
}

func (d *Deps) tournamentJoin(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := d.Tournaments.Join(r.Context(), httpx.PlayerFrom(r.Context()).ID, id); err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) tournamentBoard(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	b, err := d.Tournaments.Leaderboard(r.Context(), httpx.PlayerFrom(r.Context()), id, int(queryInt(r, "page", 1)))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, b)
	return nil
}

type prefView struct {
	AllOff     bool      `json:"allOff"`
	Categories []prefCat `json:"categories"`
	BotStarted bool      `json:"botStarted"`
	BotBlocked bool      `json:"botBlocked"`
}

type prefCat struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	On        bool   `json:"on"`
	Mandatory bool   `json:"mandatory"`
}

func (d *Deps) notifPrefs(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	var off []byte
	var allOff, started, blocked bool
	if err := d.Pool.QueryRow(r.Context(), `SELECT notif_off, notif_all_off, bot_started, bot_blocked FROM players WHERE id=$1`, p.ID).Scan(&off, &allOff, &started, &blocked); err != nil {
		return err
	}
	var list []string
	_ = json.Unmarshal(off, &list)
	set := map[string]bool{}
	for _, c := range list {
		set[c] = true
	}
	v := prefView{AllOff: allOff, BotStarted: started, BotBlocked: blocked}
	for _, c := range notify.Switchable {
		name := notify.CategoryNames[c][0]
		if p.Lang() == apperr.RU {
			name = notify.CategoryNames[c][1]
		}
		v.Categories = append(v.Categories, prefCat{Key: string(c), Name: name, On: !allOff && !set[string(c)]})
	}
	pn := notify.CategoryNames[notify.CatPayments][0]
	if p.Lang() == apperr.RU {
		pn = notify.CategoryNames[notify.CatPayments][1]
	}
	v.Categories = append(v.Categories, prefCat{Key: string(notify.CatPayments), Name: pn, On: true, Mandatory: true})
	httpx.JSON(w, 200, v)
	return nil
}

type prefReq struct {
	AllOff *bool    `json:"allOff"`
	Off    []string `json:"off" validate:"omitempty,dive,max=20"`
}

func (d *Deps) notifPrefsSet(w http.ResponseWriter, r *http.Request) error {
	var in prefReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := httpx.PlayerFrom(r.Context())
	if in.AllOff != nil {
		if _, err := d.Pool.Exec(r.Context(), `UPDATE players SET notif_all_off=$2 WHERE id=$1`, p.ID, *in.AllOff); err != nil {
			return err
		}
	}
	if in.Off != nil {
		valid := map[string]bool{}
		for _, c := range notify.Switchable {
			valid[string(c)] = true
		}
		clean := []string{}
		for _, c := range in.Off {
			if !valid[c] {
				return apperr.Validation.With("field", "off")
			}
			clean = append(clean, c)
		}
		b, _ := json.Marshal(clean)
		if _, err := d.Pool.Exec(r.Context(), `UPDATE players SET notif_off=$2 WHERE id=$1`, p.ID, b); err != nil {
			return err
		}
	}
	return d.notifPrefs(w, r)
}

// BotSetup registers the webhook and the command menus at start-up.
func (d *Deps) BotSetup(ctx context.Context) error {
	if d.Cfg.BotToken == "" || d.Cfg.PublicAPIURL == "" {
		return nil
	}
	b := bot.New(d.Pool, d.TG, d.Settings, d.Cfg.WebAppURL)
	return b.Setup(ctx, strings.TrimRight(d.Cfg.PublicAPIURL, "/")+"/api/webhooks/telegram", d.Cfg.BotWebhookSec)
}

// AnnounceTournaments tells everybody a tournament started, and its participants that it ends soon (each once).
func (d *Deps) AnnounceTournaments(ctx context.Context) error {
	started, ending, err := d.Tournaments.Due(ctx, 6*time.Hour)
	if err != nil {
		return err
	}
	for _, id := range started {
		t, err := d.Tournaments.Get(ctx, id)
		if err != nil {
			continue
		}
		if _, err := d.Notify.BulkAll(ctx, "tournament_start", map[string]any{"name_en": t.NameEN, "name_ru": t.NameRU}, fmt.Sprintf("tstart:%d:", id)); err != nil {
			return err
		}
		if err := d.Tournaments.MarkNotified(ctx, id, "start"); err != nil {
			return err
		}
	}
	for _, id := range ending {
		t, err := d.Tournaments.Get(ctx, id)
		if err != nil {
			continue
		}
		ranks, err := d.Tournaments.Ranks(ctx, id)
		if err != nil {
			return err
		}
		for pid, rank := range ranks {
			if err := d.Notify.Send(ctx, d.Pool, pid, "tournament_ending", map[string]any{"name_en": t.NameEN, "name_ru": t.NameRU, "rank": rank}, fmt.Sprintf("tend:%d:%d", id, pid)); err != nil {
				return err
			}
		}
		if err := d.Tournaments.MarkNotified(ctx, id, "ending"); err != nil {
			return err
		}
	}
	return nil
}
