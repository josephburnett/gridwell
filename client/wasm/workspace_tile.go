//go:build js && wasm

package main

// The workspace tile's client preview. The layout memo and the geometry are
// client/panepreview's and the codec client/pane's; this is fetch and draw
// glue.

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panepreview"
)

// workspaceLayout is panepreview.Layouts over the content cache; the memo
// policy is that package's. A missing body kicks its fetch here.
func (a *App) workspaceLayout(n *gridwellv1.Tile) (*pane.Tree, bool) {
	return a.views.paneLayouts.Tree(n.Id, n.BlobId, a.c.BodyGen(n.Id), func() ([]byte, bool) {
		body, ok := a.c.TileContent(n.Id)
		if !ok {
			a.fetchTileContent(n.Id, false)
		}
		return body, ok
	})
}

// paneLayoutUnreadable is panepreview.Layouts' verdict on a blob that will
// not decode: once per (tile, blob), so a corrupt layout cannot spam the
// notice row.
func (a *App) paneLayoutUnreadable(tileID string, err error) {
	a.reportErr(errsurface.Error, "layout:"+tileID, "workspace layout unreadable: "+err.Error())
}

// drawWorkspacePreview draws the stored layout small: dividers plus each
// leaf's grid one level deep, through the same machinery door previews use.
// One level deep, flat beyond, so a door inside a leaf draws as its flat
// preview. A never-arranged layout shows the split glyph.
func (a *App) drawWorkspacePreview(n *gridwellv1.Tile, x, y, w, h float64, selected, outside, dashed bool) {
	c := a.cctx
	fillRectC(c, x, y, w, h, a.pal.WorkspaceFill)

	tree, ok := a.workspaceLayout(n)
	if !ok {
		drawWorkspaceGlyph(c, x, y, w, h, a.pal.WorkspaceBorder)
	} else {
		tileRect := pane.Rect{X: x, Y: y, W: w, H: h}
		withClip(c, x, y, w, h, func() {
			for _, leaf := range panepreview.Leaves(tree, tileRect, a.rootLayoutRect()) {
				a.drawPaneLeafPreview(leaf)
			}
			// On top, so the split structure reads at any size.
			for _, d := range pane.Dividers(tree, tileRect, 1) {
				fillRectC(c, d.Rect.X, d.Rect.Y, max(d.Rect.W, 1), max(d.Rect.H, 1), a.pal.WorkspaceBorder)
			}
		})
	}

	a.strokeTileFrame(c, x, y, w, h, a.pal.WorkspaceBorder, dashed, selected)
	a.drawTileBannerLabel(n, x, y, w, h, outside)
}

// drawPaneLeafPreview paints one leaf: the grid its place resolves to, at the
// view its owner row holds. A leaf whose place or row does not resolve stays
// an empty region, and the dividers still show the arrangement.
func (a *App) drawPaneLeafPreview(leaf panepreview.Leaf) {
	gid := a.gridIDForPathFrom(leaf.Pane.Anchor(), leaf.Pane.Path())
	if gid == "" {
		return
	}
	f, ok := a.ownerView(leaf.Pane, leaf.Live)
	if !ok {
		return
	}
	v, ok := f.Live()
	if !ok {
		return
	}
	cell := leaf.Cell(v.Zoom())
	if cell < 0.5 {
		return
	}
	r := leaf.Rect
	c := a.cctx
	withClip(c, r.X, r.Y, r.W, r.H, func() {
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		drawGridLinesIn(c, a.pal.GridLineInterior, r.X, r.Y, r.W, r.H, cell, cx-v.Cx()*cell, cy-v.Cy()*cell)
		if g, ok := a.c.Grid(gid); ok {
			a.drawChildPreview(g, v.Cx(), v.Cy(), cx, cy, cell, r.X, r.Y, r.W, r.H, nil)
		}
	})
}
