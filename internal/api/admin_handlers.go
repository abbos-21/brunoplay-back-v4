package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/httpx"
	"brunoplay/internal/ledger"
)

// ---- admin auth

type loginReq struct {
	Email    string `json:"email" validate:"required,email,max=200"`
	Password string `json:"password" validate:"required,max=200"`
}

func (d *Deps) adminLogin(w http.ResponseWriter, r *http.Request) error {
	var in loginReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	s, err := d.Admin.Login(r.Context(), in.Email, in.Password, httpx.IP(r.Context()), r.UserAgent())
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, &httpx.AdminIdentity{ID: s.Admin.ID, Email: s.Admin.Email, Role: s.Admin.Role}, "admin.login", "admin", itoa(s.Admin.ID), nil, nil)
	httpx.JSON(w, 200, s)
	return nil
}

type refreshReq struct {
	RefreshToken string `json:"refreshToken" validate:"required,max=200"`
}

func (d *Deps) adminRefresh(w http.ResponseWriter, r *http.Request) error {
	var in refreshReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	s, err := d.Admin.Refresh(r.Context(), in.RefreshToken)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, s)
	return nil
}

func (d *Deps) adminLogout(w http.ResponseWriter, r *http.Request) error {
	var in refreshReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := d.Admin.Logout(r.Context(), in.RefreshToken); err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminMe(w http.ResponseWriter, r *http.Request) error {
	who := httpx.AdminFrom(r.Context())
	a, err := d.Admin.Get(r.Context(), who.ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, a)
	return nil
}

type passwordReq struct {
	Current string `json:"current" validate:"required,max=200"`
	New     string `json:"new" validate:"required,max=200"`
}

func (d *Deps) adminPassword(w http.ResponseWriter, r *http.Request) error {
	var in passwordReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	if err := d.Admin.ChangeOwnPassword(r.Context(), who.ID, in.Current, in.New); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "admin.password_change", "admin", itoa(who.ID), nil, nil)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// ---- settings

func (d *Deps) adminSettings(w http.ResponseWriter, r *http.Request) error {
	v, err := d.Settings.All(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"settings": v})
	return nil
}

func (d *Deps) adminSettingsMissing(w http.ResponseWriter, r *http.Request) error {
	v, err := d.Settings.MissingKeys(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"missing": v})
	return nil
}

func (d *Deps) adminSettingsHistory(w http.ResponseWriter, r *http.Request) error {
	h, err := d.Settings.History(r.Context(), r.URL.Query().Get("key"), int(queryInt(r, "limit", 50)))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"history": h})
	return nil
}

type settingReq struct {
	Value any `json:"value"`
}

func (d *Deps) adminSettingSet(w http.ResponseWriter, r *http.Request) error {
	who := httpx.AdminFrom(r.Context())
	key := chi.URLParam(r, "key")
	def, ok := d.Settings.Def(key)
	if !ok {
		return apperr.NotFound
	}
	if !admin.Can(who.Role, "settings.write."+def.Group) {
		return apperr.Forbidden
	}
	var in settingReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	var before any
	if old, err := d.Settings.Raw(r.Context(), key); err == nil {
		before = old
	}
	if err := d.Settings.Set(r.Context(), key, in.Value, who.ID); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "settings.set", "setting", key, before, in.Value)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// ---- admins and audit

func (d *Deps) adminList(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Admin.List(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"admins": l, "roles": admin.Roles})
	return nil
}

type adminCreateReq struct {
	Email      string `json:"email" validate:"required,email,max=200"`
	Name       string `json:"name" validate:"max=100"`
	Password   string `json:"password" validate:"required,max=200"`
	Role       string `json:"role" validate:"required"`
	TelegramID *int64 `json:"telegramId"`
}

func (d *Deps) adminCreate(w http.ResponseWriter, r *http.Request) error {
	var in adminCreateReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	a, err := d.Admin.Create(r.Context(), admin.CreateInput{Email: in.Email, Name: in.Name, Password: in.Password, Role: in.Role, TelegramID: in.TelegramID})
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "admin.create", "admin", itoa(a.ID), nil, a)
	httpx.JSON(w, 201, a)
	return nil
}

type adminUpdateReq struct {
	Name       *string `json:"name" validate:"omitempty,max=100"`
	Role       *string `json:"role"`
	IsActive   *bool   `json:"isActive"`
	Password   *string `json:"password" validate:"omitempty,max=200"`
	TelegramID *int64  `json:"telegramId"`
}

