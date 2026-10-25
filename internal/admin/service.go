package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/httpx"
	"brunoplay/internal/token"
)

const (
	AccessTTL      = 15 * time.Minute
	RefreshTTL     = 14 * 24 * time.Hour
	maxFailedLogin = 5
)

type Admin struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	IsActive    bool       `json:"isActive"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
	TelegramID  *int64     `json:"telegramId"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type Service struct {
	pool   *pgxpool.Pool
	tokens *token.Signer
	now    func() time.Time
	// OnNewLogin is called (in the background) when an admin signs in from a new IP (the bot sends an alert).
	OnNewLogin func(a Admin, ip string)
	dummyHash  string
}

func NewService(pool *pgxpool.Pool, tokens *token.Signer) *Service {
	h, _ := HashPassword("dummy-password-for-timing-equalisation")
	return &Service{pool: pool, tokens: tokens, now: time.Now, dummyHash: h}
}

func (s *Service) SetClock(f func() time.Time) { s.now = f }

type Session struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Admin        Admin     `json:"admin"`
}

var errBadLogin = apperr.AuthFailed

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func newRefresh() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

const adminCols = `id, email, name, role, is_active, last_login_at, telegram_id, created_at`

func scanAdmin(row interface{ Scan(...any) error }) (*Admin, error) {
	var a Admin
	if err := row.Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.IsActive, &a.LastLoginAt, &a.TelegramID, &a.CreatedAt); err != nil {
		if db.IsNoRows(err) {
			return nil, apperr.NotFound
		}
		return nil, err
	}
	return &a, nil
}

// Login checks the password with lockout after repeated failures and opens a session.
func (s *Service) Login(ctx context.Context, email, password, ip, ua string) (*Session, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var id int64
	var hash string
	var active bool
	var failed int
	var locked *time.Time
	var lastIP string
	err := s.pool.QueryRow(ctx, `SELECT id, password_hash, is_active, failed_logins, locked_until, last_login_ip FROM admins WHERE email=$1`, email).
		Scan(&id, &hash, &active, &failed, &locked, &lastIP)
	if err != nil {
		if db.IsNoRows(err) {
			VerifyPassword(password, s.dummyHash) // same time as a real check
			return nil, errBadLogin
		}
		return nil, err
	}
	now := s.now()
	if locked != nil && locked.After(now) {
		VerifyPassword(password, s.dummyHash)
		return nil, errBadLogin
	}
	if !VerifyPassword(password, hash) || !active {
		failed++
		var until any
		if failed >= maxFailedLogin {
			// 15 minutes, doubling for every further block, capped at 12 hours
			d := 15 * time.Minute << uint(min(failed-maxFailedLogin, 5))
			if d > 12*time.Hour {
				d = 12 * time.Hour
			}
			until = now.Add(d)
		}
		_, _ = s.pool.Exec(ctx, `UPDATE admins SET failed_logins=$2, locked_until=$3 WHERE id=$1`, id, failed, until)
		return nil, errBadLogin
	}

	refresh, err := newRefresh()
	if err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO admin_sessions (admin_id, token_hash, ip, user_agent, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		id, hashToken(refresh), ip, truncate(ua, 200), now.Add(RefreshTTL))
	if err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx, `UPDATE admins SET failed_logins=0, locked_until=NULL, last_login_at=$2, last_login_ip=$3 WHERE id=$1`, id, now, ip)
	if err != nil {
		return nil, err
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if lastIP != ip && s.OnNewLogin != nil {
		go s.OnNewLogin(*a, ip)
	}
	return s.session(a, refresh, now)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Service) session(a *Admin, refresh string, now time.Time) (*Session, error) {
	tok, exp, err := s.tokens.Issue(itoa(a.ID), AccessTTL, now)
	if err != nil {
		return nil, err
	}
	return &Session{AccessToken: tok, RefreshToken: refresh, ExpiresAt: exp, Admin: *a}, nil
}

