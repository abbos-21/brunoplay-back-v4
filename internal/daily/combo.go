package daily

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/ads"
	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/gameday"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

const (
	ComboSlots = 4
	counterKey = "combo_extra"
)

type ComboImage struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	NameEN string `json:"nameEn,omitempty"`
	NameRU string `json:"nameRu,omitempty"`
	Icon   string `json:"icon"`
	Active bool   `json:"active,omitempty"`
}

// Images returns the pool the player chooses from (the pattern itself is never part of this).
func (s *Service) Images(ctx context.Context, q db.Querier, lang apperr.Lang) ([]ComboImage, error) {
	rows, err := q.Query(ctx, `SELECT id, name_en, name_ru, icon FROM combo_images WHERE active ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ComboImage{}
	for rows.Next() {
		var x ComboImage
		if err := rows.Scan(&x.ID, &x.NameEN, &x.NameRU, &x.Icon); err != nil {
			return nil, err
		}
		x.Name = x.NameEN
		if lang == apperr.RU {
			x.Name = x.NameRU
		}
		x.NameEN, x.NameRU = "", ""
		out = append(out, x)
	}
	return out, rows.Err()
}

func randPerm(ids []int16, n int) ([]int16, error) {
	pool := append([]int16(nil), ids...)
	out := make([]int16, 0, n)
	for i := 0; i < n && len(pool) > 0; i++ {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(len(pool))))
		if err != nil {
			return nil, err
		}
		out = append(out, pool[j.Int64()])
		pool = append(pool[:j.Int64()], pool[j.Int64()+1:]...)
	}
	return out, nil
}

// pattern returns today's pattern, creating a random one when the admin scheduled none.
func (s *Service) pattern(ctx context.Context, tx db.Querier, day time.Time) ([]int16, error) {
	var items []int16
	err := tx.QueryRow(ctx, `SELECT items FROM combo_patterns WHERE day=$1`, day).Scan(&items)
	if err == nil {
		return items, nil
	}
	if !db.IsNoRows(err) {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM combo_images WHERE active ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var ids []int16
	for rows.Next() {
		var id int16
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) < ComboSlots {
		return nil, apperr.ConfigMissing.With("key", "combo.images")
	}
	items, err = randPerm(ids, ComboSlots)
	if err != nil {
		return nil, err
	}
	// two players may race to create the day's pattern: the first one wins, everyone reads the same row
	if _, err := tx.Exec(ctx, `INSERT INTO combo_patterns (day, items, source) VALUES ($1,$2,'auto') ON CONFLICT (day) DO NOTHING`, day, items); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `SELECT items FROM combo_patterns WHERE day=$1`, day).Scan(&items)
	return items, err
}

// EnsureTodayPattern creates today's pattern if needed (worker, at the start of the game day).
func (s *Service) EnsureTodayPattern(ctx context.Context) error {
	_, err := s.pattern(ctx, s.pool, gameday.Of(s.now()))
	return err
}

type comboDay struct {
	Attempts, ExtraUsed, Best int
	LastItems                 []int16
	LastCorrect               []bool
	Claimed, Doubled          bool
	ClaimedCoins              int64
}

func (s *Service) loadDay(ctx context.Context, q db.Querier, playerID int64, day time.Time, forUpdate bool) (comboDay, bool, error) {
	var d comboDay
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	err := q.QueryRow(ctx, `SELECT attempts, extra_used, best_correct, last_items, last_correct, claimed, doubled, claimed_coins
		FROM combo_days WHERE player_id=$1 AND day=$2`+lock, playerID, day).Scan(&d.Attempts, &d.ExtraUsed, &d.Best, &d.LastItems, &d.LastCorrect, &d.Claimed, &d.Doubled, &d.ClaimedCoins)
	if db.IsNoRows(err) {
		return comboDay{}, false, nil
	}
	return d, err == nil, err
}

type ComboStatus struct {
	Items             []ComboImage `json:"items"`
	Slots             int          `json:"slots"`
	AlreadyAttempted  bool         `json:"alreadyAttempted"` // the free attempt of today is used
	Attempts          int          `json:"attempts"`
	ExtraAttemptsLeft int64        `json:"extraAttemptsLeft"`
	BestCorrect       int          `json:"bestCorrect"`
	LastResult        *int         `json:"lastResult"` // correct pictures of the last attempt
	LastCorrect       []bool       `json:"lastCorrect"`
	LastItems         []int16      `json:"lastItems"`
	Reward            int64        `json:"reward"` // coins of the best attempt (claimed or not)
	CanClaim          bool         `json:"canClaim"`
	Claimed           bool         `json:"claimed"`
	CoinsPerCorrect   int64        `json:"coinsPerCorrect"`
	ResetsAt          time.Time    `json:"resetsAt"`
}

func (s *Service) extraLimit(ctx context.Context) int64 {
	v, err := s.settings.Int(ctx, settings.KeyAdsComboExtra)
	if err != nil {
		return 0
	}
	return v
}

// Combo returns the state of today's combo for a player. It never contains the pattern.
func (s *Service) Combo(ctx context.Context, playerID int64, lang apperr.Lang) (*ComboStatus, error) {
	per, err := s.settings.Int(ctx, settings.KeyComboPerCorrect)
	if err != nil {
		return nil, err
	}
	now := s.now()
	day := gameday.Of(now)
	imgs, err := s.Images(ctx, s.pool, lang)
	if err != nil {
		return nil, err
	}
	d, _, err := s.loadDay(ctx, s.pool, playerID, day, false)
	if err != nil {
		return nil, err
	}
	st := &ComboStatus{Items: imgs, Slots: ComboSlots, AlreadyAttempted: d.Attempts > 0, Attempts: d.Attempts, BestCorrect: d.Best,
		Reward: int64(d.Best) * per, Claimed: d.Claimed, CoinsPerCorrect: per, ResetsAt: gameday.NextReset(now)}
	if d.Claimed {
		st.Reward = d.ClaimedCoins
	}
	st.CanClaim = d.Attempts > 0 && !d.Claimed && d.Best > 0
	if d.Attempts > 0 {
		n := 0
		for _, c := range d.LastCorrect {
			if c {
				n++
			}
		}
		st.LastResult, st.LastCorrect, st.LastItems = &n, d.LastCorrect, d.LastItems
	}
	if limit := s.extraLimit(ctx); limit > 0 && d.Attempts > 0 && !d.Claimed {
		st.ExtraAttemptsLeft = limit - int64(d.ExtraUsed)
		if st.ExtraAttemptsLeft < 0 {
			st.ExtraAttemptsLeft = 0
		}
	}
	return st, nil
}

type ComboResult struct {
	Correct           []bool `json:"correct"`
	CorrectCount      int    `json:"correctCount"`
	BestCorrect       int    `json:"bestCorrect"`
	Reward            int64  `json:"reward"` // coins the best attempt is worth (paid on claim)
	Attempts          int    `json:"attempts"`
	ExtraAttemptsLeft int64  `json:"extraAttemptsLeft"`
}

// SubmitCombo scores one attempt. The first attempt of the day is free; further ones need a watched ad and an admin daily limit.
// The answer is never returned, only right or wrong per slot.
func (s *Service) SubmitCombo(ctx context.Context, playerID int64, items []int16, adID string) (*ComboResult, error) {
	per, err := s.settings.Int(ctx, settings.KeyComboPerCorrect)
	if err != nil {
		return nil, err
	}
	if len(items) != ComboSlots {
		return nil, apperr.ComboInvalid
	}
	seen := map[int16]bool{}
	for _, id := range items {
		if seen[id] {
			return nil, apperr.ComboInvalid
		}
		seen[id] = true
	}
	now := s.now()
	day := gameday.Of(now)
	limit := s.extraLimit(ctx)
	var res *ComboResult
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
			return err
		}
		var valid int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM combo_images WHERE active AND id = ANY($1)`, items).Scan(&valid); err != nil {
			return err
		}
		if valid != ComboSlots {
			return apperr.ComboInvalid
		}
		d, _, err := s.loadDay(ctx, tx, playerID, day, true)
		if err != nil {
			return err
		}
		if d.Claimed {
			return apperr.ComboClaimed
		}
		extra := d.Attempts > 0
		if extra {
			if limit <= 0 || int64(d.ExtraUsed) >= limit {
				return apperr.ComboAttempted
			}
			if err := s.ads.Consume(ctx, tx, playerID, adID, ads.ComboExtra, "combo_extra"); err != nil {
				return err
			}
		}
		pat, err := s.pattern(ctx, tx, day)
		if err != nil {
			return err
		}
		correct := make([]bool, ComboSlots)
		n := 0
		for i := range items {
			correct[i] = items[i] == pat[i]
			if correct[i] {
				n++
			}
		}
		best := d.Best
		if n > best {
			best = n
		}
		attempts := d.Attempts + 1
		extraUsed := d.ExtraUsed
		if extra {
			extraUsed++
		}
		var adArg any
		if adID != "" && extra {
			adArg = adID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO combo_attempts (player_id, day, attempt_no, items, correct, correct_count, ad_view_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			playerID, day, attempts, items, correct, n, adArg); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO combo_days (player_id, day, attempts, extra_used, best_correct, last_items, last_correct) VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (player_id, day) DO UPDATE SET attempts=EXCLUDED.attempts, extra_used=EXCLUDED.extra_used, best_correct=EXCLUDED.best_correct,
			last_items=EXCLUDED.last_items, last_correct=EXCLUDED.last_correct`, playerID, day, attempts, extraUsed, best, items, correct); err != nil {
			return err
		}
		left := limit - int64(extraUsed)
		if left < 0 || limit <= 0 {
			left = 0
		}
		res = &ComboResult{Correct: correct, CorrectCount: n, BestCorrect: best, Reward: int64(best) * per, Attempts: attempts, ExtraAttemptsLeft: left}
		return nil
	})
	return res, err
}

type ComboClaim struct {
	Reward     int64 `json:"reward"`
	Doubled    bool  `json:"doubled"`
	NewBalance int64 `json:"newBalance"`
}

// ClaimCombo pays the best attempt of the day (twice with a watched ad) and locks the combo until the next day.
func (s *Service) ClaimCombo(ctx context.Context, playerID int64, double bool, adID string) (*ComboClaim, error) {
	per, err := s.settings.Int(ctx, settings.KeyComboPerCorrect)
	if err != nil {
		return nil, err
	}
	now := s.now()
	day := gameday.Of(now)
	var res *ComboClaim
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
			return err
		}
		d, ok, err := s.loadDay(ctx, tx, playerID, day, true)
		if err != nil {
			return err
		}
		if !ok || d.Attempts == 0 {
			return apperr.ComboNothing
		}
		if d.Claimed {
			return apperr.ComboClaimed
		}
		coins := int64(d.Best) * per
		if coins <= 0 {
			return apperr.ComboNothing // nothing won yet: the day stays open for more attempts
		}
		if double {
			if err := s.ads.Consume(ctx, tx, playerID, adID, ads.DoubleCombo, "double_combo"); err != nil {
				return err
			}
			coins *= 2
		}
		bal, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: coins, Reason: ledger.ReasonDailyCombo,
			RefType: "combo", RefID: gameday.Key(now), IdemKey: fmt.Sprintf("combo:%d:%s", playerID, gameday.Key(now)), Earned: true,
			Meta: map[string]any{"best": d.Best, "doubled": double}})
		if err == ledger.ErrDuplicate {
			return apperr.ComboClaimed
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE combo_days SET claimed=TRUE, claimed_coins=$3, doubled=$4 WHERE player_id=$1 AND day=$2`, playerID, day, coins, double); err != nil {
			return err
		}
		res = &ComboClaim{Reward: coins, Doubled: double, NewBalance: bal}
		for _, h := range s.OnComboClaimed {
			if err := h(ctx, tx, playerID, coins); err != nil {
				return err
			}
		}
		return nil
	})
	return res, err
}

