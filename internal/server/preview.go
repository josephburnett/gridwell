package server

import (
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strconv"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// previewTile serves GET /preview/tile/<id>?w=&h=. The image source for
// markdown tile-embeds. URL tiles return their stored JPEG; other kinds
// return a kind-colored PNG placeholder.
//
// Inside Gridwell the client renders embeds natively and never hits this
// endpoint. Its job is making the same markdown URL render *something*
// recognizable in external viewers like VS Code.
func (s *Server) previewTile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Embed links carry a qualified "<plugin-uuid>/<id>"; route to the owning
	// plugin (the path keeps the "/" between uuid and id).
	qualifiedID := strings.TrimPrefix(r.URL.Path, "/preview/tile/")
	client, localID, ok := s.clientForID(qualifiedID)
	if !ok {
		http.Error(w, "invalid tile id", http.StatusBadRequest)
		return
	}
	tr, err := client.GetTile(r.Context(), &pb.GetTileRequest{TileId: localID})
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	tile := tr.Tile

	width, height := parsePreviewSize(r.URL.Query().Get("w"), r.URL.Query().Get("h"))

	if tile.Kind == rpc.KindURL {
		pr, err := client.GetTilePreview(r.Context(), &pb.GetTilePreviewRequest{TileId: localID})
		if err == nil && len(pr.Jpeg) > 0 {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(pr.Jpeg)
			return
		}
	}

	img := renderKindPlaceholder(tile.Kind, width, height)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache")
	_ = png.Encode(w, img)
}

func parsePreviewSize(ws, hs string) (int, int) {
	w, _ := strconv.Atoi(ws)
	h, _ := strconv.Atoi(hs)
	if w <= 0 {
		w = 192
	}
	if h <= 0 {
		h = 128
	}
	if w > 2048 {
		w = 2048
	}
	if h > 2048 {
		h = 2048
	}
	return w, h
}

// renderKindPlaceholder draws the fallback embed image for non-URL tiles
// (and for URL tiles with no stored JPEG yet). The image is kind-colored
// background + kind-colored border + a small centered glyph.
func renderKindPlaceholder(kind string, w, h int) image.Image {
	bg, fg := kindColors(kind)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, bg)
	stroke := h / 24
	if stroke < 2 {
		stroke = 2
	}
	drawBorder(img, stroke, fg)
	drawKindGlyph(img, kind, fg)
	return img
}

// kindColors returns (background, foreground) for a tile kind. Matches the
// color grammar from CLAUDE.md but tinted lighter for embed surfaces so
// the central glyph reads.
func kindColors(kind string) (color.RGBA, color.RGBA) {
	switch kind {
	case rpc.KindText:
		return color.RGBA{R: 232, G: 246, B: 232, A: 255},
			color.RGBA{R: 46, G: 160, B: 67, A: 255}
	case rpc.KindWell:
		return color.RGBA{R: 230, G: 240, B: 252, A: 255},
			color.RGBA{R: 47, G: 105, B: 188, A: 255}
	case rpc.KindURL:
		return color.RGBA{R: 244, G: 234, B: 252, A: 255},
			color.RGBA{R: 142, G: 84, B: 196, A: 255}
	default:
		return color.RGBA{R: 235, G: 235, B: 235, A: 255},
			color.RGBA{R: 100, G: 100, B: 100, A: 255}
	}
}

func fill(img *image.RGBA, c color.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func drawBorder(img *image.RGBA, stroke int, c color.RGBA) {
	b := img.Bounds()
	// top + bottom
	for x := b.Min.X; x < b.Max.X; x++ {
		for s := 0; s < stroke; s++ {
			img.SetRGBA(x, b.Min.Y+s, c)
			img.SetRGBA(x, b.Max.Y-1-s, c)
		}
	}
	// left + right
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for s := 0; s < stroke; s++ {
			img.SetRGBA(b.Min.X+s, y, c)
			img.SetRGBA(b.Max.X-1-s, y, c)
		}
	}
}

func drawKindGlyph(img *image.RGBA, kind string, c color.RGBA) {
	b := img.Bounds()
	cx, cy := (b.Min.X+b.Max.X)/2, (b.Min.Y+b.Max.Y)/2
	w, h := b.Dx(), b.Dy()
	sz := w
	if h < sz {
		sz = h
	}
	sz = sz / 4
	if sz < 4 {
		sz = 4
	}
	switch kind {
	case rpc.KindText:
		// three horizontal lines suggesting text
		line := sz / 6
		if line < 1 {
			line = 1
		}
		gap := sz / 3
		for i := -1; i <= 1; i++ {
			y := cy + i*gap
			drawHLine(img, cx-sz, cx+sz, y, line, c)
		}
	case rpc.KindWell:
		// inner rect (well frame)
		ringStroke := sz / 8
		if ringStroke < 2 {
			ringStroke = 2
		}
		drawRectOutline(img, cx-sz, cy-sz, cx+sz, cy+sz, ringStroke, c)
	case rpc.KindURL:
		// two interlocking chain-link arcs (simplified: two rings)
		ringStroke := sz / 6
		if ringStroke < 2 {
			ringStroke = 2
		}
		drawRing(img, cx-sz/2, cy, sz*2/3, ringStroke, c)
		drawRing(img, cx+sz/2, cy, sz*2/3, ringStroke, c)
	}
}

func drawHLine(img *image.RGBA, x0, x1, y, stroke int, c color.RGBA) {
	for x := x0; x <= x1; x++ {
		for s := -stroke / 2; s <= stroke/2; s++ {
			setIn(img, x, y+s, c)
		}
	}
}

func drawRectOutline(img *image.RGBA, x0, y0, x1, y1, stroke int, c color.RGBA) {
	for x := x0; x <= x1; x++ {
		for s := 0; s < stroke; s++ {
			setIn(img, x, y0+s, c)
			setIn(img, x, y1-s, c)
		}
	}
	for y := y0; y <= y1; y++ {
		for s := 0; s < stroke; s++ {
			setIn(img, x0+s, y, c)
			setIn(img, x1-s, y, c)
		}
	}
}

func drawRing(img *image.RGBA, cx, cy, r, stroke int, c color.RGBA) {
	rOut2 := r * r
	rIn := r - stroke
	if rIn < 0 {
		rIn = 0
	}
	rIn2 := rIn * rIn
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			d2 := dx*dx + dy*dy
			if d2 <= rOut2 && d2 >= rIn2 {
				setIn(img, cx+dx, cy+dy, c)
			}
		}
	}
}

func setIn(img *image.RGBA, x, y int, c color.RGBA) {
	b := img.Bounds()
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
		return
	}
	img.SetRGBA(x, y, c)
}
