//go:build js && wasm

package main

import (
	"slices"
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
	tileID string // resolved leaf tile id; "" if href didn't parse or target missing
}

// embedDrawer paints a single tile-embed at the given screen rect and
// records a hit entry for click handling. nil means "skip embed rendering
// in this context" (e.g., for parents that don't care about hit testing).
type embedDrawer func(x, y, w, h float64, href, alt string)

// defaultEmbedW/H is the fallback rendered size when an embed's href
// didn't include ?w=&h= in its src URL. Square: an embed is a preview of a
// tile, and tiles read as squares in the grid, so the inline block is square
// too (the drawer further centers a square preview inside whatever rect it's
// handed, so non-default sizes still render square).
const (
	defaultEmbedW = 144.0
	defaultEmbedH = 144.0
)

// makeEmbedDrawer returns an embedDrawer that renders into the named pane
// and appends to a.embedHits.
func (a *App) makeEmbedDrawer(paneID string) embedDrawer {
	anchor := ""
	if p := a.tree.FindPane(paneID); p != nil {
		anchor = uuidOf(a.gridIDForPane(p))
	}
	return func(x, y, w, h float64, href, alt string) {
		tileID := embed.ResolveEmbedTileID(anchor, href)
		var tile *rpc.Tile
		if tileID != "" {
			tile = a.findTileByID(tileID)
		}
		a.drawEmbedAt(x, y, w, h, tile, alt, embedIsLink(tile))
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

// makePreviewEmbedDrawer returns an embedDrawer for the preview pass:
// paints the kind-tinted thumbnail for every embed link, just like the
// live drawer, but skips hit-list registration. Clicking the parent text
// tile's preview should descend INTO that tile, not into an embed target
// — so the embed area doesn't need to be a separate click target here.
//
// anchorUUID is the embedding doc's plugin uuid; the href stores a bare leaf
// id (HrefForTile strips the uuid), so it's re-qualified here just as in the
// live drawer — otherwise the lookup misses and every embed paints as the
// "missing" placeholder.
func (a *App) makePreviewEmbedDrawer(anchorUUID string) embedDrawer {
	return func(x, y, w, h float64, href, alt string) {
		tileID := embed.ResolveEmbedTileID(anchorUUID, href)
		var tile *rpc.Tile
		if tileID != "" {
			tile = a.findTileByID(tileID)
		}
		a.drawEmbedAt(x, y, w, h, tile, alt, embedIsLink(tile))
	}
}

// embedIsLink reports whether an embed should render with a dashed (link)
// border. EVERY tile embedded in a markdown doc is a link, no exceptions:
// deleting an embed from the text only unlinks it (removes the markdown), it
// never deletes the underlying tile — so solid (delete-is-real) would lie. The
// only non-link case is a broken/missing target (nil), which paints the
// distinct "missing" placeholder rather than a link.
func embedIsLink(tile *rpc.Tile) bool {
	return tile != nil
}

// drawEmbedAt paints one embed into the canvas at the given screen rect.
// If tile is nil (broken or unresolved href), it paints the missing-tile
// placeholder. Otherwise it renders a real preview of the tile — the same
// one-level preview the parent-grid renderer paints (drawNodeWithPreview):
// a URL's frozen frame, a well's child-grid preview, a markdown render, etc.
// — so an embed reads as a proper preview tile, not a flat colored box.
//
// The preview is drawn as a centered SQUARE inside (x, y, w, h): a tile is a
// square in the grid, and the embed should look like one regardless of the
// rect it was laid out in.
func (a *App) drawEmbedAt(x, y, w, h float64, tile *rpc.Tile, alt string, dashed bool) {
	c := a.cctx

	// Center a square of side min(w, h) inside the rect.
	side := min(w, h)
	sx := x + (w-side)/2
	sy := y + (h-side)/2

	if tile == nil {
		// Broken reference: red-outlined placeholder.
		c.Call("save")
		c.Call("beginPath")
		c.Call("rect", sx, sy, side, side)
		c.Call("clip")
		c.Set("fillStyle", colorExitFill)
		c.Call("fillRect", sx, sy, side, side)
		c.Set("strokeStyle", colorExitBorder)
		c.Set("lineWidth", 2.0)
		c.Call("strokeRect", sx+1, sy+1, side-2, side-2)
		c.Set("fillStyle", colorExitBorder)
		label := alt
		if label == "" {
			label = "missing"
		}
		drawEmbedLabel(c, label, sx, sy, side, side)
		c.Call("restore")
		return
	}

	// parentCellSize fits the tile's footprint into the square; well previews
	// scale their child grid by it. r is the embed's own square — only the
	// markdown branch reads it, and only when a pane is descended into this
	// same tile (the embed then mirrors that live framing).
	cells := float64(max(tile.W, tile.H))
	if cells < 1 {
		cells = 1
	}
	parentCellSize := side / cells
	r := pane.Rect{X: sx, Y: sy, W: side, H: side}
	a.drawNodeWithPreview(tile, sx, sy, side, side, parentCellSize, r, false /*selected*/, false /*outside*/, dashed, "")
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

// findTileByID walks the client tile cache for any cached row with the given
// id. Used to resolve embed hrefs. On a miss it kicks a background fetch
// (fetchTileByID) to pull in the target's grid — an embed names a tile whose
// grid may never have been visited — so a later frame resolves it.
func (a *App) findTileByID(id string) *rpc.Tile {
	for _, gid := range a.c.KnownGridIDs() {
		g, ok := a.c.Grid(gid)
		if !ok {
			continue
		}
		if t, ok := g.Tiles[id]; ok {
			return &t
		}
	}
	a.fetchTileByID(id)
	return nil
}

// descendedTile resolves the tile a pane is descended into (p.TextFocus). The
// fast path is the pane's current grid; the fallback is a by-id cache walk for
// a tile that lives OFF the pane's grid — an ephemeral url visit focuses a tile
// in the plugin's scratch grid without re-anchoring the pane onto it, so the
// renderer, the url stream, and the ascent must still find it. Returns
// (_, false) when the pane isn't descended or the tile isn't cached yet.
func (a *App) descendedTile(p *pane.Pane) (rpc.Tile, bool) {
	if p.TextFocus == "" {
		return rpc.Tile{}, false
	}
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if t, ok := g.Tiles[p.TextFocus]; ok {
			return t, true
		}
	}
	if t := a.findTileByID(p.TextFocus); t != nil {
		return *t, true
	}
	return rpc.Tile{}, false
}

// descendIntoEmbed descends from the current text-tile descent into the tile
// an embed click references. Returns true if the descent was performed.
//
// The target may live in the doc's own grid (a sibling tile) or — the common
// case for a url/shell tile, which lives inside its own well — in another grid,
// or another plugin. PlanEmbedDescent decides: a same-grid target is focused /
// descended in place; a cross-grid target first re-anchors the pane onto the
// target's grid (Anchor carries the plugin uuid, so this also crosses plugins).
//
// Either way the doc's full place — anchor, path, focus, mode, scroll — is
// stashed onto the descent's saved paneState, so a single ascent restores the
// doc (restoreEmbedReturn), matching "click a tile on a grid, ascend back to
// the grid": here the doc is the grid.
func (a *App) descendIntoEmbed(p *pane.Pane, hit *embedHit) bool {
	if hit == nil {
		return false
	}
	target := a.findTileByID(hit.tileID)
	var targetGridID string
	if target != nil {
		targetGridID = target.GridID
	}
	plan := embed.PlanEmbedDescent(hit.tileID, targetGridID, a.gridIDForPane(p))
	if !plan.OK {
		return false
	}
	// Stash the doc's full place before clearing focus; patched onto the saved
	// paneState below so one ascent lands back on the doc.
	savedAnchor := p.Anchor
	savedPath := slices.Clone(p.Path)
	savedFocus := p.TextFocus
	savedMode := p.TextMode
	savedScrollX := p.TextScrollX
	savedScrollY := p.TextScrollY
	p.TextFocus = ""
	p.TextMode = ""
	// Cross-grid / cross-plugin: move the pane onto the target's grid so the
	// underlying descent focuses / descends a tile that is actually there.
	if plan.Reanchor {
		p.Anchor = plan.Anchor
		p.Path = slices.Clone(plan.Path)
	}
	a.refreshFileOverlay()
	if rpc.IsWellKind(target.Kind) {
		a.startDescent(p, target)
	} else {
		// text / url / shell
		a.startFileDescent(p, target, nil)
	}
	if top := a.local(p.ID).PeekAscent(); top != nil {
		top.Anchor = savedAnchor
		top.Path = savedPath
		top.TextFocus = savedFocus
		top.TextMode = savedMode
		top.TextScrollX = savedScrollX
		top.TextScrollY = savedScrollY
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
