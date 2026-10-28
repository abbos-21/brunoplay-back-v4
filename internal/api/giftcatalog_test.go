package api_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"brunoplay/internal/admin"
	"brunoplay/internal/games"
)

func tinyPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestGiftCatalogAndPictures(t *testing.T) {
	e := newEnv(t)
	e.gamesConfig()
	boss := e.adminLogin("boss@example.com", admin.RoleSuper)
	support := e.adminLogin("help@example.com", admin.RoleSupport)
	p := e.login(5201, "en", "")

	// the catalog lists the three gifts, none with a picture yet
	r := e.do("GET", "/api/admin/gifts/catalog", boss, nil)
	if r.Status != 200 || len(r.JSON(t)["gifts"].([]any)) != 3 {
		t.Fatalf("catalog: %d %s", r.Status, r.Body)
	}
	if g := e.do("GET", "/api/gifts/art", p.Token, nil).JSON(t)["gifts"].([]any); len(g) != 0 {
		t.Fatalf("no pictures yet: %v", g)
	}

	// only someone who may change the games settings can upload
	if r := e.do("PUT", "/api/admin/gifts/catalog/small/image", support, map[string]any{"data": tinyPNG(t)}); r.Status != 403 {
		t.Fatalf("support upload: %d %s", r.Status, r.Body)
	}
	// not a picture / too big / unknown gift
	if r := e.do("PUT", "/api/admin/gifts/catalog/small/image", boss, map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("<svg onload=alert(1)>"))}); r.Code(t) != "GIFT_CATALOG_INVALID" {
		t.Fatalf("svg/text: %d %s", r.Status, r.Body)
	}
	big := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, make([]byte, games.GiftImageMax)...)
	if r := e.do("PUT", "/api/admin/gifts/catalog/small/image", boss, map[string]any{"data": base64.StdEncoding.EncodeToString(big)}); r.Code(t) != "GIFT_CATALOG_INVALID" {
		t.Fatalf("too big: %d %s", r.Status, r.Body)
	}
	if r := e.do("PUT", "/api/admin/gifts/catalog/nope/image", boss, map[string]any{"data": tinyPNG(t)}); r.Status != 404 {
		t.Fatalf("unknown gift: %d %s", r.Status, r.Body)
	}

	// a good picture (a data: prefix is accepted) is served without a sign-in, with safe headers
	if r := e.do("PUT", "/api/admin/gifts/catalog/small/image", boss, map[string]any{"data": "data:image/png;base64," + tinyPNG(t)}); r.Status != 200 {
		t.Fatalf("upload: %d %s", r.Status, r.Body)
	}
	img := e.do("GET", "/api/gift-images/small", "", nil)
	if img.Status != 200 || img.Header.Get("Content-Type") != "image/png" || img.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("serve: %d %v", img.Status, img.Header)
	}
	if e.do("GET", "/api/gift-images/mid", "", nil).Status != 404 {
		t.Fatal("a gift without a picture must be 404")
	}
	art := e.do("GET", "/api/gifts/art", p.Token, nil).JSON(t)["gifts"].([]any)
	if len(art) != 1 || art[0].(map[string]any)["key"] != "small" || art[0].(map[string]any)["imageV"].(float64) <= 0 {
		t.Fatalf("art list: %v", art)
	}

	// saving the list: a gift used in the odds cannot be removed; a new one can be added
	list := func(extra ...map[string]any) map[string]any {
		g := []map[string]any{
			{"key": "small", "name_en": "Small", "name_ru": "Малый", "star_value": 100},
			{"key": "mid", "name_en": "Mid", "name_ru": "Средний", "star_value": 1000},
		}
		return map[string]any{"gifts": append(g, extra...)}
	}
	if r := e.do("PUT", "/api/admin/gifts/catalog", boss, list()); r.Code(t) != "GIFT_CATALOG_INVALID" { // "big" is in the odds
		t.Fatalf("remove a used gift: %d %s", r.Status, r.Body)
	}
	all := list(map[string]any{"key": "big", "name_en": "Big", "name_ru": "Большой", "star_value": 100000}, map[string]any{"key": "star-cup", "name_en": "Cup", "star_value": 50})
	if r := e.do("PUT", "/api/admin/gifts/catalog", boss, all); r.Status != 200 {
		t.Fatalf("add a gift: %d %s", r.Status, r.Body)
	}
	for _, bad := range []map[string]any{
		{"key": "Bad Key", "name_en": "x", "star_value": 5},
		{"key": "small", "name_en": "dup", "star_value": 5},
		{"key": "zero", "name_en": "x", "star_value": 0},
		{"key": "noname", "name_en": " ", "star_value": 5},
	} {
		if r := e.do("PUT", "/api/admin/gifts/catalog", boss, list(map[string]any{"key": "big", "name_en": "Big", "star_value": 100000}, bad)); r.Code(t) != "GIFT_CATALOG_INVALID" {
			t.Fatalf("bad gift %v: %d %s", bad, r.Status, r.Body)
		}
	}

	// the picture goes away on request, and with its gift
	if r := e.do("DELETE", "/api/admin/gifts/catalog/small/image", boss, nil); r.Status != 200 {
		t.Fatalf("delete picture: %d %s", r.Status, r.Body)
	}
	if e.do("GET", "/api/gift-images/small", "", nil).Status != 404 {
		t.Fatal("picture must be gone")
	}
}
