//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/inflight"
	"github.com/josephburnett/gridwell/client/pane"
)

// syncInterest runs every frame, like syncMirrors: every change to what the
// panes show draws one. interest.Tracker decides when a set is owed.
func (a *App) syncInterest(rects map[string]pane.Rect) {
	if a.interest.Show(pane.Showing(a.tree, rects, a.gridIDForPathFrom, a.cachedTiles)) {
		a.kickInterest()
	}
}

func (a *App) cachedTiles(gridID string) map[string]*gridwellv1.Tile {
	if g, ok := a.c.Grid(gridID); ok {
		return g.Tiles
	}
	return nil
}

func (a *App) kickInterest() {
	select {
	case a.interestKick <- struct{}{}:
	default:
	}
}

// sendInterest is the one sender, so two sets never reach the node out of
// order. A transport failure is the event stream's to report; the next change
// or the stream's re-open sends again.
func (a *App) sendInterest() {
	for range a.interestKick {
		for {
			set, ok := a.interest.Next()
			if !ok {
				break
			}
			ctx, cancel := inflight.Bounded()
			err := a.cl.SetInterest(ctx, set)
			cancel()
			if err == nil {
				a.resolveErr("interest")
				continue
			}
			if !clientsync.Unheard(clientsync.Of(err)) {
				a.reportErr(errsurface.Error, "interest", "the node was not told what is on screen: "+err.Error())
			}
			break
		}
	}
}
