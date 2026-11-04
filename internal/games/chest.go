package games

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/ledger"
	"brunoplay/internal/settings"
)

// The chest game: a round costs chest.round_cost_stars and shows 3 chests. One of them holds the prize (the price times
// chest.prize_multiplier, in stars) and the other two are empty. The server hides the prize at payment, the player opens one chest:
// the chance is 1 in 3, so a multiplier of 3 pays back 100% and 2.7 pays back 90%.
//
// Rounds paid before this version (slot_mode = false) had several picks and a prize pool; the worker opens them in full with
// the prizes they already carry, so nobody loses what they paid for.

// ChestSlots is how many chests a round shows (the API accepts slots 0..ChestSlots-1).
const ChestSlots = 3

// legacyChestSlots is how many chests the old version showed; a round that is still open from then accepts these slots.
const legacyChestSlots = 12

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
	Round      *ChestRound `json:"round"`
	CostStars  int64       `json:"roundCostStars"`
	Picks      int         `json:"picksPerRound"`
	Multiplier float64     `json:"multiplier"`
	WinStars   int64       `json:"winStars"`
}

type roundRow struct {
	ID       int64
	Player   int64
	Status   string
	Picks    int
	Prizes   []Prize
	Opened   map[string]int // slot -> prize index
	Created  time.Time
	SlotMode bool // the prize of slot n is Prizes[n]; otherwise the k-th chest opened gets Prizes[k]
}

