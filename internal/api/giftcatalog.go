package api

import (
	"encoding/base64"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/apperr"
	"brunoplay/internal/games"
	"brunoplay/internal/httpx"
)

// ---- the app: which gifts have their own picture, and the picture itself

type giftArt struct {
	Key    string `json:"key"`
	ImageV int64  `json:"imageV"`
}

// giftArtList tells the app which gifts have an uploaded picture. The pictures are at GET /api/gift-images/{key}?v={imageV}.
func (d *Deps) giftArtList(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Games.Catalog(r.Context())
	if err != nil {
		return err
	}
	out := []giftArt{}
	for _, g := range l {
		if g.ImageV > 0 {
			out = append(out, giftArt{Key: g.Key, ImageV: g.ImageV})
		}
	}
	httpx.JSON(w, 200, map[string]any{"gifts": out})
	return nil
}

// giftImage serves a picture. No sign-in: an <img> tag cannot send one, and the pictures are not secret.
func (d *Deps) giftImage(w http.ResponseWriter, r *http.Request) error {
	ct, data, updated, err := d.Games.GiftImage(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Cache-Control", "public, max-age=86400") // the address carries ?v=, so a new picture has a new address
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Last-Modified", updated.UTC().Format(http.TimeFormat))
	w.WriteHeader(200)
	_, _ = w.Write(data)
	return nil
}

// ---- admin

func (d *Deps) giftCatalogRoutes(r chi.Router) {
	r.With(d.adminAuth("settings.read")).Get("/gifts/catalog", httpx.Wrap(d.adminCatalog))
	r.With(d.adminAuth("settings.write.games")).Put("/gifts/catalog", httpx.Wrap(d.adminCatalogSave))
	r.With(d.adminAuth("settings.write.games")).Put("/gifts/catalog/{key}/image", httpx.Wrap(d.adminGiftImageSet))
	r.With(d.adminAuth("settings.write.games")).Delete("/gifts/catalog/{key}/image", httpx.Wrap(d.adminGiftImageDelete))
}

func (d *Deps) adminCatalog(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Games.Catalog(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"gifts": l, "imageMaxKB": games.GiftImageMax >> 10})
	return nil
}

type catalogReq struct {
	Gifts []games.Gift `json:"gifts"`
}

func (d *Deps) adminCatalogSave(w http.ResponseWriter, r *http.Request) error {
	var in catalogReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	before, _, _ := d.Games.Gifts(r.Context())
	if err := d.Games.SaveCatalog(r.Context(), in.Gifts, who.ID); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "gifts.catalog", "setting", "gifts", before, in.Gifts)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

type giftImageReq struct {
	Data string `json:"data" validate:"required"` // the picture, base64 (a "data:image/png;base64," prefix is fine)
}

func (d *Deps) adminGiftImageSet(w http.ResponseWriter, r *http.Request) error {
	var in giftImageReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	b64 := in.Data
	for i := 0; i < len(b64); i++ {
		if b64[i] == ',' {
			b64 = b64[i+1:]
			break
		}
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return apperr.GiftCatalog.With("why", "The picture could not be read")
	}
	key := chi.URLParam(r, "key")
	if err := d.Games.SaveGiftImage(r.Context(), key, data); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	_ = admin.Audit(r.Context(), d.Pool, who, "gifts.image", "gift", key, nil, map[string]any{"bytes": len(data)})
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminGiftImageDelete(w http.ResponseWriter, r *http.Request) error {
	key := chi.URLParam(r, "key")
	if err := d.Games.DeleteGiftImage(r.Context(), key); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "gifts.image.delete", "gift", key, nil, nil)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}