// Refresh rotates the refresh token (a used or revoked token cannot be used again).
func (s *Service) Refresh(ctx context.Context, refresh string) (*Session, error) {
	now := s.now()
	var adminID, sessID int64
	var expires time.Time
	var revoked *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, admin_id, expires_at, revoked_at FROM admin_sessions WHERE token_hash=$1`, hashToken(refresh)).
		Scan(&sessID, &adminID, &expires, &revoked)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, apperr.AuthExpired
		}
		return nil, err
	}
	if revoked != nil || expires.Before(now) {
		return nil, apperr.AuthExpired
	}
	a, err := s.Get(ctx, adminID)
	if err != nil || !a.IsActive {
		return nil, apperr.AuthExpired
	}
	next, err := newRefresh()
	if err != nil {
		return nil, err
	}
	err = db.WithTx(ctx, s.pool, func(tx pgxTx) error {
		ct, err := tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL`, sessID, now)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return apperr.AuthExpired
		}
		_, err = tx.Exec(ctx, `INSERT INTO admin_sessions (admin_id, token_hash, ip, expires_at)
			SELECT admin_id, $2, ip, $3 FROM admin_sessions WHERE id=$1`, sessID, hashToken(next), now.Add(RefreshTTL))
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.session(a, next, now)
}

func (s *Service) Logout(ctx context.Context, refresh string) error {
	_, err := s.pool.Exec(ctx, `UPDATE admin_sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, hashToken(refresh))
	return err
}

// RevokeAll signs an admin out everywhere (used when the password changes or the admin is deactivated).
func (s *Service) RevokeAll(ctx context.Context, adminID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE admin_sessions SET revoked_at=now() WHERE admin_id=$1 AND revoked_at IS NULL`, adminID)
	return err
}

func (s *Service) Get(ctx context.Context, id int64) (*Admin, error) {
	return scanAdmin(s.pool.QueryRow(ctx, `SELECT `+adminCols+` FROM admins WHERE id=$1`, id))
}

func (s *Service) List(ctx context.Context) ([]Admin, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+adminCols+` FROM admins ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Admin
	for rows.Next() {
		a, err := scanAdmin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

type CreateInput struct {
	Email      string
	Name       string
	Password   string
	Role       string
	TelegramID *int64
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Admin, error) {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !strings.Contains(in.Email, "@") || len(in.Email) > 200 {
		return nil, apperr.Validation.With("field", "email")
	}
	if !ValidRole(in.Role) {
		return nil, apperr.Validation.With("field", "role")
	}
	if err := CheckPassword(in.Password); err != nil {
		return nil, apperr.Validation.With("field", "password").Wrap(err)
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.pool.QueryRow(ctx, `INSERT INTO admins (email, name, password_hash, role, telegram_id) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		in.Email, in.Name, hash, in.Role, in.TelegramID).Scan(&id)
	if err != nil {
		if db.IsUnique(err) {
			return nil, apperr.Validation.With("field", "email").Wrap(errors.New("email already used"))
		}
		return nil, err
	}
	return s.Get(ctx, id)
}