// ---- guards used before an ad is offered

// CanExtraAttempt reports whether an extra-attempt ad makes sense right now.
func (s *Service) CanExtraAttempt(ctx context.Context, playerID int64) error {
	now := s.now()
	d, ok, err := s.loadDay(ctx, s.pool, playerID, gameday.Of(now), false)
	if err != nil {
		return err
	}
	limit := s.extraLimit(ctx)
	if !ok || d.Attempts == 0 || d.Claimed || limit <= 0 || int64(d.ExtraUsed) >= limit {
		return apperr.ExtraLimit
	}
	return nil
}

// CanClaimDouble reports whether a ×2 ad for the streak or the combo makes sense right now.
func (s *Service) CanClaimDouble(ctx context.Context, playerID int64, placement string) error {
	now := s.now()
	if placement == ads.DoubleStreak {
		r, err := s.loadStreak(ctx, s.pool, playerID, false)
		if err != nil {
			return err
		}
		if _, claimedToday, _, _, _, _ := plan(r, now); claimedToday {
			return apperr.StreakClaimed
		}
		return nil
	}
	d, ok, err := s.loadDay(ctx, s.pool, playerID, gameday.Of(now), false)
	if err != nil {
		return err
	}
	if !ok || d.Attempts == 0 || d.Best == 0 {
		return apperr.ComboNothing
	}
	if d.Claimed {
		return apperr.ComboClaimed
	}
	return nil
}

