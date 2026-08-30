//go:build js && wasm

package main

import (
	"context"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/pane"
)

// Naming: name the room you are in. The focused pane's name renders as the
// bottom bar's centered title (bottombar.go), not in a pill floating over
// pane content. The band is carved out of every native surface's rect
// (panebox.BarInset), so the label and the inline rename input work
// identically over canvas, shells, and live url panes, with no native twin.
//
// A right-click on the title opens the rename input when the name is
// user-editable: Enter or blur commits the versioned SetTile rename, a
// user-owned name the automatic captures never overwrite; Escape cancels; and
// an unchanged value never writes. Read-only contexts — a plugin root, an
// ephemeral visit, a text tile's derived name — just show their label. A
// left-click on the title toggles the tmux-style pane zoom
// (Tree.ToggleZoom).
//
// This file owns the decisions: what the name is (bubbleLabel), what it edits
// (renameTarget), the shared inline input, and the one rename door.

// renameTarget returns the tile the focused pane's rename input edits, or
// ok=false when nothing here is renamable:
//   - descended into a url or shell tile: that tile. Text tiles derive their
//     name from their first line, which the server refuses to override, and
//     ephemeral tiles die on ascent, so naming one is a lie.
//   - inside a well's grid: the containing well (the last path segment),
//     resolved from the parent grid — renaming the room names its door.
//   - a plugin root: nothing, since plugin names are config-owned.
func (a *App) renameTarget(p *pane.Pane) (rpc.Tile, bool) {
	if p == nil {
		return rpc.Tile{}, false
	}
	if p.ContentID() != "" {
		t, ok := a.descendedTile(p)
		if !ok || t.Kind == rpc.KindText || a.isEphemeralTile(p, &t) {
			return rpc.Tile{}, false
		}
		return t, true
	}
	if len(p.Path()) == 0 {
		// A namespace level (a connection, a linked world): the door tile,
		// the well descended through, is a real row, and renaming the room
		// names its door, exactly like the containing-well arm below.
		// Declarations — plugin roots, menu entries — stay unrenamable.
		if t, kind := a.doorFind(p); kind == door.Well {
			return t, true
		}
		return rpc.Tile{}, false
	}
	parentGridID := a.gridIDForPathFrom(p.Anchor(), p.Path()[:len(p.Path())-1])
	g, ok := a.c.Grid(parentGridID)
	if !ok {
		return rpc.Tile{}, false
	}
	t, ok := g.Tiles[p.Path()[len(p.Path())-1]]
	if !ok || !rpc.IsWellKind(t.Kind) {
		return rpc.Tile{}, false
	}
	return t, true
}

// bubbleLabel is what the bar title shows for the focused pane and whether
// it is user-editable: the renameTarget's name when one exists; otherwise a
// read-only context label (the plugin's config label at its root,
// "ephemeral" inside a dying visit, a text tile's derived first-line name).
// bubbleDecorate applies pane-state markers to the title text, currently the
// zoom indicator. One owner: everything that shows the name renders
// bubbleLabel's output.
func (a *App) bubbleDecorate(p *pane.Pane, label string) string {
	if a.tree.Zoomed == p.ID {
		return "⛶ " + label
	}
	return label
}

func (a *App) bubbleLabel(p *pane.Pane) (label string, editable, muted bool) {
	if t, ok := a.renameTarget(p); ok {
		if t.AltText == "" {
			return "unnamed", true, true
		}
		return t.AltText, true, false
	}
	if p.ContentID() != "" {
		if t, ok := a.descendedTile(p); ok {
			if a.isEphemeralTile(p, &t) {
				return "ephemeral", false, true
			}
			if t.AltText != "" {
				return t.AltText, false, false // text tiles: derived, read-only
			}
		}
		return "unnamed", false, true
	}
	// A namespace level: the door's identity — the entry's or the plugin's
	// declared label, read-only. A renamable door was already answered by
	// the renameTarget arm above.
	if len(p.Path()) == 0 {
		if t, kind := a.doorFind(p); kind != door.None {
			if t.AltText == "" {
				return "unnamed", false, true
			}
			return t.AltText, false, true
		}
	}
	// An uncached parent: the plugin's config-owned label.
	want := uuidOf(a.gridIDForPane(p))
	for _, pl := range a.allPlugins() {
		if pl.UUID == want && pl.Label != "" {
			return pl.Label, false, true
		}
	}
	return "unnamed", false, true
}

