//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/inflight"
	"github.com/josephburnett/gridwell/client/pane"
)

// syncView runs every draw, like syncMirrors: every change to what the panes
// show draws one. pane.View is the view; subscription.Tracker decides when it is
// owed.
func (a *App) syncView(rects map[string]pane.Rect) {
	if a.subscription.Show(pane.View(a.tree, &a.ws, rects, a.caps.Label(), a.gridIDForPathFrom, a.cachedTiles)) {
		a.kickSubscription()
	}
}

func (a *App) cachedTiles(gridID string) map[string]*gridwellv1.Tile {
	if g, ok := a.c.Grid(gridID); ok {
		return g.Tiles
	}
	return nil
}

func (a *App) kickSubscription() {
	select {
	case a.subscriptionKick <- struct{}{}:
	default:
	}
}

// sendView is the one sender, so two views never reach the node out of order.
// A transport failure is the event stream's to report; the next change or the
// stream's re-open sends again.
func (a *App) sendView() {
	for range a.subscriptionKick {
		for {
			v, ok := a.subscription.Next()
			if !ok {
				break
			}
			ctx, cancel := inflight.Bounded()
			err := a.cl.SetView(ctx, v)
			cancel()
			if err == nil {
				a.resolveErr("subscription")
				continue
			}
			if !clientsync.Unheard(clientsync.Of(err)) {
				a.reportErr(errsurface.Error, "subscription", "the node was not told what is on screen: "+err.Error())
			}
			break
		}
	}
}
