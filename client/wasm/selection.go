//go:build js && wasm

package main

import (
	"context"
	"syscall/js"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// The selection gestures' glue. client/gesture decides what a press, a band
// and Esc do to the set, which is the grid row's in every pane, and
// client/cache holds a select ahead of the node's word.

// bandState is a shift + left-drag sweep: the press's cell and the pointer's,
// in the grid the press landed on.
type bandState struct {
	paneID, gridID string
	ax, ay, bx, by int64
}

// selectionShown is the set pane p draws for the grid it shows; a content
// place shows none.
func (a *App) selectionShown(p *pane.Pane) []string {
	if p.ContentID() != "" {
		return nil
	}
	return a.c.Selection(a.gridIDForPane(p))
}

// setSelection applies sel to the selection of the grid p shows, drawn at
// once and written to the grid row through postSelection.
func (a *App) setSelection(p *pane.Pane, how string, sel gesture.Select) {
	if p.ContentID() != "" {
		return
	}
	gid := a.gridIDForPane(p)
	if g, ok := a.c.Grid(gid); ok {
		sel = sel.OnGrid(func(id string) bool { _, ok := g.Tiles[id]; return ok })
	}
	if sel.Empty() {
		a.draw()
		return
	}
	a.c.Select(gid, sel.Add, sel.Remove)
	a.postSelection(gid)
	a.emit(traceevent.Select(how, p.ID, gid, len(a.selectionShown(p))))
	a.draw()
}

// postSelection carries the grid's owed selects to its row on the grid's own
// chain. What it sends is read when it is sent, every tile still owed, so a
// parked retry carries the selects made meanwhile and a burst lands in order.
// It claims nothing; a verdict against it (a source switched off, a grid gone)
// takes what it carried down, and the write's own reconcile re-reads the grid.
func (a *App) postSelection(gid string) {
	var sent cache.SelectSent
	var callErr error
	a.post(write{
		label: "SetSelection", gid: gid, id: gid, chain: "selection:" + gid, optimistic: true,
		call: func(ctx context.Context) error {
			if sent = a.c.SelectionUp(gid); sent.Empty() {
				return nil
			}
			callErr = a.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: gid, Add: sent.Add, Remove: sent.Remove})
			return callErr
		},
		then: func() { a.c.SelectionLanded(gid, sent) },
		done: func() {
			if o := clientsync.Of(callErr); callErr != nil && clientsync.ReactOptimistic(o).DropLocal {
				a.emit(traceevent.OutboxRollback("SetSelection", gid, gid, o))
				a.c.SelectionRefused(gid, sent)
				a.scheduleDraw(traceevent.WhySelection)
			}
		},
	})
}

// toggleSelected is a shift-click on tile n in pane p.
func (a *App) toggleSelected(p *pane.Pane, n *gridwellv1.Tile) {
	a.setSelection(p, "toggle", gesture.Toggle(a.selectionShown(p), n.Id))
}

// armBand starts a sweep at the pressed cell.
func (a *App) armBand(p *pane.Pane, cellX, cellY int64) {
	a.band = &bandState{paneID: p.ID, gridID: a.gridIDForPane(p), ax: cellX, ay: cellY, bx: cellX, by: cellY}
}

// moveBand follows the pointer cell by cell.
func (a *App) moveBand(sx, sy float64) {
	b := a.band
	p := a.tree.FindPane(b.paneID)
	if p == nil {
		return
	}
	if x, y, ok := cellAtScreen(p, paneRectFor(a, p), sx, sy); ok && (x != b.bx || y != b.by) {
		b.bx, b.by = x, y
		a.draw()
	}
}

// finishBand adds every tile the band holds to the selection. A pane that
// moved off the swept grid meanwhile takes nothing.
func (a *App) finishBand(sx, sy float64) {
	a.moveBand(sx, sy)
	b := a.band
	a.band = nil
	p := a.tree.FindPane(b.paneID)
	g, ok := a.c.Grid(b.gridID)
	if p == nil || !ok || a.gridIDForPane(p) != b.gridID {
		a.draw()
		return
	}
	tiles := make([]gesture.Placed, 0, len(g.Tiles))
	for _, n := range g.Tiles {
		tiles = append(tiles, gesture.Placed{ID: n.Id, X: n.X, Y: n.Y, W: n.W, H: n.H})
	}
	band := gesture.BandOf(b.ax, b.ay, b.bx, b.by)
	a.setSelection(p, "band", gesture.Sweep(a.selectionShown(p), band, tiles))
}

// escapeIdle is what Esc reads when no gesture is armed; onCanvas says the
// key reached the canvas.
func (a *App) escapeIdle(onCanvas bool) gesture.Idle {
	p := a.tree.FocusedPane()
	if p == nil {
		return gesture.Idle{}
	}
	return gesture.Idle{
		OnCanvas:  onCanvas,
		GridPlace: p.ContentID() == "",
		Selected:  len(a.selectionShown(p)) > 0,
	}
}

// clearSelection is Esc with nothing in flight.
func (a *App) clearSelection() {
	if p := a.tree.FocusedPane(); p != nil {
		a.setSelection(p, "clear", gesture.Clear(a.selectionShown(p)))
	}
}

// clearAllSelections is the + menu's Clear selections: the node empties every
// grid's set it and its far nodes hold, each told back as SelectionChanged.
func (a *App) clearAllSelections() {
	// A verdict re-reads the grid in front of the user.
	var gid string
	if p := a.tree.FocusedPane(); p != nil {
		gid = a.gridIDForPane(p)
	}
	a.post(write{
		label: "SetSelection", gid: gid, source: "selection", failText: "the selection was not cleared",
		call: func(ctx context.Context) error {
			return a.cl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{Clear: true, All: true})
		},
	})
}

// drawSelectionBorder is the yellow border inside a selected tile, inset so a
// neighbour's or the focused tile's outline never covers it.
func (a *App) drawSelectionBorder(c js.Value, x, y, w, h float64) {
	strokeTileBorder(c, x, y, w, h, a.pal.Selection, selectionBorderPx)
}

// selectionBorderPx is wider than the kind border it covers, so it reads at
// any zoom.
const selectionBorderPx = 3.0

// drawBand outlines the cells a sweep in pane p covers.
func (a *App) drawBand(ps dragdrop.Pane, p *pane.Pane) {
	b := a.band
	if b == nil || b.paneID != p.ID {
		return
	}
	band := gesture.BandOf(b.ax, b.ay, b.bx, b.by)
	x0, y0 := ps.CellToScreen(float64(band.X), float64(band.Y))
	x1, y1 := ps.CellToScreen(float64(band.X+band.W), float64(band.Y+band.H))
	c := a.cctx
	c.Call("save")
	c.Set("globalAlpha", 0.12)
	fillRectC(c, x0, y0, x1-x0, y1-y0, a.pal.Selection)
	c.Set("globalAlpha", 1.0)
	setTileDash(c)
	strokeTileBorder(c, x0, y0, x1-x0, y1-y0, a.pal.Selection, 2)
	clearTileDash(c)
	c.Call("restore")
}
