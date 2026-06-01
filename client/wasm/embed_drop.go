//go:build js && wasm

package main

import (
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
	tileID       int64
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
		HasTextFocus: p.TextFocus != 0,
		IsURLDescent: a.isURLDescent(p),
		TextMode:     p.TextMode,
		Inside:       pointInFileInner(p, r, sx, sy),
	}
	return embedpkg.ClassifyDocTarget(state), p, r
}

// docRejectAt reports whether (sx, sy) lies over a text descent that's
// in rendered mode (read-only). Used to flip the cursor to "not allowed"
// while dragging.
func (a *App) docRejectAt(sx, sy float64) bool {
	mode, _, _ := a.classifyDocTargetAt(sx, sy)
	return mode == embedpkg.DocTargetRendered
}

// docDropTargetAt returns a populated docDropTarget when (sx, sy) lies
// inside a raw-mode text descent pane. The insertion point is the end
// of the line under the cursor.
func (a *App) docDropTargetAt(sx, sy float64) (*docDropTarget, bool) {
	mode, p, r := a.classifyDocTargetAt(sx, sy)
	if mode != embedpkg.DocTargetRaw {
		return nil, false
	}
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return nil, false
	}
	tile, ok := g.Tiles[p.TextFocus]
	if !ok {
		return nil, false
	}
	blob, ok := a.c.Blob(tile.BlobID)
	if !ok {
		return nil, false
	}
	_, iy, _, _ := fileInnerBox(p, r)
	row := embedpkg.RowAt(iy, sy, p.TextScrollY, fileLineHeightPx)
	offset := embedpkg.LineEndOffset(string(blob), row)
	return &docDropTarget{
		pane:         p,
		rect:         r,
		tileID:       tile.ID,
		version:      tile.Version,
		insertOffset: offset,
	}, true
}

// fileLineHeightPx is the rendered line height in the file text-mode
// view. Matches the textarea (file_overlay.go: 14px font × 1.4 leading).
const fileLineHeightPx = 14.0 * fileFixedScale * 1.4

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

	gid := a.gridIDForPath(dt.pane.Path)
	g, hasGrid := a.c.Grid(gid)
	if !hasGrid {
		return
	}
	tile, hasTile := g.Tiles[dt.tileID]
	if !hasTile {
		return
	}
	bytes, hasBlob := a.c.Blob(tile.BlobID)
	if !hasBlob {
		return
	}
	newSrc := embedpkg.Insert(string(bytes), link, dt.insertOffset)

	// Synchronously reflect the change before the RPC roundtrip:
	//   - cache: replace blob under the existing BlobID so canvas
	//     renders see fresh content immediately.
	//   - textarea: if the singleton textarea is currently bound to this
	//     doc (regardless of which pane has focus), push the new value
	//     in directly. Without this, focus-shifting back to the doc
	//     would re-display the stale buffer, and worse, a raw→rendered
	//     toggle would save the stale buffer back over the drop.
	if tile.BlobID != 0 {
		a.c.PutBlob(tile.BlobID, []byte(newSrc))
	}
	if a.lastTextareaTileID == dt.tileID &&
		!a.fileTextarea.IsUndefined() && !a.fileTextarea.IsNull() {
		a.fileTextarea.Set("value", newSrc)
	}

	path := append([]int64(nil), dt.pane.Path...)
	go func() {
		req := rpc.UpdateTextRequest{
			Path:    rpc.Path{WellIDs: path},
			TileID:  dt.tileID,
			Version: dt.version,
			Data:    []byte(newSrc),
		}
		var resp rpc.TileResponse
		status, err := postJSON("/rpc/UpdateText", req, &resp)
		if err == nil && status == 200 {
			a.c.PutBlob(resp.Tile.BlobID, []byte(newSrc))
			a.fetchGrid(gid)
			if d.srcGridID != gid {
				a.fetchGrid(d.srcGridID)
			}
			return
		}
		if status == 409 {
			a.refetchGridOnConflict(gid, "UpdateText")
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
	c.Call("arc", cx-off, cy, r, 0.0, 2*3.14159, false)
	c.Call("stroke")
	c.Call("beginPath")
	c.Call("arc", cx+off, cy, r, 0.0, 2*3.14159, false)
	c.Call("stroke")
}
