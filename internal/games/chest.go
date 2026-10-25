package games

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

type chestEntry struct {
	Type   string  `json:"type"`
	Value  int64   `json:"value"`
	Weight float64 `json:"weight"`
	Gift   string  `json:"gift"`
}

func (s *Service) chestPool(ctx context.Context) ([]Prize, error) {
	list, err := settings.Get[[]chestEntry](ctx, s.settings, settings.KeyChestPool)
	if err != nil {
		return nil, err
	}
	_, gifts, err := s.Gifts(ctx)
	var out []Prize
	for _, e := range list {
		p := Prize{Type: e.Type, Value: e.Value, Weight: e.Weight, Gift: e.Gift}
		switch e.Type {
		case TypeGift:
			if err != nil {
				return nil, err
			}
			g, ok := gifts[e.Gift]
			if !ok {
				continue
			}
			p.Value = g.StarValue
		case TypeCoins, TypeStars:
			if e.Value <= 0 {
				continue
			}
		default:
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, apperr.ConfigMissing.With("key", settings.KeyChestPool)
	}
	return out, nil
}

type ChestPrize struct {
	Type   string `json:"type"`
	Value  int64  `json:"value"`
	Gift   string `json:"gift,omitempty"`
	Label  string `json:"label"`
	GiftID int64  `json:"giftId,omitempty"`
}

type ChestOpened struct {
	Slot  int        `json:"slot"`
	Prize ChestPrize `json:"prize"`
}

type ChestRound struct {
	ID        int64         `json:"roundId"`
	Picks     int           `json:"picks"`
	Opened    []ChestOpened `json:"opened"`
	Remaining int           `json:"remaining"`
	CreatedAt time.Time     `json:"createdAt"`
}

type ChestState struct {
	Round     *ChestRound `json:"round"`
	CostStars int64       `json:"roundCostStars"`
	Picks     int         `json:"picksPerRound"`
}

type roundRow struct {
	ID      int64
	Player  int64
	Status  string
	Picks   int
	Prizes  []Prize
	Opened  map[string]int // slot -> prize index
	Created time.Time
	Gifts   map[string]int64 // slot -> gift id (kept inside opened meta)
}

func (s *Service) loadRound(ctx context.Context, q db.Querier, playerID int64, forUpdate bool) (*roundRow, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	var r roundRow
	var prizes, opened []byte
	err := q.QueryRow(ctx, `SELECT id, player_id, status, picks, prizes, opened, created_at FROM chest_rounds WHERE player_id=$1 AND status='OPEN'`+lock, playerID).
		Scan(&r.ID, &r.Player, &r.Status, &r.Picks, &prizes, &opened, &r.Created)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(prizes, &r.Prizes); err != nil {
		return nil, err
	}
	r.Opened = map[string]int{}
	if err := json.Unmarshal(opened, &r.Opened); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Service) chestPrize(p Prize, lang apperr.Lang, names map[string]Gift, giftID int64) ChestPrize {
	cp := ChestPrize{Type: p.Type, Value: p.Value, Gift: p.Gift, GiftID: giftID}
	switch p.Type {
	case TypeGift:
		cp.Label = names[p.Gift].Name(lang)
	case TypeCoins:
		cp.Label = "Coins"
		if lang == apperr.RU {
			cp.Label = "Монеты"
		}
	default:
		cp.Label = "Stars"
		if lang == apperr.RU {
			cp.Label = "Звёзды"
		}
	}
	return cp
}

func (s *Service) roundView(ctx context.Context, r *roundRow, lang apperr.Lang) *ChestRound {
	_, names, _ := s.Gifts(ctx)
	v := &ChestRound{ID: r.ID, Picks: r.Picks, Remaining: r.Picks - len(r.Opened), CreatedAt: r.Created, Opened: []ChestOpened{}}
	for slot, idx := range r.Opened {
		n, err := strconv.Atoi(slot)
		if err != nil || idx >= len(r.Prizes) {
			continue
		}
		v.Opened = append(v.Opened, ChestOpened{Slot: n, Prize: s.chestPrize(r.Prizes[idx], lang, names, 0)})
	}
	return v
}

// Chest returns the open round (if any), so a player who left can continue.
func (s *Service) Chest(ctx context.Context, playerID int64, lang apperr.Lang) (*ChestState, error) {
	cost, err := s.settings.Int(ctx, settings.KeyChestCost)
	if err != nil {
		return nil, err
	}
	picks, err := s.settings.Int(ctx, settings.KeyChestPicks)
	if err != nil {
		return nil, err
	}
	st := &ChestState{CostStars: cost, Picks: int(picks)}
	r, err := s.loadRound(ctx, s.pool, playerID, false)
	if err != nil {
		return nil, err
	}
	if r != nil {
		st.Round = s.roundView(ctx, r, lang)
	}
	return st, nil
}

// StartChestRound pays once and draws every prize of the round now, in a hidden order.
func (s *Service) StartChestRound(ctx context.Context, playerID int64, lang apperr.Lang) (*ChestState, error) {
	cost, err := s.settings.Int(ctx, settings.KeyChestCost)
	if err != nil {
		return nil, err
	}
	picksI, err := s.settings.Int(ctx, settings.KeyChestPicks)
	if err != nil {
		return nil, err
	}
	picks := int(picksI)
	pool, err := s.chestPool(ctx)
	if err != nil {
		return nil, err
	}
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
			return err
		}
		if r, err := s.loadRound(ctx, tx, playerID, true); err != nil {
			return err
		} else if r != nil {
			return apperr.ChestRoundOpen
		}
		if cost > 0 {
			if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: -cost, Reason: ledger.ReasonChestCharge,
				RefType: "chest", RefID: "round"}); err != nil {
				return err
			}
			if err := s.recordSpend(ctx, tx, GameChest, "round", cost); err != nil {
				return err
			}
			if err := s.spent(ctx, tx, playerID, GameChest, cost); err != nil {
				return err
			}
		}
		prizes := make([]Prize, 0, picks)
		for i := 0; i < picks; i++ {
			drawn, err := pick(pool)
			if err != nil {
				return err
			}
			p, _, err := s.guard(ctx, tx, playerID, GameChest, "round", drawn, pool, picks-1-i)
			if err != nil {
				return err
			}
			if err := s.recordPaid(ctx, tx, GameChest, "round", s.starValue(p, s.coinStars(ctx))); err != nil {
				return err
			}
			p.Weight = 0
			prizes = append(prizes, p)
		}
		b, _ := json.Marshal(prizes)
		var roundID int64
		err := tx.QueryRow(ctx, `INSERT INTO chest_rounds (player_id, status, cost, picks, prizes, created_at) VALUES ($1,'OPEN',$2,$3,$4,$5) RETURNING id`, playerID, cost, picks, b, s.now()).Scan(&roundID)
		if db.IsUnique(err, "chest_one_open") {
			return apperr.ChestRoundOpen
		}
		if err != nil {
			return err
		}
		for _, h := range s.OnChestRound {
			if err := h(ctx, tx, playerID, roundID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Chest(ctx, playerID, lang)
}

// award pays one prize of a round.
func (s *Service) award(ctx context.Context, tx pgx.Tx, playerID, roundID int64, idx int, p Prize, spentShare int64) (int64, error) {
	var giftID int64
	switch p.Type {
	case TypeCoins:
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Coin, Amount: p.Value, Reason: ledger.ReasonChestPrize,
			RefType: "chest", RefID: ledger.RefID(roundID), IdemKey: fmt.Sprintf("chest:%d:%d", roundID, idx), Earned: true}); err != nil && err != ledger.ErrDuplicate {
			return 0, err
		}
	case TypeStars:
		if _, err := ledger.Apply(ctx, tx, ledger.Entry{PlayerID: playerID, Currency: ledger.Star, Amount: p.Value, Reason: ledger.ReasonChestPrize,
			RefType: "chest", RefID: ledger.RefID(roundID), IdemKey: fmt.Sprintf("chest:%d:%d", roundID, idx)}); err != nil && err != ledger.ErrDuplicate {
			return 0, err
		}
	case TypeGift:
		id, err := s.grant(ctx, tx, playerID, p, "chest", ledger.RefID(roundID))
		if err != nil {
			return 0, err
		}
		giftID = id
		if err := s.feed(ctx, tx, playerID, GameChest, p, spentShare); err != nil {
			return 0, err
		}
	}
	return giftID, nil
}

