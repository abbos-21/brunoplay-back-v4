// Package combopost posts the daily combo to the ambassadors Telegram channel: one picture of the four combo images in order (as the game's combo row),
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

// the colours of the game's combo row, and nothing else: the page yellow, the cream cells and the brown dividers
var (
	pageYellow = color.RGBA{0xfb, 0xe1, 0x66, 0xff}
	cellCream  = color.NRGBA{0xfd, 0xe7, 0xb8, 0xff}
	divider    = color.NRGBA{0xcf, 0x87, 0x41, 0xff}
)

const (
	canvasW  = 1080
	canvasH  = 340
	cellW    = 240
	cellH    = 220
	artSz    = 170
	radius   = 54
	divW     = 3
	insetPx  = 8 // the soft light on the top edge and shade on the bottom edge, as on the game's row
	insetMax = 0.25
)

// Render draws the combo as the game shows it: one cream row with a brown border, the pictures in order and thin dividers between them,
// on the page yellow.
func Render(items []int16) ([]byte, error) {
	n := len(items)
	if n == 0 {
		return nil, fmt.Errorf("combopost: no pictures")
	}
	img := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	draw.Draw(img, img.Bounds(), &image.Uniform{pageYellow}, image.Point{}, draw.Src)

	rowW := n * cellW
	row := image.Rect((canvasW-rowW)/2, (canvasH-cellH)/2, (canvasW-rowW)/2+rowW, (canvasH-cellH)/2+cellH)
	// the border (the brown of the dividers) is the outer rounded shape; the cream and its light and shade sit inside it
	inner := image.Rect(row.Min.X+divW, row.Min.Y+divW, row.Max.X-divW, row.Max.Y-divW)
	for y := row.Min.Y; y < row.Max.Y; y++ {
		for x := row.Min.X; x < row.Max.X; x++ {
			if outer := coverage(x-row.Min.X, y-row.Min.Y, row.Dx(), row.Dy(), radius); outer > 0 {
				blend(img, x, y, divider, outer)
			}
			if !image.Pt(x, y).In(inner) {
				continue
			}
			cov := coverage(x-inner.Min.X, y-inner.Min.Y, inner.Dx(), inner.Dy(), radius-divW)
			if cov == 0 {
				continue
			}
			blend(img, x, y, cellCream, cov)
			if d := y - inner.Min.Y; d < insetPx {
				blend(img, x, y, color.NRGBA{255, 255, 255, 255}, cov*insetMax*float64(insetPx-d)/insetPx)
			}
			if d := inner.Max.Y - 1 - y; d < insetPx {
				blend(img, x, y, color.NRGBA{0, 0, 0, 255}, cov*insetMax*float64(insetPx-d)/insetPx)
			}
		}
	}
	for i := 1; i < n; i++ {
		x := row.Min.X + i*cellW - divW/2
		for y := row.Min.Y; y < row.Max.Y; y++ {
			for dx := 0; dx < divW; dx++ {
				blend(img, x+dx, y, divider, 1)
			}
		}
	}
	for i, id := range items {
		raw, err := artFS.ReadFile(fmt.Sprintf("art/%d.png", id))
		if err != nil {
			return nil, fmt.Errorf("combopost: no picture for id %d", id)
		}
		art, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		ax := row.Min.X + i*cellW + (cellW-artSz)/2
		ay := row.Min.Y + (cellH-artSz)/2
		draw.Draw(img, image.Rect(ax, ay, ax+artSz, ay+artSz), art, art.Bounds().Min, draw.Over)
	}

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
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

func blend(dst *image.RGBA, x, y int, c color.NRGBA, cov float64) {
	if !image.Pt(x, y).In(dst.Bounds()) {
		return
	}
	a := float64(c.A) / 255 * cov
	o := dst.RGBAAt(x, y)
	mix := func(s, d uint8) uint8 { return uint8(float64(s)*a + float64(d)*(1-a)) }
	dst.SetRGBA(x, y, color.RGBA{R: mix(c.R, o.R), G: mix(c.G, o.G), B: mix(c.B, o.B), A: 255})
}
