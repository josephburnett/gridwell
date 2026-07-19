//go:build js && wasm

package main

import (
	"math"
	"slices"
	"syscall/js"

	embedpkg "github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// docDropTarget describes the cursor sitting over a raw-mode text descent
// pane — the case where a drag gesture inserts a markdown reference into
// the doc instead of moving / cloning the tile.
type docDropTarget struct {
	pane         *pane.Pane
	rect         pane.Rect
	tileID       string
	version      int64
	insertOffset int
}

// classifyDocTargetAt resolves the cursor at (sx, sy) against any text
// descent pane underneath. The wasm-specific work is gathering the pane
// state from the focused-pane tree; the classification rule lives in
// client/embed.
func (a *App) classifyDocTargetAt(sx, sy float64) (embedpkg.DocTarget, *pane.Pane, pane.Rect) {
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok || p == nil {
		return embedpkg.DocTargetNone, nil, pane.Rect{}
	}
	state := embedpkg.PaneState{
		HasTextFocus: p.TextFocus != "",
		IsURLDescent: a.isURLDescent(p),
		TextMode:     p.TextMode,
		Inside:       pointInFileInner(p, r, sx, sy),
		ReadOnly:     a.docPaneReadOnly(p),
	}
	return embedpkg.ClassifyDocTarget(state), p, r
}

// docPaneReadOnly reports whether the text tile a pane is descended into is
// read-only (source-backed: a plugin's @info / file metadata). Such docs reject
// drops even in an editable mode.
func (a *App) docPaneReadOnly(p *pane.Pane) bool {
	g, ok := a.c.Grid(a.gridIDForPane(p))
	if !ok {
		return false
	}
	file, ok := g.Tiles[p.TextFocus]
	return ok && a.tileReadOnly(&file)
}

// docRejectAt reports whether (sx, sy) lies over a read-only text descent.
// Used to flip the cursor to "not allowed" while dragging (editable docs —
// raw or rendered — are now drop targets, not rejects).
func (a *App) docRejectAt(sx, sy float64) bool {
	mode, _, _ := a.classifyDocTargetAt(sx, sy)
	return mode == embedpkg.DocTargetReject
}

// docDropTargetAt returns a populated docDropTarget when (sx, sy) lies inside
// an editable text descent pane. In raw mode the insertion point is the end of
// the line under the cursor; in rendered mode it's the source caret offset
// under the cursor (so the embed lands where you drop it, and you see it right
// away).
func (a *App) docDropTargetAt(sx, sy float64) (*docDropTarget, bool) {
	mode, p, r := a.classifyDocTargetAt(sx, sy)
	if mode != embedpkg.DocTargetRaw && mode != embedpkg.DocTargetRendered {
		return nil, false
	}
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		return nil, false
	}
	tile, ok := g.Tiles[p.TextFocus]
	if !ok {
		return nil, false
	}
	var offset int
	if mode == embedpkg.DocTargetRendered {
		off, ok := a.markdownCaretAt(p, r, &tile, sx, sy)
		if !ok {
			return nil, false
		}
		offset = off
	} else {
		blob, ok := a.c.TileContent(tile.ContentID())
		if !ok {
			return nil, false
		}
		_, iy, _, _ := textInnerBox(p, r)
		row := embedpkg.RowAt(iy, sy, p.TextScrollY, textLineHeightPx)
		offset = embedpkg.LineEndOffset(string(blob), row)
	}
	return &docDropTarget{
		pane:         p,
		rect:         r,
		tileID:       tile.ID,
		version:      tile.Version,
		insertOffset: offset,
	}, true
}

// textLineHeightPx is the rendered line height in the file text-mode
// view. Matches the textarea (file_overlay.go: 14px font × 1.4 leading).
const textLineHeightPx = 14.0 * textFixedScale * 1.4