type PickResult struct {
	Prize     ChestPrize `json:"prize"`
	Slot      int        `json:"slot"`
	Remaining int        `json:"remaining"`
	Finished  bool       `json:"finished"`
	// when the round ends: everything the player won, for the win screen
	Results       []ChestPrize `json:"results,omitempty"`
	TotalCoinsWon int64        `json:"totalCoinsWon,omitempty"`
	TotalStarsWon int64        `json:"totalStarsWon,omitempty"`
}

// ChestSlots is how many chests the app shows (the API accepts slots 0..ChestSlots-1).
const ChestSlots = 12

// PickChest opens one chest (a slot). Opening the same slot again returns the same prize and pays nothing more.
func (s *Service) PickChest(ctx context.Context, playerID int64, slot int, lang apperr.Lang) (*PickResult, error) {
	_, names, _ := s.Gifts(ctx)
	var res *PickResult
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.players.Lock(ctx, tx, playerID); err != nil {
			return err
		}
		r, err := s.loadRound(ctx, tx, playerID, true)
		if err != nil {
			return err
		}
		if r == nil {
			return apperr.ChestNoRound
		}
		// the app shows ChestSlots chests and the player opens any Picks of them; the prizes were drawn at payment
		if slot < 0 || slot >= ChestSlots {
			return apperr.BadRequest
		}
		key := strconv.Itoa(slot)
		if idx, ok := r.Opened[key]; ok {
			res = &PickResult{Prize: s.chestPrize(r.Prizes[idx], lang, names, 0), Slot: slot, Remaining: r.Picks - len(r.Opened)}
			return nil
		}
		idx := len(r.Opened)
		p := r.Prizes[idx]
		share := int64(0)
		if r.Picks > 0 {
			var cost int64
			_ = tx.QueryRow(ctx, `SELECT cost FROM chest_rounds WHERE id=$1`, r.ID).Scan(&cost)
			share = cost / int64(r.Picks)
		}
		gid, err := s.award(ctx, tx, playerID, r.ID, idx, p, share)
		if err != nil {
			return err
		}
		r.Opened[key] = idx
		ob, _ := json.Marshal(r.Opened)
		finished := len(r.Opened) >= r.Picks
		status := "OPEN"
		var fin any
		if finished {
			status = "DONE"
			fin = s.now()
		}
		if _, err := tx.Exec(ctx, `UPDATE chest_rounds SET opened=$2, status=$3, finished_at=$4 WHERE id=$1`, r.ID, ob, status, fin); err != nil {
			return err
		}
		res = &PickResult{Prize: s.chestPrize(p, lang, names, gid), Slot: slot, Remaining: r.Picks - len(r.Opened), Finished: finished}
		if finished {
			for _, pr := range r.Prizes {
				res.Results = append(res.Results, s.chestPrize(pr, lang, names, 0))
				switch pr.Type {
				case TypeCoins:
					res.TotalCoinsWon += pr.Value
				case TypeStars:
					res.TotalStarsWon += pr.Value
				}
			}
		}
		return nil
	})
	return res, err
}