func (s *Service) loadRound(ctx context.Context, q db.Querier, playerID int64, forUpdate bool) (*roundRow, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	var r roundRow
	var prizes, opened []byte
	err := q.QueryRow(ctx, `SELECT id, player_id, status, picks, prizes, opened, created_at, slot_mode FROM chest_rounds WHERE player_id=$1 AND status='OPEN'`+lock, playerID).
		Scan(&r.ID, &r.Player, &r.Status, &r.Picks, &prizes, &opened, &r.Created, &r.SlotMode)
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
	if p.Type == TypeGift {
		cp.Label = names[p.Gift].Name(lang)
	} else {
		cp.Label = prizeLabel(p, lang)
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

// chestRules reads the round price and what the prize chest pays.
func (s *Service) chestRules(ctx context.Context) (cost int64, mult float64, win int64, err error) {
	if cost, err = s.settings.Int(ctx, settings.KeyChestCost); err != nil {
		return 0, 0, 0, err
	}
	if mult, err = s.settings.Float(ctx, settings.KeyChestPrizeMult); err != nil {
		return 0, 0, 0, err
	}
	return cost, mult, winAmount(cost, mult), nil
}

// Chest returns the open round (if any), so a player who left can continue.
func (s *Service) Chest(ctx context.Context, playerID int64, lang apperr.Lang) (*ChestState, error) {
	cost, mult, win, err := s.chestRules(ctx)
	if err != nil {
		return nil, err
	}
	st := &ChestState{CostStars: cost, Picks: 1, Multiplier: mult, WinStars: win}
	r, err := s.loadRound(ctx, s.pool, playerID, false)
	if err != nil {
		return nil, err
	}
	if r != nil {
		st.Round = s.roundView(ctx, r, lang)
	}
	return st, nil
}

// StartChestRound pays once and hides the prize in one of the 3 chests now.
func (s *Service) StartChestRound(ctx context.Context, playerID int64, lang apperr.Lang) (*ChestState, error) {
	cost, _, win, err := s.chestRules(ctx)
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
		// the prize goes into one of the three chests, always. The payout guard does not apply here: the odds (1 in 3) and the
		// multiplier already cap the return, and a round with three empty chests would look like a bug to the player.
		// The prize counts as paid out only when its chest is opened (see award).
		prizeSlot := int(randFloat() * ChestSlots)
		if prizeSlot >= ChestSlots {
			prizeSlot = ChestSlots - 1
		}
		prizes := make([]Prize, ChestSlots)
		for i := range prizes {
			prizes[i] = Prize{Type: TypeEmpty}
		}
		if win > 0 {
			prizes[prizeSlot] = Prize{Type: TypeStars, Value: win}
		}
		b, _ := json.Marshal(prizes)
		var roundID int64
		err := tx.QueryRow(ctx, `INSERT INTO chest_rounds (player_id, status, cost, picks, prizes, slot_mode, created_at) VALUES ($1,'OPEN',$2,1,$3,TRUE,$4) RETURNING id`,
			playerID, cost, b, s.now()).Scan(&roundID)
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
		} else if err == nil {
			if err := s.recordPaid(ctx, tx, GameChest, "round", p.Value); err != nil {
				return 0, err
			}
		}
		if err := s.feed(ctx, tx, playerID, GameChest, p, spentShare); err != nil {
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
	// when the round ends: what was in each chest (by slot), so the app can show where the prize was
	Reveal []ChestPrize `json:"reveal,omitempty"`
}

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
		limit := ChestSlots
		if !r.SlotMode {
			limit = legacyChestSlots
		}
		if slot < 0 || slot >= limit {
			return apperr.BadRequest
		}
		key := strconv.Itoa(slot)
		if idx, ok := r.Opened[key]; ok {
			res = &PickResult{Prize: s.chestPrize(r.Prizes[idx], lang, names, 0), Slot: slot, Remaining: r.Picks - len(r.Opened)}
			return nil
		}
		// the new rounds: the chest decides the prize; the old ones: the k-th chest opened gets the k-th prize
		idx := len(r.Opened)
		if r.SlotMode {
			idx = slot
		}
		if idx >= len(r.Prizes) {
			return apperr.BadRequest
		}
		p := r.Prizes[idx]
		var cost int64
		_ = tx.QueryRow(ctx, `SELECT cost FROM chest_rounds WHERE id=$1`, r.ID).Scan(&cost)
		share := int64(0)
		if r.Picks > 0 {
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
			if r.SlotMode {
				res.Results = []ChestPrize{s.chestPrize(p, lang, names, gid)}
				if p.Type == TypeStars {
					res.TotalStarsWon = p.Value
				}
				for _, pr := range r.Prizes {
					res.Reveal = append(res.Reveal, s.chestPrize(pr, lang, names, 0))
				}
				return nil
			}
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

// FinishStaleRounds opens the rest of rounds that nobody will finish, so a player never loses what they paid for: rounds left open
// longer than chest.round_expiry_hours (0 = never), and every round that was paid in the old version (several picks, a prize
// pool). A round of the new game that expires opens one chest chosen at random. Returns how many rounds were finished.
func (s *Service) FinishStaleRounds(ctx context.Context) (int, error) {
	hours, err := s.settings.Int(ctx, settings.KeyChestExpiryH)
	if err != nil {
		return 0, nil
	}
	expired := time.Time{} // nothing is expired when the limit is 0
	if hours > 0 {
		expired = s.now().Add(-time.Duration(hours) * time.Hour)
	}
	rows, err := s.pool.Query(ctx, `SELECT player_id FROM chest_rounds WHERE status='OPEN' AND (NOT slot_mode OR created_at < $1) LIMIT 100`, expired)
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
			if r.SlotMode {
				slot := int(math.Floor(randFloat() * ChestSlots))
				if slot >= ChestSlots {
					slot = ChestSlots - 1
				}
				if _, err := s.award(ctx, tx, pid, r.ID, slot, r.Prizes[slot], cost); err != nil {
					return err
				}
				r.Opened["auto"+strconv.Itoa(slot)] = slot
			} else {
				for idx := len(r.Opened); idx < r.Picks; idx++ {
					if _, err := s.award(ctx, tx, pid, r.ID, idx, r.Prizes[idx], cost/int64(r.Picks)); err != nil {
						return err
					}
					r.Opened["auto"+strconv.Itoa(idx)] = idx
				}
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
