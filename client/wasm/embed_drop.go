//go:build js && wasm

package main

import (
	"fmt"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// docDropTarget describes the cursor sitting over a raw-mode text descent
// pane — the case where a right-drag-clone gesture inserts a markdown
// reference into the doc instead of cloning the tile.
type docDropTarget struct {
	pane    *pane.Pane
	rect    paneRect
	tileID  int64 // text tile being viewed
	version int64
	// insertOffset is the markdown byte offset (line-end of the line
	// under the cursor) where the URL should be inserted. v1 uses
	// end-of-line for simplicity; caret-precise drops can come later.
	insertOffset int
}

// docRejectAt reports whether (sx, sy) lies inside a text descent pane
// whose mode makes it an invalid drop target. Today: any text descent
// in rendered mode. Used to flip the cursor to "not allowed" while
// dragging, so the user sees a clear rejection rather than silence.
func (a *App) docRejectAt(sx, sy float64) bool {
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok || p == nil {
		return false
	}
	if p.TextFocus == 0 {
		return false
	}
	if a.isURLDescent(p) {
		return false
	}
	if !pointInFileInner(p, r, sx, sy) {
		return false
	}
	return p.TextMode == rpc.TextModeRendered
}

// docDropTargetAt reports whether (sx, sy) sits inside a raw-mode text
// descent pane's content area. Returns the resolved target on success.
//
// Only raw-mode text descents are valid drop targets — rendered mode is
// read-only, and URL tiles aren't markdown.
func (a *App) docDropTargetAt(sx, sy float64) (*docDropTarget, bool) {
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok || p == nil {
		return nil, false
	}
	if p.TextFocus == 0 || p.TextMode != rpc.TextModeText {
		return nil, false
	}
	if a.isURLDescent(p) {
		return nil, false
	}
	if !pointInFileInner(p, r, sx, sy) {
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
	offset := docInsertOffsetAt(p, r, sx, sy, string(blob))
	return &docDropTarget{
		pane:         p,
		rect:         r,
		tileID:       tile.ID,
		version:      tile.Version,
		insertOffset: offset,
	}, true
}

// docInsertOffsetAt computes the markdown-byte insertion point for a drop
// landing at (sx, sy). v1 returns the end of the line under the cursor —
// simple and unambiguous, no caret tracking required. The pane's
// TextScroll / inner-box geometry maps screen y → document line.
func docInsertOffsetAt(p *pane.Pane, r paneRect, _, sy float64, src string) int {
	ix, iy, _, _ := fileInnerBox(p, r)
	_ = ix
	// Same line-height the textarea uses (file_overlay.go: fontPx = 14 *
	// fileFixedScale → lineHeight = 14 * 1.4 = 19.6).
	lineHeight := 14.0 * fileFixedScale * 1.4
	dy := sy - iy + p.TextScrollY
	row := max(int(dy/lineHeight), 0)
	// Walk to the end of the row-th line.
	pos := 0
	for range row {
		nl := strings.IndexByte(src[pos:], '\n')
		if nl < 0 {
			return len(src)
		}
		pos += nl + 1
	}
	// pos is the start of the target line; advance to its end-of-content.
	nl := strings.IndexByte(src[pos:], '\n')
	if nl < 0 {
		return len(src)
	}
	return pos + nl
}

// commitEmbedDrop inserts a markdown image-in-link for the dragged tile
// at the target offset and POSTs UpdateText. Used by the right-drag path
// when the drop landed on a raw-mode text pane.
func (a *App) commitEmbedDrop(d *dragState, dt *docDropTarget) {
	src := d.snapshotTile
	width, height := embedDropDims(&src)
	href := embedDropHref(src.ID)
	previewURL := fmt.Sprintf("/preview/tile/%d?w=%d&h=%d", src.ID, width, height)
	alt := embedDropAlt(&src)
	link := fmt.Sprintf("[![%s](%s)](%s)", alt, previewURL, href)

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
	srcText := string(bytes)
	off := min(dt.insertOffset, len(srcText))
	off = max(off, 0)
	// Surround the inserted link with spaces if the surrounding
	// characters aren't already whitespace, so the markdown parser sees
	// it as its own token.
	pre := ""
	post := ""
	if off > 0 && srcText[off-1] != ' ' && srcText[off-1] != '\n' && srcText[off-1] != '\t' {
		pre = " "
	}
	if off < len(srcText) && srcText[off] != ' ' && srcText[off] != '\n' && srcText[off] != '\t' {
		post = " "
	}
	newSrc := srcText[:off] + pre + link + post + srcText[off:]

	// Synchronously reflect the change before the RPC roundtrip: replace
	// the cached blob under the existing BlobID so canvas renders see
	// fresh content immediately, and update the textarea if the focused
	// pane is showing this same doc — otherwise the textarea keeps its
	// stale value and the user sees the old text return on next click.
	if tile.BlobID != 0 {
		a.c.PutBlob(tile.BlobID, []byte(newSrc))
	}
	if fp := a.tree.FocusedPane(); fp != nil &&
		fp.TextFocus == dt.tileID && fp.TextMode == rpc.TextModeText &&
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
			// The server's new tile has a fresh content-hashed BlobID;
			// seed the cache under it so the post-fetch render doesn't
			// have to round-trip for the blob.
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

// embedDropDims returns the embed's logical pixel dimensions, based on
// the source tile's cell footprint at the default 1.0 doc embed-zoom.
func embedDropDims(src *rpc.Tile) (int, int) {
	w := int(src.W * int64(cellPx))
	h := int(src.H * int64(cellPx))
	if w <= 0 {
		w = int(defaultEmbedW)
	}
	if h <= 0 {
		h = int(defaultEmbedH)
	}
	return w, h
}

// embedDropHref builds the markdown link href for an embed. v1 form:
// "/<tileID>" — a leaf reference that the rendered-mode click handler
// already resolves. Future work: compute the full descent path so VS
// Code can navigate into a fresh Gridwell at the right grid context.
func embedDropHref(tileID int64) string {
	return "/" + strconv.FormatInt(tileID, 10)
}

// embedDropAlt returns an alt text for the embed. Mostly informational
// for VS Code (where it shows on broken images) and accessibility tools.
func embedDropAlt(src *rpc.Tile) string {
	return fmt.Sprintf("%s tile %d", src.Kind, src.ID)
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
