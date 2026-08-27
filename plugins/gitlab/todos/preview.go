package todos

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// The tile FACE. A page tile's grid face is its preview image (the
// client draws nothing else but the label until one arrives), so a
// todo without one is an empty box — the provider derives the face
// from the content, as fs derives a thumbnail: a card with the state
// stripe, the ref and title, and the action line.

const (
	previewW = 320
	previewH = 160
)

var (
	cardBg      = color.RGBA{0xff, 0xff, 0xff, 0xff}
	cardInk     = color.RGBA{0x1f, 0x23, 0x28, 0xff}
	cardMuted   = color.RGBA{0x6b, 0x72, 0x80, 0xff}
	stripeOpen  = color.RGBA{0xf5, 0xb8, 0x00, 0xff}
	stripeDone  = color.RGBA{0x2e, 0xa0, 0x5c, 0xff}
	doneInk     = color.RGBA{0x8a, 0x93, 0x9e, 0xff}
	cardPadding = 14
)

// Preview renders the todo's face as a JPEG.
func Preview(t *Todo) []byte {
	img := image.NewRGBA(image.Rect(0, 0, previewW, previewH))
	draw.Draw(img, img.Bounds(), &image.Uniform{cardBg}, image.Point{}, draw.Src)
	stripe := stripeOpen
	ink := cardInk
	if t.Done() {
		stripe = stripeDone
		ink = doneInk
	}
	draw.Draw(img, image.Rect(0, 0, 8, previewH), &image.Uniform{stripe}, image.Point{}, draw.Src)

	face := basicfont.Face7x13
	x := 8 + cardPadding
	y := cardPadding + face.Metrics().Ascent.Ceil()
	line := func(s string, c color.Color) {
		if y > previewH-cardPadding {
			return
		}
		d := &font.Drawer{Dst: img, Src: &image.Uniform{c}, Face: face, Dot: fixed.P(x, y)}
		d.DrawString(s)
		y += face.Metrics().Height.Ceil() + 3
	}
	head := t.Title()
	if r := t.Ref(); r != "" {
		head = r + "  " + head
	}
	if t.Done() {
		head = "✓ " + head
	}
	cols := (previewW - x - cardPadding) / face.Advance
	for _, l := range wrap(head, cols, 3) {
		line(l, ink)
	}
	y += 4
	meta := t.Action()
	if t.Author.Name != "" {
		meta += " · " + t.Author.Name
	}
	if t.Project.PathWithNamespace != "" {
		meta += " · " + t.Project.PathWithNamespace
	}
	for _, l := range wrap(meta, cols, 2) {
		line(l, cardMuted)
	}
	line(t.CreatedAt.UTC().Format("2006-01-02"), cardMuted)

	var out bytes.Buffer
	_ = jpeg.Encode(&out, img, &jpeg.Options{Quality: 80})
	return out.Bytes()
}

// wrap breaks s into at most maxLines lines of at most cols runes,
// at spaces where it can; the last line gets an ellipsis if cut.
func wrap(s string, cols, maxLines int) []string {
	if cols < 4 {
		cols = 4
	}
	var lines []string
	words := strings.Fields(s)
	cur := ""
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for _, w := range words {
		for len([]rune(w)) > cols {
			r := []rune(w)
			if cur != "" {
				flush()
			}
			lines = append(lines, string(r[:cols]))
			w = string(r[cols:])
		}
		if cur == "" {
			cur = w
		} else if len([]rune(cur))+1+len([]rune(w)) <= cols {
			cur += " " + w
		} else {
			flush()
			cur = w
		}
	}
	flush()
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		last := []rune(lines[maxLines-1])
		if len(last) > cols-1 {
			last = last[:cols-1]
		}
		lines[maxLines-1] = string(last) + "…"
	}
	return lines
}