// FinishStaleRounds opens the remaining chests of rounds left open longer than chest.round_expiry_hours (0 = never), so a player
// never loses what they paid for. Returns how many rounds were finished.
func (s *Service) FinishStaleRounds(ctx context.Context) (int, error) {
	hours, err := s.settings.Int(ctx, settings.KeyChestExpiryH)
	if err != nil || hours <= 0 {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT player_id FROM chest_rounds WHERE status='OPEN' AND created_at < $1 LIMIT 100`, s.now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, pid := range ids {
		err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
			r, err := s.loadRound(ctx, tx, pid, true)
			if err != nil || r == nil {
				return err
			}
			var cost int64
			_ = tx.QueryRow(ctx, `SELECT cost FROM chest_rounds WHERE id=$1`, r.ID).Scan(&cost)
			for idx := len(r.Opened); idx < r.Picks; idx++ {
				if _, err := s.award(ctx, tx, pid, r.ID, idx, r.Prizes[idx], cost/int64(r.Picks)); err != nil {
					return err
				}
				r.Opened["auto"+strconv.Itoa(idx)] = idx
			}
			ob, _ := json.Marshal(r.Opened)
			_, err = tx.Exec(ctx, `UPDATE chest_rounds SET opened=$2, status='DONE', finished_at=$3 WHERE id=$1`, r.ID, ob, s.now())
			return err
		})
		if err == nil {
			n++
		}
	}
	return n, nil
}
