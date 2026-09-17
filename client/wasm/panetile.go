//go:build js && wasm

package main

// The pane tile's client face. The layout memo and the geometry are
// client/panepreview's and the codec client/pane's; this is fetch and draw
// glue.

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panepreview"
)

// paneTileLayout is panepreview.Layouts over the content cache; the memo
// policy is that package's. A missing body kicks its fetch here.
func (a *App) paneTileLayout(n *gridwellv1.Tile) (*pane.Tree, bool) {
	return a.views.paneLayouts.Tree(n.Id, n.BlobId, func() ([]byte, bool) {
		body, ok := a.c.TileContent(n.Id)
		if !ok {
			a.fetchTileContent(n.Id)
		}
		return body, ok
	})
}

// paneLayoutUnreadable is panepreview.Layouts' verdict on a blob that will
// not decode: once per (tile, blob), so a corrupt layout cannot spam the
// strip.
func (a *App) paneLayoutUnreadable(tileID string, err error) {
	a.reportErr(errsurface.Error, "layout:"+tileID, "workspace layout unreadable: "+err.Error())
}

// drawPaneTilePreview draws the stored layout small: dividers plus each
// leaf's grid one level deep, through the same machinery well previews use.
// One level deep, flat beyond, so a well inside a leaf draws as its flat
// face. A never-arranged layout shows the split glyph.
func (a *App) drawPaneTilePreview(n *gridwellv1.Tile, x, y, w, h float64, selected, outside, dashed bool) {
	c := a.cctx
	c.Set("fillStyle", colorPaneTileFill)
	c.Call("fillRect", x, y, w, h)

	tree, ok := a.paneTileLayout(n)
	if !ok {
		drawPaneGlyph(c, x, y, w, h, colorPaneTileBorder)
	} else {
		tileRect := pane.Rect{X: x, Y: y, W: w, H: h}
		scale := panepreview.Scale(tileRect, a.rootLayoutRect())
		withClip(c, x, y, w, h, func() {
			for _, leaf := range panepreview.Leaves(tree, tileRect, scale) {
				a.drawPaneLeafPreview(leaf)
			}
			// On top, so the split structure reads at any size.
			for _, d := range pane.Dividers(tree, tileRect, 1) {
				c.Set("fillStyle", colorPaneTileBorder)
				c.Call("fillRect", d.Rect.X, d.Rect.Y, max(d.Rect.W, 1), max(d.Rect.H, 1))
			}
		})
	}

	strokeTileFrame(c, x, y, w, h, colorPaneTileBorder, dashed, selected)
	a.drawTileBannerLabel(n, x, y, w, h, outside)
}

// drawPaneLeafPreview paints one leaf: the grid its place resolves to,
// centered on its stored viewport. A leaf whose place does not resolve stays
// an empty region, and the dividers still show the arrangement.
func (a *App) drawPaneLeafPreview(leaf panepreview.Leaf) {
	if leaf.PreviewCell < 0.5 {
		return
	}
	gid := a.gridIDForPathFrom(leaf.Pane.Anchor(), leaf.Pane.Path())
	if gid == "" {
		return
	}
	r := leaf.Rect
	c := a.cctx
	withClip(c, r.X, r.Y, r.W, r.H, func() {
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		originX := cx - leaf.Pane.Cx*leaf.PreviewCell
		originY := cy - leaf.Pane.Cy*leaf.PreviewCell
		drawGridLinesIn(c, colorGridLineInterior, r.X, r.Y, r.W, r.H, leaf.PreviewCell, originX, originY)
		if g, ok := a.c.Grid(gid); ok {
			a.drawChildPreview(g, leaf.Pane.Cx, leaf.Pane.Cy, cx, cy, leaf.PreviewCell,
				r.X, r.Y, r.W, r.H, "")
		}
	})
}

// createPaneAtCell lands an unnamed pane tile with no layout blob, so it is
// never-arranged and the first descent installs the default single pane.
func (a *App) createPaneAtCell(gid string, cellX, cellY int64) {
	req := &gridwellv1.CreateTileRequest{GridId: gid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindPane, X: cellX, Y: cellY, W: 1, H: 1}}
	a.postTileMutate("CreatePane", gid, func(ctx context.Context) (*gridwellv1.Tile, error) {
		return a.cl.CreateTile(ctx, req)
	}, nil)
}