// doorFind resolves the tile the focused pane's current level was entered
// through (the DOOR — client/door), assembling the inputs from the caches:
// the level below's grid when there is one, and the plugin declarations.
func (a *App) doorFind(p *pane.Pane) (rpc.Tile, door.Kind) {
	var parent map[string]rpc.Tile
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

// togglePaneZoom zooms the focused pane to the full layout, or back: the
// left-click on the bar's centered title.
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

// openRenameInput lives in bottombar.go: the input opens over the bar's
// current crumb, whose geometry the bar owns.
//
// openNameInputAt spawns the one inline rename input — the same DOM shape and
// commit and cancel keys for every rename surface (the current-pane crumb,
// the pane-tile bar crumb). position sets the placement styles; onCommit
// receives the trimmed value on Enter or on blur, because on a phone the
// keyboard's done key blurs and "typed a name, tapped elsewhere" must not
// silently discard. Escape cancels, and an unchanged value never writes: a
// no-op close must not bump the version, because reading never mutates.
func (a *App) openNameInputAt(value string, width float64, position func(st js.Value), onCommit func(string)) {
	doc := js.Global().Get("document")
	in := doc.Call("createElement", "input")
	in.Set("id", "gw-rename-input")
	in.Set("value", value)
	st := in.Get("style")
	st.Set("position", "absolute")
	st.Set("zIndex", "8")
	st.Set("background", colorMenuBg)
	st.Set("border", "1px solid "+colorFocusBorder)
	st.Set("borderRadius", "10px")
	st.Set("padding", "1px 10px")
	st.Set("font", "12px sans-serif")
	st.Set("color", colorMenuItemHi)
	st.Set("outline", "none")
	st.Set("width", pxOf(width))
	position(st)
	a.renameEditing = true

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
		a.renameEditing = false
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
		closeInput(true) // blur commits — see the doc comment
		return nil
	})
	in.Call("addEventListener", "keydown", keyCb)
	in.Call("addEventListener", "blur", blurCb)
	doc.Get("body").Call("appendChild", in)
	in.Call("focus")
	in.Call("select")
	a.draw() // hides the title while editing
}

// commitRename posts the user-owned name and patches the cache so the pill
// (and any banner) reflects it immediately; the TileChanged event confirms.
func (a *App) commitRename(tileID, alt string) {
	a.commitRenameRetained(tileID, alt, func(t *rpc.Tile) {
		a.c.UpdateTile(t.GridID, *t)
	})
}

// commitRenameRetained is the one rename commit: the versioned rename, then
// `apply` on success. The typed name is retained on a transport failure: the
// input element is gone by the time the RPC fails, so the closure parked in
// the outbox is the only copy of what the user typed, and it lands on the
// retry kick. A server verdict surfaces and stands.
func (a *App) commitRenameRetained(tileID, alt string, apply func(*rpc.Tile)) {
	var tile *rpc.Tile
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

// postRename is the one rename door: the versioned SetTile rename arm. A name
// the user types is a content edit, so it claims a version and bumps one, and
// the claim comes from the cached row.
//
// A conflict surfaces rather than re-claiming. Captures do not bump the row,
// so a conflict here means a genuine concurrent content edit, and silently
// re-claiming over it would overwrite it.
func (a *App) postRename(ctx context.Context, tileID, alt string) (*rpc.Tile, error) {
	version := int64(0)
	if t := a.cachedTileByID(tileID); t != nil {
		version = t.Version
	}
	return a.cl.RenameTile(ctx, tileID, version, alt)
}

// gridIDOfTile answers "which grid's cache reconciles if this write is
// refused" for a call site that holds only an id. "" when the row is in no
// cached grid, which makes the dispatcher's refetch a no-op: a grid this
// client never loaded has nothing to reconcile.
func (a *App) gridIDOfTile(tileID string) string {
	if t := a.cachedTileByID(tileID); t != nil {
		return t.GridID
	}
	return ""
}

// pxOf formats a float as a CSS pixel length.
func pxOf(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64) + "px"
}
