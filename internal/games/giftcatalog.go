package games

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/settings"
)

// The gift catalog is the `gifts` setting plus a picture per gift (table gift_images). The admin panel edits both.
// Without a picture the app shows the artwork bundled with it.

// GiftImageMax is the largest picture accepted (bytes). A gift is shown small; this is plenty.
const GiftImageMax = 400 << 10

const maxGifts = 100

var giftKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// pictureTypes are the formats accepted. SVG is left out on purpose: it can carry scripts.
var pictureTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true}

func catalogErr(format string, a ...any) error {
	return apperr.GiftCatalog.With("why", fmt.Sprintf(format, a...))
}

// CatalogGift is a gift of the catalog as the admin panel and the app see it. ImageV is 0 without a picture,
// else a version number (changes with every upload) to put in the picture's address so a new picture is not hidden by the cache.
type CatalogGift struct {
	Key       string `json:"key"`
	NameEN    string `json:"name_en"`
	NameRU    string `json:"name_ru"`
	StarValue int64  `json:"star_value"`
	ImageV    int64  `json:"imageV"`
}

// Catalog lists the gifts with the version of their picture.
func (s *Service) Catalog(ctx context.Context) ([]CatalogGift, error) {
	list, _, err := s.Gifts(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT gift_key, extract(epoch FROM updated_at)::bigint FROM gift_images`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ver := map[string]int64{}
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		ver[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]CatalogGift, 0, len(list))
	for _, g := range list {
		out = append(out, CatalogGift{Key: g.Key, NameEN: g.NameEN, NameRU: g.NameRU, StarValue: g.StarValue, ImageV: ver[g.Key]})
	}
	return out, nil
}

// SaveCatalog replaces the list of gifts. A gift that is removed must not be in use: in the wheel odds, in the chest pool,
// or in somebody's collection waiting for a delivery.
func (s *Service) SaveCatalog(ctx context.Context, list []Gift, adminID int64) error {
	if len(list) > maxGifts {
		return catalogErr("At most %d gifts", maxGifts)
	}
	seen := map[string]bool{}
	for i, g := range list {
		n := i + 1
		if !giftKeyRe.MatchString(g.Key) {
			return catalogErr("Gift %d: the key must be 1-32 characters: lowercase letters, digits, - or _ (for example: heart)", n)
		}
		if seen[g.Key] {
			return catalogErr("The key %q is used twice", g.Key)
		}
		seen[g.Key] = true
		if strings.TrimSpace(g.NameEN) == "" {
			return catalogErr("Gift %q needs an English name", g.Key)
		}
		if len(g.NameEN) > 60 || len(g.NameRU) > 60 {
			return catalogErr("Gift %q: a name is longer than 60 characters", g.Key)
		}
		if g.StarValue < 1 || g.StarValue > 100_000_000 {
			return catalogErr("Gift %q: the value in stars must be a whole number from 1 up", g.Key)
		}
	}

	old, _, err := s.Gifts(ctx)
	if err != nil {
		return err
	}
	var odds, chest string
	if b, e := s.settings.Raw(ctx, settings.KeySpinOdds); e == nil {
		odds = string(b)
	}
	if b, e := s.settings.Raw(ctx, settings.KeyChestPool); e == nil {
		chest = string(b)
	}
	for _, g := range old {
		if seen[g.Key] {
			continue
		}
		quoted := `"` + g.Key + `"`
		if strings.Contains(odds, quoted) || strings.Contains(chest, quoted) {
			return catalogErr("The gift %q is still used in the wheel odds or the chest pool: take it out there first", g.Key)
		}
		var open int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM player_gifts WHERE gift_key=$1 AND status IN ('OWNED','CLAIM_REQUESTED')`, g.Key).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return catalogErr("The gift %q is in %d player collections (not sold or delivered yet): it cannot be removed", g.Key, open)
		}
	}

	if err := s.settings.Set(ctx, settings.KeyGifts, list, adminID); err != nil {
		return err
	}
	// pictures of removed gifts go too
	keys := make([]string, 0, len(list))
	for _, g := range list {
		keys = append(keys, g.Key)
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM gift_images WHERE NOT (gift_key = ANY($1))`, keys)
	return err
}

// SaveGiftImage stores the picture of a gift (replaces the old one). The type is taken from the bytes, not from what the sender says.
func (s *Service) SaveGiftImage(ctx context.Context, key string, data []byte) error {
	if len(data) == 0 {
		return catalogErr("The picture is empty")
	}
	if len(data) > GiftImageMax {
		return catalogErr("The picture is too big (%d KB): the limit is %d KB. Make it smaller, for example 512x512.", len(data)>>10, GiftImageMax>>10)
	}
	ct := http.DetectContentType(data)
	if !pictureTypes[ct] {
		return catalogErr("Use a PNG, JPEG, WebP or GIF picture")
	}
	_, m, err := s.Gifts(ctx)
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return apperr.NotFound
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO gift_images (gift_key, content_type, data, updated_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (gift_key) DO UPDATE SET content_type=EXCLUDED.content_type, data=EXCLUDED.data, updated_at=EXCLUDED.updated_at`,
		key, ct, data, s.now())
	return err
}

// DeleteGiftImage takes the picture away: the app goes back to its bundled artwork for that gift.
func (s *Service) DeleteGiftImage(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM gift_images WHERE gift_key=$1`, key)
	return err
}

// GiftImage returns the picture of a gift; apperr.NotFound when it has none.
func (s *Service) GiftImage(ctx context.Context, key string) (contentType string, data []byte, updated time.Time, err error) {
	err = s.pool.QueryRow(ctx, `SELECT content_type, data, updated_at FROM gift_images WHERE gift_key=$1`, key).Scan(&contentType, &data, &updated)
	if db.IsNoRows(err) {
		return "", nil, time.Time{}, apperr.NotFound
	}
	return contentType, data, updated, err
}
