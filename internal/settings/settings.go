// Package settings is the editable configuration store (every {CONFIG} value of the ToR).
// Values live in PostgreSQL with history; reads come from a short-lived in-memory snapshot.
// A key without a value and without a safe default is "unconfigured": the feature that needs it answers CONFIG_MISSING.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
)

type Kind string

const (
	KInt    Kind = "int"
	KFloat  Kind = "float"
	KBool   Kind = "bool"
	KString Kind = "string"
	KJSON   Kind = "json"
)

// Def describes one setting.
type Def struct {
	Key     string
	Kind    Kind
	Group   string
	Desc    string
	Default any // nil = no default: the setting must be configured by the owner
	Min     *float64
	Max     *float64
	Enum    []string
	Check   func(raw json.RawMessage) error // extra validation for KJSON
}

func num(v float64) *float64 { return &v }

type snapshot struct {
	values map[string]json.RawMessage
	at     time.Time
}

type Service struct {
	pool *pgxpool.Pool
	ttl  time.Duration
	mu   sync.Mutex
	snap *snapshot
	defs map[string]Def
}

func New(pool *pgxpool.Pool) *Service {
	s := &Service{pool: pool, ttl: 2 * time.Second, defs: map[string]Def{}}
	for _, d := range Registry() {
		s.defs[d.Key] = d
	}
	return s
}

// Def returns the definition of a key.
func (s *Service) Def(key string) (Def, bool) { d, ok := s.defs[key]; return d, ok }

// SetTTL changes how long a snapshot is trusted (tests use 0 for immediate reads).
func (s *Service) SetTTL(d time.Duration) { s.ttl = d; s.invalidate() }

func (s *Service) invalidate() {
	s.mu.Lock()
	s.snap = nil
	s.mu.Unlock()
}

func (s *Service) load(ctx context.Context) (*snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snap != nil && time.Since(s.snap.at) < s.ttl {
		return s.snap, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = json.RawMessage(v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.snap = &snapshot{values: m, at: time.Now()}
	return s.snap, nil
}

// Raw returns the stored JSON of a key, or its default, or CONFIG_MISSING.
func (s *Service) Raw(ctx context.Context, key string) (json.RawMessage, error) {
	d, ok := s.defs[key]
	if !ok {
		return nil, fmt.Errorf("settings: unknown key %q", key)
	}
	snap, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if v, ok := snap.values[key]; ok {
		return v, nil
	}
	if d.Default == nil {
		return nil, apperr.ConfigMissing.With("key", key)
	}
	b, _ := json.Marshal(d.Default)
	return b, nil
}

// Get decodes a setting into T.
func Get[T any](ctx context.Context, s *Service, key string) (T, error) {
	var out T
	raw, err := s.Raw(ctx, key)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("settings: key %s has an invalid stored value: %w", key, err)
	}
	return out, nil
}

// Int, Bool, Str are the common typed reads.
func (s *Service) Int(ctx context.Context, key string) (int64, error) {
	f, err := Get[float64](ctx, s, key)
	return int64(f), err
}
func (s *Service) Float(ctx context.Context, key string) (float64, error) {
	return Get[float64](ctx, s, key)
}
func (s *Service) Bool(ctx context.Context, key string) (bool, error) {
	return Get[bool](ctx, s, key)
}
func (s *Service) Str(ctx context.Context, key string) (string, error) {
	return Get[string](ctx, s, key)
}

// MustBool/Int return the fallback when the value is unreadable (used by non-critical gates such as maintenance).
func (s *Service) BoolOr(ctx context.Context, key string, fallback bool) bool {
	v, err := s.Bool(ctx, key)
	if err != nil {
		return fallback
	}
	return v
}
func (s *Service) StrOr(ctx context.Context, key, fallback string) string {
	v, err := s.Str(ctx, key)
	if err != nil {
		return fallback
	}
	return v
}

// Validate checks a value against the definition of a key and returns its canonical JSON.
func (s *Service) Validate(key string, value any) (json.RawMessage, error) {
	d, ok := s.defs[key]
	if !ok {
		return nil, apperr.NotFound.With("key", key)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, apperr.Validation.Wrap(err)
	}
	bad := func(why string) error {
		return apperr.Validation.With("field", key).Wrap(fmt.Errorf("%s: %s", key, why))
	}
	switch d.Kind {
	case KInt, KFloat:
		var f float64
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, bad("must be a number")
		}
		if d.Kind == KInt && f != float64(int64(f)) {
			return nil, bad("must be a whole number")
		}
		if d.Min != nil && f < *d.Min {
			return nil, bad(fmt.Sprintf("must be >= %v", *d.Min))
		}
		if d.Max != nil && f > *d.Max {
			return nil, bad(fmt.Sprintf("must be <= %v", *d.Max))
		}
	case KBool:
		var v bool
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, bad("must be true or false")
		}
	case KString:
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, bad("must be text")
		}
		if len(d.Enum) > 0 {
			okEnum := false
			for _, e := range d.Enum {
				if e == v {
					okEnum = true
				}
			}
			if !okEnum {
				return nil, bad("not an allowed value")
			}
		}
		if len(v) > 2000 {
			return nil, bad("too long")
		}
	case KJSON:
		if !json.Valid(b) {
			return nil, bad("invalid JSON")
		}
		if d.Check != nil {
			if err := d.Check(b); err != nil {
				return nil, bad(err.Error())
			}
		}
	}
	return b, nil
}