type UpdateInput struct {
	Name       *string
	Role       *string
	IsActive   *bool
	Password   *string
	TelegramID *int64
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (*Admin, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Role != nil {
		if !ValidRole(*in.Role) {
			return nil, apperr.Validation.With("field", "role")
		}
	}
	// never leave the system without an active super admin
	demote := (in.Role != nil && *in.Role != RoleSuper) || (in.IsActive != nil && !*in.IsActive)
	if cur.Role == RoleSuper && cur.IsActive && demote {
		var others int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM admins WHERE role='super' AND is_active AND id<>$1`, id).Scan(&others); err != nil {
			return nil, err
		}
		if others == 0 {
			return nil, apperr.Validation.With("field", "role").Wrap(errors.New("cannot remove the last super admin"))
		}
	}
	if in.Name != nil {
		if _, err := s.pool.Exec(ctx, `UPDATE admins SET name=$2 WHERE id=$1`, id, *in.Name); err != nil {
			return nil, err
		}
	}
	if in.Role != nil {
		if _, err := s.pool.Exec(ctx, `UPDATE admins SET role=$2 WHERE id=$1`, id, *in.Role); err != nil {
			return nil, err
		}
	}
	if in.IsActive != nil {
		if _, err := s.pool.Exec(ctx, `UPDATE admins SET is_active=$2 WHERE id=$1`, id, *in.IsActive); err != nil {
			return nil, err
		}
		if !*in.IsActive {
			_ = s.RevokeAll(ctx, id)
		}
	}
	if in.TelegramID != nil {
		if _, err := s.pool.Exec(ctx, `UPDATE admins SET telegram_id=NULLIF($2,0) WHERE id=$1`, id, *in.TelegramID); err != nil {
			return nil, err
		}
	}
	if in.Password != nil {
		if err := CheckPassword(*in.Password); err != nil {
			return nil, apperr.Validation.With("field", "password").Wrap(err)
		}
		h, err := HashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE admins SET password_hash=$2, failed_logins=0, locked_until=NULL WHERE id=$1`, id, h); err != nil {
			return nil, err
		}
		_ = s.RevokeAll(ctx, id)
	}
	return s.Get(ctx, id)
}

// ChangeOwnPassword requires the current password.
func (s *Service) ChangeOwnPassword(ctx context.Context, id int64, current, next string) error {
	var hash string
	if err := s.pool.QueryRow(ctx, `SELECT password_hash FROM admins WHERE id=$1`, id).Scan(&hash); err != nil {
		return err
	}
	if !VerifyPassword(current, hash) {
		return apperr.AuthFailed
	}
	_, err := s.Update(ctx, id, UpdateInput{Password: &next})
	return err
}

// Verify checks an access token and returns the admin identity.
func (s *Service) Verify(ctx context.Context, raw string) (*httpx.AdminIdentity, error) {
	sub, err := s.tokens.Verify(raw, s.now())
	if err != nil {
		if errors.Is(err, token.ErrExpired) {
			return nil, apperr.AuthExpired
		}
		return nil, apperr.Unauthorized
	}
	id, err := parseID(sub)
	if err != nil {
		return nil, apperr.Unauthorized
	}
	a, err := s.Get(ctx, id)
	if err != nil || !a.IsActive {
		return nil, apperr.Unauthorized
	}
	return &httpx.AdminIdentity{ID: a.ID, Email: a.Email, Role: a.Role}, nil
}

// ---- audit

// Audit appends an audit row. q can be the pool or a transaction so the row is atomic with the change.
func Audit(ctx context.Context, q db.Querier, who *httpx.AdminIdentity, action, targetType, targetID string, before, after any) error {
	var id any
	email := ""
	if who != nil {
		id, email = who.ID, who.Email
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if before == nil {
		b = nil
	}
	if after == nil {
		a = nil
	}
	_, err := q.Exec(ctx, `INSERT INTO audit_log (admin_id, admin_email, action, target_type, target_id, before, after, ip)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id, email, action, targetType, targetID, b, a, httpx.IP(ctx))
	return err
}

type AuditRow struct {
	ID         int64           `json:"id"`
	AdminID    *int64          `json:"adminId"`
	AdminEmail string          `json:"adminEmail"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	IP         string          `json:"ip"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// AuditList returns audit rows newest first; onlyAdmin > 0 limits to one admin.
func (s *Service) AuditList(ctx context.Context, onlyAdmin int64, action string, beforeID int64, limit int) ([]AuditRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, admin_id, admin_email, action, target_type, target_id, before, after, ip, created_at
		FROM audit_log WHERE ($1=0 OR admin_id=$1) AND ($2='' OR action=$2) AND ($3=0 OR id<$3)
		ORDER BY id DESC LIMIT $4`, onlyAdmin, action, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		var b, a []byte
		if err := rows.Scan(&r.ID, &r.AdminID, &r.AdminEmail, &r.Action, &r.TargetType, &r.TargetID, &b, &a, &r.IP, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Before, r.After = b, a
		out = append(out, r)
	}
	return out, rows.Err()
}
