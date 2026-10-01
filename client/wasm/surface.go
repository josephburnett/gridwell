//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
)

// surfaceKind is one kind of live surface as the takeover rule sees it: who
// holds one, how one closes with its freeze, and how one moves to another
// pane whole. Every kind moves, so a takeover never rebuilds a surface.
type surfaceKind struct {
	holders func() []pane.Holder
	close   func(paneID string)
	move    func(fromID string, to *pane.Pane)
}

func (a *App) urlSurface() surfaceKind {
	return surfaceKind{a.urlSurfaces, func(id string) { a.closeURLStream(id, true) }, a.moveURLView}
}

func (a *App) shellSurface() surfaceKind {
	return surfaceKind{a.shellSurfaces, func(id string) { a.closeShellStream(id, true) }, a.moveShellStream}
}

// engage applies pane.TakeOver to tileID going live in p and reports whether
// a fresh surface is still to be placed there.
func (a *App) engage(k surfaceKind, p *pane.Pane, tileID string) bool {
	eng := pane.TakeOver(k.holders(), p.ID, tileID)
	// close is the one path that persists a freeze, for a surface the rule
	// never allowed and for a different tile in this pane alike.
	for _, id := range eng.Close {
		k.close(id)
	}
	if eng.Keep {
		return false
	}
	k.close(p.ID)
	if eng.From != "" {
		k.move(eng.From, p)
		return false
	}
	return true
}

// handBackSurfaces runs nav.EffHandBackSurfaces: each live surface in the
// level being left moves to its pane.Heir in the parked tree, before the
// level's panes are flushed away and would close it.
func (a *App) handBackSurfaces() {
	top := a.ws.Top()
	if top == nil || top.OuterTree == nil {
		return
	}
	var returning []pane.Holder
	top.OuterTree.Walk(func(p *pane.Pane) {
		if t, ok := a.descendedTile(p); ok {
			returning = append(returning, pane.Holder{PaneID: p.ID, TileID: rpc.ContentID(t)})
		}
	})
	for _, k := range []surfaceKind{a.urlSurface(), a.shellSurface()} {
		for _, h := range k.holders() {
			if a.tree.FindPane(h.PaneID) == nil {
				continue
			}
			if heir := pane.Heir(h.TileID, returning, k.holders()); heir != "" {
				k.move(h.PaneID, top.OuterTree.FindPane(heir))
			}
		}
	}
}
