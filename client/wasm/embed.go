//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// embedHit is a click-target for one rendered tile-embed inside a text
// pane. Populated each frame by drawEmbed and consumed by the input
// handler to descend when an embed is left-clicked.
type embedHit struct {
	paneID string
	x, y   float64
	w, h   float64
	href   string
	tileID int64 // resolved leaf tile id; 0 if href didn't parse or target missing
}

// embedDrawer paints a single tile-embed at the given screen rect and
// records a hit entry for click handling. nil means "skip embed rendering
// in this context" (e.g., for parents that don't care about hit testing).
type embedDrawer func(x, y, w, h float64, href, alt string)

// defaultEmbedW/H is the fallback rendered size when an embed's href
// didn't include ?w=&h= in its src URL. Sized to match a 3x2 tile at
// cellPx — a reasonable inline block.
const (
	defaultEmbedW = 192.0
	defaultEmbedH = 128.0
)

// makeEmbedDrawer returns an embedDrawer that renders into the named pane
// and appends to a.embedHits.
func (a *App) makeEmbedDrawer(paneID string) embedDrawer {
	return func(x, y, w, h float64, href, alt string) {
		tileID := embed.LeafTileIDFromHref(href)
		var tile *rpc.Tile
		if tileID != 0 {
			tile = a.findTileByID(tileID)
		}
		a.drawEmbedAt(x, y, w, h, tile, alt)
		a.embedHits = append(a.embedHits, embedHit{
			paneID: paneID,
			x:      x,
			y:      y,
			w:      w,
			h:      h,
			href:   href,
			tileID: tileID,
		})
	}
}

// drawEmbedAt paints one embed into the canvas at the given screen rect.
// If tile is nil (broken or unresolved href), the image is the missing-tile
// placeholder. Otherwise the tile's kind drives the rendering.
func (a *App) drawEmbedAt(x, y, w, h float64, tile *rpc.Tile, alt string) {
	c := a.cctx
	c.Call("save")
	c.Call("beginPath")
	c.Call("rect", x, y, w, h)
	c.Call("clip")

	if tile == nil {
		// Broken reference: red-outlined placeholder.
		c.Set("fillStyle", colorBlackHoleFill)
		c.Call("fillRect", x, y, w, h)
		c.Set("strokeStyle", colorBlackHoleLine)
		c.Set("lineWidth", 2.0)
		c.Call("strokeRect", x+1, y+1, w-2, h-2)
		c.Set("fillStyle", colorBlackHoleLine)
		label := alt
		if label == "" {
			label = "missing"
		}
		drawEmbedLabel(c, label, x, y, w, h)
		c.Call("restore")
		return
	}

	bg, fg := embedBgFg(tile.Kind)
	c.Set("fillStyle", bg)
	c.Call("fillRect", x, y, w, h)

	// Kind-specific preview fill before the outline so it sits inside.
	switch tile.Kind {
	case rpc.KindURL:
		if img, ok := a.urlPreview.Get(tile.ID); ok && img.Truthy() {
			drawImageCover(c, img, x, y, w, h, 0, 0)
		}
	case rpc.KindWell:
		// Future: re-use drawChildPreview for a real flat preview. For now
		// the kind background + outline is the visual.
	case rpc.KindText:
		// Future: render markdown without recursing into embeds. For now
		// the kind background carries the signal.
	case rpc.KindBlackHole:
		// Filled by background tint; nothing else to paint.
	}

	c.Set("strokeStyle", fg)
	c.Set("lineWidth", 2.0)
	c.Call("strokeRect", x+1, y+1, w-2, h-2)
	c.Call("restore")
}

// embedBgFg returns the (fill, stroke) colors used for embed rendering.
// Matches the per-kind colors used by drawNode for grid-cell rendering.
func embedBgFg(kind string) (string, string) {
	switch kind {
	case rpc.KindText:
		return colorMarkdownFill, colorMarkdownLine
	case rpc.KindWell:
		return colorBg, colorFocusBorder
	case rpc.KindURL:
		return colorURLFill, colorURLLine
	case rpc.KindBlackHole:
		return colorBlackHoleFill, colorBlackHoleLine
	}
	return colorBg, colorMuted
}

// drawEmbedLabel paints a short label centered in (x, y, w, h). Used by
// the missing-tile placeholder.
func drawEmbedLabel(c js.Value, text string, x, y, w, h float64) {
	setFont(c, 11, `ui-sans-serif, system-ui, -apple-system, sans-serif`, true, false)
	c.Set("textBaseline", "middle")
	c.Set("textAlign", "center")
	c.Call("fillText", text, x+w/2, y+h/2)
	c.Set("textAlign", "start")
	c.Set("textBaseline", "top")
}

// findTileByID walks the client tile cache for any cached row with the
// given id. Used to resolve embed hrefs.
func (a *App) findTileByID(id int64) *rpc.Tile {
	for _, gid := range a.c.KnownGridIDs() {
		g, ok := a.c.Grid(gid)
		if !ok {
			continue
		}
		if t, ok := g.Tiles[id]; ok {
			return &t
		}
	}
	return nil
}

// descendIntoEmbed performs a leaf-swap descent from the current text-tile
// descent into the tile referenced by an embed click. Returns true if the
// descent was performed.
//
// v1 limitation: only embeds whose target tile is in the same grid as the
// current descended pane are followed; cross-grid embeds are a future
// extension (they require fetching the target's parent grid chain).
//
// The ascent that follows lands on the doc's grid, not back on the doc
// itself. Restoring the doc as the ascent target is future work — it
// requires extending the pane Path concept to remember text-tile
// breadcrumbs.
func (a *App) descendIntoEmbed(p *pane.Pane, hit *embedHit) bool {
	if hit == nil || hit.tileID == 0 {
		return false
	}
	target := a.findTileByID(hit.tileID)
	if target == nil {
		return false
	}
	if target.GridID != a.gridIDForPath(p.Path) {
		// Cross-grid embed targets are not yet supported.
		return false
	}
	if target.Kind != rpc.KindText && target.Kind != rpc.KindURL && target.Kind != rpc.KindWell {
		return false
	}
	// Clear the current text descent so the descent machinery sees a
	// grid-state pane, then dispatch to the appropriate descent for the
	// target kind. The animation goes doc-overtake → target-overtake.
	p.TextFocus = 0
	p.TextMode = ""
	a.refreshFileOverlay()
	if target.Kind == rpc.KindWell {
		a.startDescent(p, target)
	} else {
		a.startFileDescent(p, target, nil)
	}
	return true
}

// embedHitAt returns the topmost embed hit covering (sx, sy) in the named
// pane, or nil if no embed is under the point.
func (a *App) embedHitAt(paneID string, sx, sy float64) *embedHit {
	for i := len(a.embedHits) - 1; i >= 0; i-- {
		h := &a.embedHits[i]
		if h.paneID != paneID {
			continue
		}
		if sx >= h.x && sx < h.x+h.w && sy >= h.y && sy < h.y+h.h {
			return h
		}
	}
	return nil
}
