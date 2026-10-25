package httpx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/players"
	"brunoplay/internal/ratelimit"
	"brunoplay/internal/settings"
	"brunoplay/internal/token"
)

// AdminIdentity is the signed-in admin attached to admin requests.
type AdminIdentity struct {
	ID    int64
	Email string
	Role  string
}

func WithPlayer(ctx context.Context, p *players.Player) context.Context {
	ctx = context.WithValue(ctx, keyPlayer, p)
	return context.WithValue(ctx, keyLang, p.Lang())
}
func PlayerFrom(ctx context.Context) *players.Player {
	p, _ := ctx.Value(keyPlayer).(*players.Player)
	return p
}
func WithAdmin(ctx context.Context, a *AdminIdentity) context.Context {
	return context.WithValue(ctx, keyAdmin, a)
}
func AdminFrom(ctx context.Context) *AdminIdentity {
	a, _ := ctx.Value(keyAdmin).(*AdminIdentity)
	return a
}

// MW bundles the dependencies of the middleware.
type MW struct {
	Settings     *settings.Service
	Players      *players.Store
	PlayerTokens *token.Signer
	Pool         *pgxpool.Pool
	IPLimiter    ratelimit.Limiter
	PlayerLimit  ratelimit.Limiter
	TrustProxy   bool
	CORSOrigins  []string
	Now          func() time.Time
}

func (m *MW) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Base is the chain every request goes through: id, panic safety, security headers, logging, CORS, IP rate limit.
func (m *MW) Base(next http.Handler) http.Handler {
	return m.requestID(m.recoverer(m.securityHeaders(m.cors(m.accessLog(m.ipLimit(next))))))
}

func (m *MW) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), keyRequestID, id)
		ctx = context.WithValue(ctx, keyIP, ClientIP(r, m.TrustProxy))
		// language from Accept-Language until the player is known
		if al := r.Header.Get("Accept-Language"); al != "" {
			ctx = context.WithValue(ctx, keyLang, apperr.ParseLang(al))
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *MW) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "panic", rec, "path", r.URL.Path, "request_id", RequestID(r.Context()))
				Fail(w, r, apperr.Internal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (m *MW) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (m *MW) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range m.CORSOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && (allowed[o] || allowed["*"]) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-App-Version, Accept-Language, X-Request-ID")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (m *MW) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return
		}
		lvl := slog.LevelInfo
		if sw.status >= 500 {
			lvl = slog.LevelError
		}
		attrs := []any{"method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds(),
			"bytes", sw.bytes, "request_id", RequestID(r.Context())}
		if p := PlayerFrom(r.Context()); p != nil {
			attrs = append(attrs, "player", p.ID)
		}
		slog.Log(r.Context(), lvl, "http", attrs...)
	})
}