func (d *Deps) adminUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in adminUpdateReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	before, err := d.Admin.Get(r.Context(), id)
	if err != nil {
		return err
	}
	a, err := d.Admin.Update(r.Context(), id, admin.UpdateInput{Name: in.Name, Role: in.Role, IsActive: in.IsActive, Password: in.Password, TelegramID: in.TelegramID})
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "admin.update", "admin", itoa(id), before, a)
	httpx.JSON(w, 200, a)
	return nil
}

func (d *Deps) adminAudit(w http.ResponseWriter, r *http.Request) error {
	who := httpx.AdminFrom(r.Context())
	only := int64(0)
	switch {
	case admin.Can(who.Role, "audit.read"):
	case admin.Can(who.Role, "audit.read.own"):
		only = who.ID
	default:
		return apperr.Forbidden
	}
	rows, err := d.Admin.AuditList(r.Context(), only, r.URL.Query().Get("action"), queryInt(r, "before", 0), int(queryInt(r, "limit", 50)))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"audit": rows})
	return nil
}

// ---- players (admin view)

type adminPlayerView struct {
	ID               int64     `json:"id"`
	TelegramID       string    `json:"telegramId"`
	Username         string    `json:"username"`
	FirstName        string    `json:"firstName"`
	LastName         string    `json:"lastName"`
	Language         string    `json:"language"`
	IsPremium        bool      `json:"isPremium"`
	Coins            int64     `json:"coins"`
	Stars            int64     `json:"stars"`
	TotalCoinsEarned int64     `json:"totalCoinsEarned"`
	Level            int       `json:"level"`
	MiningLevel      int       `json:"miningLevel"`
	CapacityLevel    int       `json:"capacityLevel"`
	IsBlocked        bool      `json:"isBlocked"`
	BlockReason      string    `json:"blockReason"`
	HoldWithdrawals  bool      `json:"holdWithdrawals"`
	HoldRewards      bool      `json:"holdRewards"`
	RiskScore        int       `json:"riskScore"`
	Flags            any       `json:"flags"`
	AdminNote        string    `json:"adminNote"`
	ReferredBy       *int64    `json:"referredBy"`
	ReferralCounted  bool      `json:"referralCounted"`
	InviteBonusCoins *int64    `json:"inviteBonusCoins"`
	FriendBonusCoins int64     `json:"friendBonusCoins"`
	Country          string    `json:"country"`
	LastIP           string    `json:"lastIp"`
	BotStarted       bool      `json:"botStarted"`
	BotBlocked       bool      `json:"botBlocked"`
	CreatedAt        time.Time `json:"createdAt"`
	LastSeenAt       time.Time `json:"lastSeenAt"`
}

const adminPlayerCols = `id, telegram_id::text, username, first_name, last_name, language, is_premium, coins, stars, total_coins_earned,
	level, mining_level, capacity_level, is_blocked, block_reason, hold_withdrawals, hold_rewards, risk_score, flags, admin_note,
	referred_by, referral_counted, invite_bonus_coins, friend_bonus_coins, country, last_ip, bot_started, bot_blocked, created_at, last_seen_at`

func scanAdminPlayer(row pgx.Row) (*adminPlayerView, error) {
	var v adminPlayerView
	err := row.Scan(&v.ID, &v.TelegramID, &v.Username, &v.FirstName, &v.LastName, &v.Language, &v.IsPremium, &v.Coins, &v.Stars,
		&v.TotalCoinsEarned, &v.Level, &v.MiningLevel, &v.CapacityLevel, &v.IsBlocked, &v.BlockReason, &v.HoldWithdrawals,
		&v.HoldRewards, &v.RiskScore, &v.Flags, &v.AdminNote, &v.ReferredBy, &v.ReferralCounted, &v.InviteBonusCoins,
		&v.FriendBonusCoins, &v.Country, &v.LastIP, &v.BotStarted, &v.BotBlocked, &v.CreatedAt, &v.LastSeenAt)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, apperr.NotFound
		}
		return nil, err
	}
	return &v, nil
}