// ---- admin

type Pattern struct {
	Day    string  `json:"day"`
	Items  []int16 `json:"items"`
	Source string  `json:"source"`
}

// SetPattern schedules a pattern for a future day (a day that has started is read-only).
func (s *Service) SetPattern(ctx context.Context, day time.Time, items []int16, adminID int64) error {
	day = gameday.Of(day)
	if !day.After(gameday.Of(s.now())) {
		return apperr.BadRequest.With("reason", "the day has started")
	}
	if len(items) != ComboSlots {
		return apperr.ComboInvalid
	}
	seen := map[int16]bool{}
	for _, id := range items {
		if seen[id] {
			return apperr.ComboInvalid
		}
		seen[id] = true
	}
	var valid int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM combo_images WHERE active AND id = ANY($1)`, items).Scan(&valid); err != nil {
		return err
	}
	if valid != ComboSlots {
		return apperr.ComboInvalid
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO combo_patterns (day, items, source, created_by) VALUES ($1,$2,'admin',$3)
		ON CONFLICT (day) DO UPDATE SET items=EXCLUDED.items, source='admin', created_by=EXCLUDED.created_by`, day, items, adminID)
	return err
}

func (s *Service) DeletePattern(ctx context.Context, day time.Time) error {
	day = gameday.Of(day)
	if !day.After(gameday.Of(s.now())) {
		return apperr.BadRequest.With("reason", "the day has started")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM combo_patterns WHERE day=$1`, day)
	return err
}

type PatternDay struct {
	Day        string  `json:"day"`
	Items      []int16 `json:"items"`
	Source     string  `json:"source"`
	Attempters int64   `json:"attempters"`
	AvgBest    float64 `json:"avgBest"`
	Claimed    int64   `json:"claimed"`
}

// Patterns lists the patterns of a date range with how many players tried them (for the admin calendar).
func (s *Service) Patterns(ctx context.Context, from, to time.Time) ([]PatternDay, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.day, p.items, p.source,
		(SELECT count(*) FROM combo_days d WHERE d.day=p.day),
		COALESCE((SELECT avg(best_correct) FROM combo_days d WHERE d.day=p.day),0),
		(SELECT count(*) FROM combo_days d WHERE d.day=p.day AND claimed)
		FROM combo_patterns p WHERE p.day BETWEEN $1 AND $2 ORDER BY p.day`, gameday.Of(from), gameday.Of(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PatternDay{}
	for rows.Next() {
		var p PatternDay
		var d time.Time
		if err := rows.Scan(&d, &p.Items, &p.Source, &p.Attempters, &p.AvgBest, &p.Claimed); err != nil {
			return nil, err
		}
		p.Day = d.Format("2006-01-02")
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateImage edits one of the 8 pictures.
func (s *Service) UpdateImage(ctx context.Context, id int, nameEN, nameRU, icon *string, active *bool) error {
	if id < 1 || id > 8 {
		return apperr.NotFound
	}
	if active != nil && !*active {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM combo_images WHERE active AND id<>$1`, id).Scan(&n); err != nil {
			return err
		}
		if n < ComboSlots {
			return apperr.Validation.With("field", "active")
		}
	}
	_, err := s.pool.Exec(ctx, `UPDATE combo_images SET name_en=COALESCE($2,name_en), name_ru=COALESCE($3,name_ru), icon=COALESCE($4,icon), active=COALESCE($5,active) WHERE id=$1`,
		id, nameEN, nameRU, icon, active)
	return err
}

func (s *Service) AllImages(ctx context.Context) ([]ComboImage, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name_en, name_ru, icon, active FROM combo_images ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ComboImage{}
	for rows.Next() {
		var x ComboImage
		if err := rows.Scan(&x.ID, &x.NameEN, &x.NameRU, &x.Icon, &x.Active); err != nil {
			return nil, err
		}
		x.Name = x.NameEN
		out = append(out, x)
	}
	return out, rows.Err()
}
