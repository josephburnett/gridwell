//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/bartitle"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/contentrow"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/outbox"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// Naming: name the grid you are in. The focused pane's name renders as the
// bottom bar's centered title, in a band that is reserved layout below every
// pane, so the label and the inline rename input work identically over
// canvas, shells and live url panes. An unchanged value never writes, because
// reading never mutates.

// barTitle is the one answer to what the bar's centered title says and which
// row a right-click there renames; bartitle.Decide owns the choice and this
// gathers its inputs from the caches. The door lookup kicks a grid fetch and
// the declared-label scan walks every declaration, so each is resolved only on
// the arm that reads it.
func (a *App) barTitle(p *pane.Pane) (bartitle.Verdict, *gridwellv1.Tile) {
	if p == nil {
		return bartitle.Verdict{}, nil
	}
	in := bartitle.Input{Descent: p.ContentID() != "", AtAnchor: len(p.Path()) == 0}
	var descended, entered, parent *gridwellv1.Tile
	if t, ok := a.descendedTile(p); ok {
		descended = t
		in.Descended, in.DescendedName = true, t.AltText
		in.DescendedText = !rpc.KindOf(t.Kind).TypedName
		if row, s := a.askContentRow(t); s == contentrow.Ready {
			in.DescendedReadOnly = row.ReadOnly
		}
		in.CertainlyEphemeral = a.certainlyEphemeral(p, t)
	}
	if !in.Descent {
		if in.AtAnchor {
			entered, in.Door = a.doorFind(p)
			if entered != nil {
				in.DoorName = entered.AltText
			}
		} else if t, ok := a.parentDoor(p); ok {
			parent, in.Parent, in.ParentName = t, true, t.AltText
		}
		in.CertainlyEphemeral = a.enteredVisit(p) != nil
		in.ConfigLabel = a.declaredLabel(p)
		in.DirectoryPath, _ = rpc.FilePath(a.gridIDForPane(p))
	}
	v := bartitle.Decide(in)
	switch v.Rename {
	case bartitle.RenameDescent:
		return v, descended
	case bartitle.RenameDoor:
		return v, entered
	case bartitle.RenameParent:
		return v, parent
	}
	return v, nil
}

// parentDoor is the door row at the tail of the pane's path, from the cache.
func (a *App) parentDoor(p *pane.Pane) (*gridwellv1.Tile, bool) {
	g, ok := a.c.Grid(a.gridIDForPathFrom(p.Anchor(), p.Path()[:len(p.Path())-1]))
	if !ok {
		return nil, false
	}
	t, ok := g.Tiles[p.Path()[len(p.Path())-1]]
	if !ok || !rpc.IsDoorKind(t.Kind) {
		return nil, false
	}
	return t, true
}

// declaredLabel is the roster row's label for the pane's grid.
func (a *App) declaredLabel(p *pane.Pane) string {
	return door.RowLabel(a.gridIDForPane(p), a.allSources())
}

// bubbleDecorate applies pane-state markers to the title text.
func (a *App) bubbleDecorate(p *pane.Pane, label string) string {
	if a.tree.Zoomed == p.ID {
		return "⛶ " + label
	}
	return label
}

// doorFind resolves the tile the pane's current level was entered through,
// assembling client/door's inputs from the caches.
func (a *App) doorFind(p *pane.Pane) (*gridwellv1.Tile, door.Kind) {
	var parent map[string]*gridwellv1.Tile
	if p.Depth() > 1 {
		anchor, path := p.AnchorPathAt(p.Depth() - 2)
		if gid := a.gridIDForPathFrom(anchor, path); gid != "" {
			if g, ok := a.c.Grid(gid); ok {
				parent = g.Tiles
			} else {
				a.fetchGrid(gid)
			}
		}
	}
	return door.Find(p.Anchor(), parent, a.allSources())
}

// togglePaneZoom is the left-click on the bar's centered title.
func (a *App) togglePaneZoom() {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	a.menu.Close()
	a.tree.ToggleZoom(p.ID)
	a.emit(traceevent.PaneZoom(p.ID, a.tree.Zoomed == p.ID))
	a.draw()
	a.scheduleURLUpdate()
}

// renameInput is the open inline rename: its element, and end, which closes
// it, committing a changed value when commit is true.
type renameInput struct {
	el  js.Value
	end func(commit bool)
}