func (d *Deps) adminPlayers(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := int(queryInt(r, "limit", 30))
	if limit < 1 || limit > 100 {
		limit = 30
	}
	offset := int(queryInt(r, "offset", 0))
	if offset < 0 {
		offset = 0
	}
	var args []any
	where := "deleted_at IS NULL"
	if q != "" {
		args = append(args, "%"+strings.TrimPrefix(q, "@")+"%", q)
		where += " AND (lower(username) LIKE lower($1) OR lower(first_name) LIKE lower($1) OR telegram_id::text = $2 OR id::text = $2)"
	}
	if v := r.URL.Query().Get("blocked"); v == "1" {
		where += " AND is_blocked"
	}
	if v := r.URL.Query().Get("flagged"); v == "1" {
		where += " AND (hold_withdrawals OR hold_rewards OR risk_score > 0)"
	}
	var total int64
	if err := d.Pool.QueryRow(r.Context(), `SELECT count(*) FROM players WHERE `+where, args...).Scan(&total); err != nil {
		return err
	}
	n := len(args)
	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := d.Pool.Query(r.Context(), `SELECT `+adminPlayerCols+` FROM players WHERE `+where+
		` ORDER BY id DESC LIMIT $`+strconv.Itoa(n+1)+` OFFSET $`+strconv.Itoa(n+2), listArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []*adminPlayerView{}
	for rows.Next() {
		v, err := scanAdminPlayer(rows)
		if err != nil {
			return err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"players": out, "total": total})
	return nil
}

func (d *Deps) adminPlayer(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	v, err := scanAdminPlayer(d.Pool.QueryRow(r.Context(), `SELECT `+adminPlayerCols+` FROM players WHERE id=$1`, id))
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, v)
	return nil
}

func (d *Deps) adminPlayerLedger(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	before := queryInt(r, "before", 0)
	limit := int(queryInt(r, "limit", 50))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := d.Pool.Query(r.Context(), `SELECT id, currency, amount, balance_after, reason, ref_type, ref_id, meta, created_at
		FROM ledger_entries WHERE player_id=$1 AND ($2=0 OR id<$2) ORDER BY id DESC LIMIT $3`, id, before, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		ID           int64     `json:"id"`
		Currency     string    `json:"currency"`
		Amount       int64     `json:"amount"`
		BalanceAfter int64     `json:"balanceAfter"`
		Reason       string    `json:"reason"`
		RefType      string    `json:"refType"`
		RefID        string    `json:"refId"`
		Meta         any       `json:"meta"`
		CreatedAt    time.Time `json:"createdAt"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.Currency, &x.Amount, &x.BalanceAfter, &x.Reason, &x.RefType, &x.RefID, &x.Meta, &x.CreatedAt); err != nil {
			return err
		}
		out = append(out, x)
	}
	httpx.JSON(w, 200, map[string]any{"entries": out})
	return rows.Err()
}

type blockReq struct {
	Reason string `json:"reason" validate:"max=300"`
}

func (d *Deps) adminBlock(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in blockReq
	if err := httpx.DecodeOptional(r, &in); err != nil {
		return err
	}
	if err := d.Players.SetBlocked(r.Context(), id, true, in.Reason); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "player.block", "player", itoa(id), nil, map[string]any{"reason": in.Reason})
	d.onBlockChanged(r, id, true)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminUnblock(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := d.Players.SetBlocked(r.Context(), id, false, ""); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "player.unblock", "player", itoa(id), nil, nil)
	d.onBlockChanged(r, id, false)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

type flagsReq struct {
	HoldWithdrawals *bool   `json:"holdWithdrawals"`
	HoldRewards     *bool   `json:"holdRewards"`
	Note            *string `json:"note" validate:"omitempty,max=1000"`
}

func (d *Deps) adminFlags(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in flagsReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	// holds are a finance/super decision; support may only write the note
	if (in.HoldWithdrawals != nil || in.HoldRewards != nil) && !admin.Can(who.Role, "players.block") {
		return apperr.Forbidden
	}
	err = db.WithTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		if in.HoldWithdrawals != nil {
			if _, err := tx.Exec(r.Context(), `UPDATE players SET hold_withdrawals=$2 WHERE id=$1`, id, *in.HoldWithdrawals); err != nil {
				return err
			}
		}
		if in.HoldRewards != nil {
			if _, err := tx.Exec(r.Context(), `UPDATE players SET hold_rewards=$2 WHERE id=$1`, id, *in.HoldRewards); err != nil {
				return err
			}
		}
		if in.Note != nil {
			if _, err := tx.Exec(r.Context(), `UPDATE players SET admin_note=$2 WHERE id=$1`, id, *in.Note); err != nil {
				return err
			}
		}
		return admin.Audit(r.Context(), tx, who, "player.flags", "player", itoa(id), nil, in)
	})
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

type inviteBonusReq struct {
	Coins *int64 `json:"coins" validate:"omitempty,gte=0"` // null = back to the default
}

func (d *Deps) adminInviteBonus(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in inviteBonusReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	_, err = d.Pool.Exec(r.Context(), `UPDATE players SET invite_bonus_coins=$2 WHERE id=$1`, id, in.Coins)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "player.invite_bonus", "player", itoa(id), nil, in)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

var _ = ledger.Coin