func (m *MW) ipLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.IPLimiter != nil && !m.IPLimiter.Allow(IP(r.Context())) {
			w.Header().Set("Retry-After", "1")
			Fail(w, r, apperr.TooManyReq)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Gates (maintenance and minimum version) apply to player endpoints only.
func (m *MW) Gates(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if m.Settings.BoolOr(ctx, settings.KeyMaintenanceOn, false) {
			lang := Lang(ctx)
			text := m.Settings.StrOr(ctx, settings.KeyMaintenanceEN, "")
			if lang == apperr.RU {
				if ru := m.Settings.StrOr(ctx, settings.KeyMaintenanceRU, ""); ru != "" {
					text = ru
				}
			}
			e := apperr.Maintenance.With("until", m.Settings.StrOr(ctx, settings.KeyMaintenanceEnd, "")).With("text", text)
			Fail(w, r, e)
			return
		}
		if min := m.Settings.StrOr(ctx, settings.KeyMinVersion, ""); min != "" {
			if v := r.Header.Get("X-App-Version"); v != "" && versionLess(v, min) {
				Fail(w, r, apperr.UpgradeNeeded.With("minVersion", min))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// versionLess compares dotted numeric versions ("1.2.10" < "1.3").
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// PlayerAuth requires a valid player token. Blocked players pass only where allowBlocked is set (so the app can show the screen).
func (m *MW) PlayerAuth(allowBlocked bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			raw := strings.TrimPrefix(h, "Bearer ")
			if h == "" || raw == h {
				Fail(w, r, apperr.Unauthorized)
				return
			}
			sub, err := m.PlayerTokens.Verify(raw, m.now())
			if err != nil {
				if err == token.ErrExpired {
					Fail(w, r, apperr.AuthExpired)
				} else {
					Fail(w, r, apperr.Unauthorized)
				}
				return
			}
			id, err := strconv.ParseInt(sub, 10, 64)
			if err != nil {
				Fail(w, r, apperr.Unauthorized)
				return
			}
			p, err := m.Players.Get(r.Context(), m.Pool, id)
			if err != nil {
				if apperr.Is(err, apperr.NotFound) {
					Fail(w, r, apperr.Unauthorized)
				} else {
					Fail(w, r, err)
				}
				return
			}
			ctx := WithPlayer(r.Context(), p)
			if p.IsBlocked && !allowBlocked {
				Fail(w, r.WithContext(ctx), apperr.AccountBlocked)
				return
			}
			if m.PlayerLimit != nil && !m.PlayerLimit.Allow("p:"+sub) {
				w.Header().Set("Retry-After", "1")
				Fail(w, r.WithContext(ctx), apperr.TooManyReq)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Limit applies an extra per-player limit to sensitive routes (buy, withdraw, ...).
func (m *MW) Limit(l ratelimit.Limiter, name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := name + ":" + IP(r.Context())
			if p := PlayerFrom(r.Context()); p != nil {
				key = name + ":p" + strconv.FormatInt(p.ID, 10)
			}
			if !l.Allow(key) {
				w.Header().Set("Retry-After", "1")
				Fail(w, r, apperr.TooManyReq)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(c int) { r.status = c; r.ResponseWriter.WriteHeader(c) }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

// Idempotency makes a money-moving POST safe to repeat: the same Idempotency-Key replays the stored answer.
func (m *MW) Idempotency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		p := PlayerFrom(r.Context())
		if key == "" || p == nil || r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		if len(key) > 100 {
			Fail(w, r, apperr.BadRequest)
			return
		}
		ctx := r.Context()
		ct, err := m.Pool.Exec(ctx, `INSERT INTO idempotency_keys (player_id, key, endpoint) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
			p.ID, key, r.Method+" "+r.URL.Path)
		if err != nil {
			Fail(w, r, err)
			return
		}
		if ct.RowsAffected() == 0 {
			var status int
			var body []byte
			var endpoint string
			if err := m.Pool.QueryRow(ctx, `SELECT status, response, endpoint FROM idempotency_keys WHERE player_id=$1 AND key=$2`, p.ID, key).
				Scan(&status, &body, &endpoint); err != nil {
				Fail(w, r, err)
				return
			}
			if endpoint != r.Method+" "+r.URL.Path {
				Fail(w, r, apperr.BadRequest)
				return
			}
			if status == 0 {
				Fail(w, r, apperr.IdemInProgress)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Idempotent-Replay", "true")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status >= 500 || rec.status == 0 {
			// a failed attempt may be retried with the same key
			_, _ = m.Pool.Exec(context.WithoutCancel(ctx), `DELETE FROM idempotency_keys WHERE player_id=$1 AND key=$2`, p.ID, key)
			return
		}
		_, _ = m.Pool.Exec(context.WithoutCancel(ctx), `UPDATE idempotency_keys SET status=$3, response=$4 WHERE player_id=$1 AND key=$2`,
			p.ID, key, rec.status, rec.buf.Bytes())
	})
}

// PurgeIdempotency removes keys older than the given age (called by the worker).
func PurgeIdempotency(ctx context.Context, pool *pgxpool.Pool, olderThan time.Duration) error {
	_, err := pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	return err
}
