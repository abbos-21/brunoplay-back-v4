// Package combopost posts the daily combo to the ambassadors Telegram channel: one picture of the four combo images in order,
// a caption in Russian and English, and an "open the game" button.
package combopost

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// the eight combo pictures of the game, by combo image id (the same artwork the game and the admin show)
//
//go:embed art/*.png
var artFS embed.FS

const (
	canvasW = 1080
	canvasH = 380
	tileSz  = 220
	gapSz   = 30
	artSz   = 200
)

// Render draws the combo: the pictures in order on tiles, with small arrows between them, on the game's blue.
func Render(items []int16) ([]byte, error) {
	n := len(items)
	if n == 0 {
		return nil, fmt.Errorf("combopost: no pictures")
	}
	img := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	// the background: the blue of the game, a little lighter in the middle
	for y := 0; y < canvasH; y++ {
		for x := 0; x < canvasW; x++ {
			dx, dy := float64(x-canvasW/2)/float64(canvasW/2), float64(y-canvasH/2)/float64(canvasH/2)
			d := dx*dx*0.6 + dy*dy*0.4
			if d > 1 {
				d = 1
			}
			img.SetRGBA(x, y, color.RGBA{R: uint8(34 - 14*d), G: uint8(98 - 36*d), B: uint8(210 - 56*d), A: 255})
		}
	}

	total := n*tileSz + (n-1)*gapSz
	x0 := (canvasW - total) / 2
	y0 := (canvasH - tileSz) / 2
	for i, id := range items {
		x := x0 + i*(tileSz+gapSz)
		fillRound(img, image.Rect(x, y0+6, x+tileSz, y0+6+tileSz), 40, color.NRGBA{0, 0, 0, 60}) // a soft shadow
		fillRound(img, image.Rect(x, y0, x+tileSz, y0+tileSz), 40, color.NRGBA{255, 255, 255, 46})
		raw, err := artFS.ReadFile(fmt.Sprintf("art/%d.png", id))
		if err != nil {
			return nil, fmt.Errorf("combopost: no picture for id %d", id)
		}
		art, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		ax, ay := x+(tileSz-artSz)/2, y0+(tileSz-artSz)/2
		draw.Draw(img, image.Rect(ax, ay, ax+artSz, ay+artSz), art, art.Bounds().Min, draw.Over)
		if i < n-1 {
			arrow(img, x+tileSz+gapSz/2, y0+tileSz/2)
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// fillRound blends a rounded rectangle (anti-aliased by 3x3 sampling at the corners).
func fillRound(dst *image.RGBA, r image.Rectangle, rad int, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cov := coverage(x-r.Min.X, y-r.Min.Y, r.Dx(), r.Dy(), rad)
			if cov > 0 {
				blend(dst, x, y, c, cov)
			}
		}
	}
}

func coverage(x, y, w, h, rad int) float64 {
	if (x >= rad && x < w-rad) || (y >= rad && y < h-rad) {
		return 1
	}
	in := 0
	for sy := 0; sy < 3; sy++ {
		for sx := 0; sx < 3; sx++ {
			px, py := float64(x)+(float64(sx)+0.5)/3, float64(y)+(float64(sy)+0.5)/3
			cx, cy := float64(rad), float64(rad)
			if px > float64(w)/2 {
				cx = float64(w - rad)
			}
			if py > float64(h)/2 {
				cy = float64(h - rad)
			}
			if (px-cx)*(px-cx)+(py-cy)*(py-cy) <= float64(rad*rad) {
				in++
			}
		}
	}
	return float64(in) / 9
}

// arrow draws a small triangle pointing right, centred on (cx, cy).
func arrow(dst *image.RGBA, cx, cy int) {
	const hw, hh = 8, 13
	for x := -hw; x <= hw; x++ {
		half := hh * (hw - x) / (2 * hw) // tall at the left, a point at the right
		for y := -half; y <= half; y++ {
			blend(dst, cx+x, cy+y, color.NRGBA{255, 255, 255, 190}, 1)
		}
	}
}

func blend(dst *image.RGBA, x, y int, c color.NRGBA, cov float64) {
	if !image.Pt(x, y).In(dst.Bounds()) {
		return
	}
	a := float64(c.A) / 255 * cov
	o := dst.RGBAAt(x, y)
	mix := func(s, d uint8) uint8 { return uint8(float64(s)*a + float64(d)*(1-a)) }
	dst.SetRGBA(x, y, color.RGBA{R: mix(c.R, o.R), G: mix(c.G, o.G), B: mix(c.B, o.B), A: 255})
}