// Set stores a value, appends the history row and refreshes the snapshot.
func (s *Service) Set(ctx context.Context, key string, value any, adminID int64) error {
	raw, err := s.Validate(key, value)
	if err != nil {
		return err
	}
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var old []byte
		err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1 FOR UPDATE`, key).Scan(&old)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		var admin any
		if adminID > 0 {
			admin = adminID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO settings (key, value, updated_at, updated_by) VALUES ($1,$2,now(),$3)
			ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now(), updated_by=EXCLUDED.updated_by`,
			key, []byte(raw), admin); err != nil {
			return err
		}
		var oldAny any
		if old != nil {
			oldAny = old
		}
		_, err = tx.Exec(ctx, `INSERT INTO settings_history (key, old_value, new_value, changed_by) VALUES ($1,$2,$3,$4)`,
			key, oldAny, []byte(raw), admin)
		return err
	})
	if err != nil {
		return err
	}
	s.invalidate()
	return nil
}

// View is what the admin API shows for one setting.
type View struct {
	Key        string          `json:"key"`
	Group      string          `json:"group"`
	Kind       Kind            `json:"kind"`
	Desc       string          `json:"desc"`
	Value      json.RawMessage `json:"value"`
	Default    any             `json:"default"`
	Configured bool            `json:"configured"` // a value is stored
	Missing    bool            `json:"missing"`    // no value and no default: the owner must supply it
	Enum       []string        `json:"enum,omitempty"`
	UpdatedAt  *time.Time      `json:"updatedAt,omitempty"`
}

func (s *Service) All(ctx context.Context) ([]View, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value, updated_at FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type stored struct {
		v  []byte
		at time.Time
	}
	st := map[string]stored{}
	for rows.Next() {
		var k string
		var v []byte
		var at time.Time
		if err := rows.Scan(&k, &v, &at); err != nil {
			return nil, err
		}
		st[k] = stored{v, at}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]View, 0, len(s.defs))
	for _, d := range s.defs {
		v := View{Key: d.Key, Group: d.Group, Kind: d.Kind, Desc: d.Desc, Default: d.Default, Enum: d.Enum}
		if x, ok := st[d.Key]; ok {
			v.Value, v.Configured = json.RawMessage(x.v), true
			t := x.at
			v.UpdatedAt = &t
		} else if d.Default != nil {
			v.Value, _ = json.Marshal(d.Default)
		} else {
			v.Missing = true
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// MissingKeys lists the settings that still need a value from the owner (for the admin "what is empty" view).
func (s *Service) MissingKeys(ctx context.Context) ([]string, error) {
	all, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, v := range all {
		if v.Missing {
			out = append(out, v.Key)
		}
	}
	return out, nil
}

type HistoryRow struct {
	ID        int64           `json:"id"`
	Key       string          `json:"key"`
	Old       json.RawMessage `json:"old"`
	New       json.RawMessage `json:"new"`
	ChangedBy *int64          `json:"changedBy"`
	ChangedAt time.Time       `json:"changedAt"`
}

func (s *Service) History(ctx context.Context, key string, limit int) ([]HistoryRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, key, old_value, new_value, changed_by, changed_at
		FROM settings_history WHERE ($1 = '' OR key=$1) ORDER BY id DESC LIMIT $2`, key, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var h HistoryRow
		var o, n []byte
		if err := rows.Scan(&h.ID, &h.Key, &o, &n, &h.ChangedBy, &h.ChangedAt); err != nil {
			return nil, err
		}
		h.Old, h.New = o, n
		out = append(out, h)
	}
	return out, rows.Err()
}