// commitEmbedDrop inserts a markdown plain link for the dragged tile at
// the target offset and POSTs UpdateText. The link is anchored at the
// browser's current origin so it resolves outside Gridwell too.
func (a *App) commitEmbedDrop(d *dragState, dt *docDropTarget) {
	src := d.snapshotTile
	alt := src.AltText
	if alt == "" {
		alt = embedpkg.DefaultAlt(src.Kind, src.ID)
	}
	link := embedpkg.Markdown(browserOrigin(), src.ID, alt)

	gid := a.gridIDForPane(dt.pane)
	g, hasGrid := a.c.Grid(gid)
	if !hasGrid {
		return
	}
	tile, hasTile := g.Tiles[dt.tileID]
	if !hasTile {
		return
	}
	bytes, hasBlob := a.c.TileContent(tile.ContentID())
	if !hasBlob {
		return
	}
	newSrc := embedpkg.Insert(string(bytes), link, dt.insertOffset)

	// Synchronously reflect the change before the RPC roundtrip:
	//   - cache: write the new body through the content store (tile-scoped by
	//     tile id, so the inserted embed never leaks into a clone), the same
	//     accessor the renderer reads.
	//   - textarea: if the singleton textarea is currently bound to this
	//     doc (regardless of which pane has focus), push the new value
	//     in directly. Without this, focus-shifting back to the doc
	//     would re-display the stale buffer, and worse, a raw→rendered
	//     toggle would save the stale buffer back over the drop.
	a.c.PutEditedContent(tile.ContentID(), []byte(newSrc))
	if a.lastTextareaTileID == dt.tileID &&
		!a.textTextarea.IsUndefined() && !a.textTextarea.IsNull() {
		a.textTextarea.Set("value", newSrc)
	}
	// In rendered mode, advance the caret past the just-inserted embed so it's
	// visible and typing continues after it. The inserted length (link plus any
	// padding spaces from Insert) is the source-length delta.
	if dt.pane.TextMode == rpc.TextModeRendered {
		a.local(dt.pane.ID).SetCaret(dt.insertOffset + (len(newSrc) - len(bytes)))
	}

	// Claim the save basis — the version of the bytes the insert was computed
	// over — not the drag-time row version: a foreign edit landing since the
	// drag started must conflict visibly, never be overwritten.
	saveVersion := dt.version
	if base, ok := a.c.SaveBasis(dt.tileID); ok {
		saveVersion = base
	}
	path := slices.Clone(dt.pane.Path)
	go func() {
		_, ok := a.postUpdateText(gid, &rpc.UpdateTextRequest{
			Path:    rpc.Path{WellIDs: path},
			TileID:  dt.tileID,
			Version: saveVersion,
			Data:    []byte(newSrc),
		}, []byte(newSrc))
		if !ok {
			return
		}
		a.fetchGrid(gid)
		if d.srcGridID != gid {
			a.fetchGrid(d.srcGridID)
		}
	}()
}

// browserOrigin returns window.location.origin (e.g.
// "http://localhost:8080") so links can be anchored absolutely. An
// empty result falls back to same-origin relative paths.
func browserOrigin() string {
	loc := js.Global().Get("location")
	if !loc.Truthy() {
		return ""
	}
	o := loc.Get("origin")
	if !o.Truthy() {
		return ""
	}
	return o.String()
}

// drawGhostLinkBadge paints the chain-link glyph over the dragged ghost
// when it's sitting over a doc drop target. Same chain-link visual as
// the right-click-stationary hint on a rendered embed.
func drawGhostLinkBadge(c js.Value, cx, cy, size float64) {
	stroke := size * 0.10
	if stroke < 2 {
		stroke = 2
	}
	r := size * 0.20
	off := r * 0.55
	c.Set("strokeStyle", colorPlusFg)
	c.Set("lineWidth", stroke)
	c.Call("beginPath")
	c.Call("arc", cx-off, cy, r, 0.0, 2*math.Pi, false)
	c.Call("stroke")
	c.Call("beginPath")
	c.Call("arc", cx+off, cy, r, 0.0, 2*math.Pi, false)
	c.Call("stroke")
}

// drawGhostNoEntryBadge paints the international "no entry" sign (red
// disc, white ring, white diagonal slash) centered at (cx, cy). Used as
// a ghost overlay during drags whose drop would be rejected — most
// notably left-drag (move) of a source-grid tile into a regular grid,
// which the server rejects in favor of right-drag (clone/link).
func drawGhostNoEntryBadge(c js.Value, cx, cy, size float64) {
	radius := size * 0.32
	if radius < 14 {
		radius = 14
	}
	ringW := radius * 0.18
	// Red disc.
	c.Set("fillStyle", colorNoEntryFill)
	c.Call("beginPath")
	c.Call("arc", cx, cy, radius, 0.0, 2*math.Pi, false)
	c.Call("fill")
	// White ring inside the red.
	c.Set("strokeStyle", colorNoEntryStroke)
	c.Set("lineWidth", ringW)
	c.Call("beginPath")
	c.Call("arc", cx, cy, radius-ringW/2-1, 0.0, 2*math.Pi, false)
	c.Call("stroke")
	// White diagonal slash (top-left → bottom-right).
	slashR := radius - ringW*1.4
	angle := math.Pi / 4
	c.Set("lineCap", "round")
	c.Call("beginPath")
	c.Call("moveTo", cx+math.Cos(angle+math.Pi)*slashR, cy+math.Sin(angle+math.Pi)*slashR)
	c.Call("lineTo", cx+math.Cos(angle)*slashR, cy+math.Sin(angle)*slashR)
	c.Call("stroke")
	c.Set("lineCap", "butt")
	c.Set("lineWidth", 1.0)
}
