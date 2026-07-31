//go:build js && wasm

package main

// The pane tile's client face: the layout memo and the live mini-render
// preview. The geometry lives in the pure client/panepreview package (the
// preview↔descent continuity is unit-tested there); the codec lives in
// client/pane; this file is only the cache/fetch/draw glue.

import (
	"context"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panepreview"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// paneLayoutEntry memoizes one pane tile's decoded workspace tree, keyed by
// the blob generation that produced it. tree == nil records a decode failure
// for that blob (reported once, not per frame).
type paneLayoutEntry struct {
	blobID int64
	tree   *pane.Tree
}

// paneTileChainPrefix returns the transit-chain prefix through which this
// tile's OWNING NODE is reached — everything before the owning-plugin
// segment ("" for a local tile, "<ssh>/" for one hop, and so on). The layout
// blob's ids are stored in the owning node's frame (the codec's relativity
// rule); the reader prepends this prefix to resolve them in its own view.
// Built from the shared id codec (NamespaceOf), never a local split.
func paneTileChainPrefix(tileID string) string {
	ns := rpc.NamespaceOf(rpc.NamespaceOf(tileID))
	if ns == "" {
		return ""
	}
	return ns + "/"
}

// paneTileLayout returns the decoded workspace tree for a pane tile,
// memoized by (tile, blob) generation. A blob change (another view's layout
// write) invalidates via the tile row — cache.Apply drops the stale content
// bytes, the memo key mismatches, and the fetch refills; until the new bytes
// land the last decoded arrangement keeps drawing (stale-but-arranged beats
// a blank flash). Returns (nil, false) for a never-arranged tile (no blob),
// a not-yet-fetched layout, or a corrupt/newer-format blob (reported once
// per blob generation, per charter §6).
func (a *App) paneTileLayout(n *rpc.Tile) (*pane.Tree, bool) {
	if n.BlobID == 0 {
		return nil, false
	}
	e := a.paneLayouts[n.ID]
	if e != nil && e.blobID == n.BlobID {
		return e.tree, e.tree != nil
	}
	body, ok := a.c.TileContent(n.ID)
	if !ok {
		a.fetchTileContent(n.ID)
		if e != nil && e.tree != nil {
			return e.tree, true
		}
		return nil, false
	}
	prefix := paneTileChainPrefix(n.ID)
	tree, err := pane.DecodeLayout(body, func(id string) string { return prefix + id })
	if err != nil {
		// Once per blob generation: the memo entry below short-circuits the
		// next frames, so a corrupt layout cannot spam the strip.
		a.reportErr(errsurface.Error, "layout:"+n.ID, "workspace layout unreadable: "+err.Error())
		a.paneLayouts[n.ID] = &paneLayoutEntry{blobID: n.BlobID}
		return nil, false
	}
	a.paneLayouts[n.ID] = &paneLayoutEntry{blobID: n.BlobID, tree: tree}
	return tree, true
}

// drawPaneTilePreview is the pane tile's parent-grid renderer: the stored
// workspace drawn small — dividers plus each leaf's grid one level deep at
// the leaf's stored viewport, via the SAME drawChildPreview machinery well
// previews use ("one level deep, flat beyond" holds here too: a well or
// pane tile inside a leaf draws as its flat face). A never-arranged or
// not-yet-loaded workspace shows the split glyph.
func (a *App) drawPaneTilePreview(n *rpc.Tile, x, y, w, h float64, selected, outside, dashed bool) {
	c := a.cctx
	c.Set("fillStyle", colorPaneTileFill)
	c.Call("fillRect", x, y, w, h)

	tree, ok := a.paneTileLayout(n)
	if !ok {
		drawPaneGlyph(c, x, y, w, h, colorPaneTileBorder)
	} else {
		tileRect := pane.Rect{X: x, Y: y, W: w, H: h}
		scale := panepreview.Scale(tileRect, a.rootLayoutRect())
		c.Call("save")
		c.Call("beginPath")
		c.Call("rect", x, y, w, h)
		c.Call("clip")
		for _, leaf := range panepreview.Leaves(tree, tileRect, scale) {
			a.drawPaneLeafPreview(leaf)
		}
		// Divider lines on top, so the split structure reads at any size.
		for _, d := range pane.Dividers(tree, tileRect, 1) {
			c.Set("fillStyle", colorPaneTileBorder)
			c.Call("fillRect", d.Rect.X, d.Rect.Y, max(d.Rect.W, 1), max(d.Rect.H, 1))
		}
		c.Call("restore")
	}

	if dashed {
		setTileDash(c)
	}
	strokeTileBorder(c, x, y, w, h, colorPaneTileBorder, tileBorderPx)
	if dashed {
		clearTileDash(c)
	}
	if selected {
		drawSelectedTileOutline(c, x, y, w, h)
	}
	a.drawTileBannerLabel(n, x, y, w, h, outside)
}

// drawPaneLeafPreview paints one leaf of the mini-render: the grid the leaf's
// place resolves to, centered on the leaf's stored viewport at its preview
// cell size. A leaf whose place doesn't resolve (remote-owned home, stale
// path — the loose-walk rule) stays an empty region; the dividers still show
// the arrangement.
func (a *App) drawPaneLeafPreview(leaf panepreview.Leaf) {
	if leaf.PreviewCell < 0.5 {
		return
	}
	gid := a.gridIDForPathFrom(leaf.Pane.Anchor, leaf.Pane.Path)
	if gid == "" {
		return
	}
	r := leaf.Rect
	c := a.cctx
	c.Call("save")
	c.Call("beginPath")
	c.Call("rect", r.X, r.Y, r.W, r.H)
	c.Call("clip")
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	originX := cx - leaf.Pane.Cx*leaf.PreviewCell
	originY := cy - leaf.Pane.Cy*leaf.PreviewCell
	drawGridLinesIn(c, colorGridLineInterior, r.X, r.Y, r.W, r.H, leaf.PreviewCell, originX, originY)
	if g, ok := a.c.Grid(gid); ok {
		a.drawChildPreview(g, leaf.Pane.Cx, leaf.Pane.Cy, cx, cy, leaf.PreviewCell,
			r.X, r.Y, r.W, r.H, "")
	}
	c.Call("restore")
}

// createPaneAtCell fires CreatePane at the given cell. Footprint is 1×1;
// created UNNAMED like wells (naming happens via the bar-title rename) and with
// no layout blob (never arranged — first descent installs the default
// single pane).
func (a *App) createPaneAtCell(p *pane.Pane, cellX, cellY int64) {
	gid := a.gridIDForPane(p)
	req := &rpc.CreatePaneRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	a.postTileMutate("CreatePane", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreatePane(ctx, req)
	}, nil)
}
