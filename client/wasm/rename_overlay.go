//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/bartitle"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/pane"
)

// Naming: name the room you are in. The focused pane's name renders as the
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
		in.DescendedText = t.Kind == rpc.KindText
		in.PossiblyEphemeral = a.possiblyEphemeral(p, t)
		in.CertainlyEphemeral = a.certainlyEphemeral(p, t)
	}
	if !in.Descent {
		if in.AtAnchor {
			entered, in.Door = a.doorFind(p)
			if entered != nil {
				in.DoorName = entered.AltText
			}
		} else if t, ok := a.parentWell(p); ok {
			parent, in.Parent, in.ParentName = t, true, t.AltText
		}
		in.ConfigLabel = a.declaredLabel(p)
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

// parentWell is the well row at the tail of the pane's path, from the cache.
func (a *App) parentWell(p *pane.Pane) (*gridwellv1.Tile, bool) {
	g, ok := a.c.Grid(a.gridIDForPathFrom(p.Anchor(), p.Path()[:len(p.Path())-1]))
	if !ok {
		return nil, false
	}
	t, ok := g.Tiles[p.Path()[len(p.Path())-1]]
	if !ok || !rpc.IsWellKind(t.Kind) {
		return nil, false
	}
	return t, true
}

// declaredLabel is the plugin declaration's label for the pane's grid.
func (a *App) declaredLabel(p *pane.Pane) string {
	want := uuidOf(a.gridIDForPane(p))
	for _, pl := range a.allPlugins() {
		if pl.Uuid == want && pl.Label != "" {
			return pl.Label
		}
	}
	return ""
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
	return door.Find(p.Anchor(), parent, a.allPlugins())
}

// togglePaneZoom is the left-click on the bar's centered title.
func (a *App) togglePaneZoom() {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	a.menu.Close()
	a.tree.ToggleZoom(p.ID)
	a.draw()
	a.scheduleURLUpdate()
}

// openNameInputAt spawns the one inline rename input, the same DOM shape and
// keys for every rename surface. onCommit receives the trimmed value on Enter
// or on blur, because a phone keyboard's done key blurs and a typed name must
// not be silently discarded. An unchanged value never writes, because reading
// never mutates.
func (a *App) openNameInputAt(value string, width float64, position func(st js.Value), onCommit func(string)) {
	doc := js.Global().Get("document")
	in := doc.Call("createElement", "input")
	in.Set("id", "gw-rename-input")
	in.Set("value", value)
	st := in.Get("style")
	st.Set("position", "absolute")
	st.Set("zIndex", "8")
	st.Set("background", a.pal.MenuBg)
	st.Set("border", "1px solid "+a.pal.FocusBorder)
	st.Set("borderRadius", "10px")
	st.Set("padding", "1px 10px")
	st.Set("font", "12px sans-serif")
	st.Set("color", a.pal.MenuItemHi)
	st.Set("outline", "none")
	st.Set("width", pxf(width))
	position(st)
	a.overlays.renameEditing = true

	orig := strings.TrimSpace(value)
	closed := false
	var keyCb, blurCb js.Func
	closeInput := func(commit bool) {
		if closed {
			return
		}
		closed = true
		val := strings.TrimSpace(in.Get("value").String())
		in.Call("remove")
		keyCb.Release()
		blurCb.Release()
		a.overlays.renameEditing = false
		if commit && val != orig {
			onCommit(val)
		}
		a.draw()
	}
	keyCb = js.FuncOf(func(_ js.Value, args []js.Value) any {
		ev := args[0]
		ev.Call("stopPropagation")
		switch ev.Get("key").String() {
		case "Enter":
			closeInput(true)
		case "Escape":
			closeInput(false)
		}
		return nil
	})
	blurCb = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		closeInput(true) // blur commits; see the doc comment
		return nil
	})
	in.Call("addEventListener", "keydown", keyCb)
	in.Call("addEventListener", "blur", blurCb)
	doc.Get("body").Call("appendChild", in)
	in.Call("focus")
	in.Call("select")
	a.draw() // hides the title while editing
}

// commitRename posts the user-owned name and patches the cache so the title
// reflects it immediately. The TileChanged event confirms.
func (a *App) commitRename(tileID, alt string) {
	a.commitRenameRetained(tileID, alt, func(t *gridwellv1.Tile) {
		a.c.UpdateTile(t.GridId, t)
	})
}

// commitRenameRetained is the one rename commit, running `apply` on success.
// The input element is gone by the time an RPC fails, so the closure parked
// in the outbox is the only copy of what the user typed.
func (a *App) commitRenameRetained(tileID, alt string, apply func(*gridwellv1.Tile)) {
	var tile *gridwellv1.Tile
	a.post(write{
		label: "Rename", gid: a.gridIDOfTile(tileID), id: tileID,
		source: "rename", failText: "rename",
		call: func(ctx context.Context) error {
			var err error
			tile, err = a.postRename(ctx, tileID, alt)
			return err
		},
		then: func() {
			if tile != nil {
				apply(tile)
			}
			a.draw()
		},
	})
}

// postRename is the one rename door. A name the user types is a content
// edit, so it claims a version and bumps one. A conflict surfaces rather than
// re-claiming: captures do not bump the row, so a conflict here is a genuine
// concurrent edit.
func (a *App) postRename(ctx context.Context, tileID, alt string) (*gridwellv1.Tile, error) {
	version := int64(0)
	if t := a.cachedTileByID(tileID); t != nil {
		version = t.Version
	}
	return a.cl.RenameTile(ctx, tileID, version, alt)
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