// openNameInputAt spawns the one inline rename input, the same DOM shape and
// keys for every rename surface. onCommit receives the trimmed value on Enter
// or on blur, because a phone keyboard's done key blurs and a typed name must
// not be silently discarded. An unchanged value never writes, because reading
// never mutates; onCancel, when set, hears every close that writes nothing.
func (a *App) openNameInputAt(value string, width float64, position func(st js.Value), onCommit func(string), onCancel func()) {
	in := a.element("input", "gw-rename-input", map[string]string{"position": "absolute", "zIndex": "8",
		"background": a.pal.MenuBg, "border": "1px solid " + a.pal.FocusBorder, "borderRadius": "10px",
		"padding": "1px 10px", "font": "12px sans-serif", "color": a.pal.MenuItemHi, "outline": "none",
		"width": pxf(width)})
	in.Set("value", value)
	position(in.Get("style"))

	orig := strings.TrimSpace(value)
	closed := false
	var offKey, offBlur func()
	closeInput := func(commit bool) {
		if closed {
			return
		}
		closed = true
		val := strings.TrimSpace(in.Get("value").String())
		in.Call("remove")
		offKey()
		offBlur()
		a.overlays.rename = nil
		if commit && val != orig {
			onCommit(val)
		} else if onCancel != nil {
			onCancel()
		}
		a.draw()
	}
	offKey = listen(in, "keydown", func(ev js.Value) {
		ev.Call("stopPropagation")
		switch ev.Get("key").String() {
		case "Enter":
			closeInput(true)
		case "Escape":
			closeInput(false)
		}
	})
	offBlur = listen(in, "blur", func(js.Value) {
		closeInput(true) // blur commits; see the doc comment
	})
	a.doc.Get("body").Call("appendChild", in)
	a.overlays.rename = &renameInput{el: in, end: closeInput}
	a.draw() // hides the title while editing, and gives the input the keyboard
	in.Call("select")
}

// openRenameAt is openNameInputAt for a rename of tileID: the claim opens
// with the input, on the version the user sees, and closes with it unless a
// rename takes it.
func (a *App) openRenameAt(tileID string, saw int64, value string, width float64, position func(st js.Value),
	commit func(cl *outbox.Claim, alt string)) {
	claims := a.persist.out.Claims
	cl := claims.Open(tileID, saw)
	a.openNameInputAt(value, width, position, func(val string) { commit(cl, val) }, func() { claims.Close(cl) })
}

// commitRename is the one rename commit: it posts the user-owned name and
// runs apply on success, which hears the version the rename claimed. The
// input element is gone by the time an RPC fails, so the closure parked in
// the outbox is the only copy of what the user typed. A name is content, so
// it claims and bumps a version: cl, read at each send, moves only by this
// client's own landings (outbox.Claims), so a name another writer set since
// the input opened is refused. It rides the row's chain behind this client's
// other writes to it, and closes cl once nothing is owed. apply may be nil.
func (a *App) commitRename(cl *outbox.Claim, tileID, alt string, apply func(t *gridwellv1.Tile, claimed int64)) {
	var tile *gridwellv1.Tile
	var claimed int64
	claims := a.persist.out.Claims
	key := outbox.Key{Op: "Rename", ID: tileID}
	a.post(write{
		label: key.Op, gid: a.gridIDOfTile(tileID), id: tileID,
		source: "rename", failText: "rename", chain: tileID,
		call: func(ctx context.Context) error {
			var err error
			claimed = claims.Version(cl)
			tile, err = a.cl.RenameTile(ctx, tileID, claimed, alt)
			return err
		},
		then: func() {
			if tile != nil {
				claims.Landed(tileID, claimed, tile.Version)
				if apply != nil {
					apply(tile, claimed)
				}
				a.c.PutWriteResponse(tile.GridId, tile, cache.WroteName)
			}
			a.draw()
		},
		done: func() {
			if !a.persist.out.Has(key) {
				claims.Close(cl)
			}
		},
	})
}

// gridIDOfTile is which grid's cache reconciles if a write is refused, for a
// call site that holds only an id. "" makes the dispatcher's refetch a no-op,
// since a grid this client never loaded has nothing to reconcile.
func (a *App) gridIDOfTile(tileID string) string {
	if t := a.cachedTileByID(tileID); t != nil {
		return t.GridId
	}
	return ""
}
