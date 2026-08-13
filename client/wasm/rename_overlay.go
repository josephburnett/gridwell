//go:build js && wasm

package main

import (
	"context"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// Naming — "name the room you're in" (issues #61, #118, #213). The focused
// pane's name renders as the bottom bar's centered TITLE (bottombar.go),
// not in a pill floating over pane content: the band is carved out of
// every native surface's rect (panebox.BarInset), so the label and the
// inline rename input work identically over canvas, shells, and live url
// panes with no native twin.
// RIGHT-click on the title opens the rename input when the name is
// user-editable (Enter or blur commits the versioned SetTile rename — a
// USER-owned name the automatic captures never overwrite; Escape cancels,
// and an unchanged value never writes);
// read-only contexts (the node grid, a plugin root, an ephemeral visit, a
// text tile's derived name) just show their label. LEFT-click on the title
// toggles the tmux-style pane zoom (Tree.ToggleZoom).
//
// This file owns the DECISIONS: what the name is (bubbleLabel), what it
// edits (renameTarget), the shared inline input, and the one rename door.

// renameTarget returns the tile the focused pane's rename input edits, or
// ok=false when nothing here is renamable:
//   - descended into a url/shell tile → that tile ("rename the tmux pane").
//     Text tiles derive their name from their first line (refused server-side
//     too); ephemeral tiles die on ascent, so naming one is a lie.
//   - inside a well's grid → the CONTAINING well (the last path segment),
//     resolved from the parent grid — renaming the room names its door.
//   - a plugin root / the node grid → nothing (plugin names are config-owned).
func (a *App) renameTarget(p *pane.Pane) (rpc.Tile, bool) {
	if p == nil {
		return rpc.Tile{}, false
	}
	if p.TextFocus != "" {
		t, ok := a.descendedTile(p)
		if !ok || t.Kind == rpc.KindText || a.isEphemeralTile(p, &t) {
			return rpc.Tile{}, false
		}
		return t, true
	}
	if len(p.Path) == 0 {
		return rpc.Tile{}, false
	}
	parentGridID := a.gridIDForPathFrom(p.Anchor, p.Path[:len(p.Path)-1])
	g, ok := a.c.Grid(parentGridID)
	if !ok {
		return rpc.Tile{}, false
	}
	t, ok := g.Tiles[p.Path[len(p.Path)-1]]
	if !ok || !rpc.IsWellKind(t.Kind) {
		return rpc.Tile{}, false
	}
	return t, true
}

// bubbleLabel is what the bar title shows for the focused pane and whether
// it is user-editable: the renameTarget's name when one exists; otherwise a
// read-only context label ("home" on the node grid, the plugin's config
// label at its root, "ephemeral" inside a dying visit, a text tile's derived
// first-line name).
// bubbleDecorate applies pane-state markers to the title text — currently
// the zoom indicator (issue #124). ONE owner: everything that shows the
// name renders bubbleLabel's output.
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
	if p.TextFocus != "" {
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
	if a.isNodeGridPane(p) {
		return "home", false, true
	}
	// A plugin root (or an uncached parent): the plugin's config-owned label.
	want := uuidOf(a.gridIDForPane(p))
	for _, pl := range a.plugins {
		if pl.UUID == want && pl.Label != "" {
			return pl.Label, false, true
		}
	}
	return "unnamed", false, true
}

// togglePaneZoom zooms the focused pane to the full layout, or back —
// left-click on the bar's centered title (issues #118, #213, #220).
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

// openRenameInput lives in bottombar.go (issue #213): the input opens over
// the bar's current crumb, whose geometry the bar owns.

// openNameInputAt spawns the ONE inline rename input — the same DOM shape
// and commit/cancel keys for every rename surface (the current-pane crumb,
// the workspace bar crumb). position sets the placement styles; onCommit
// receives the trimmed value on Enter OR blur (2026-08-13: on a phone the
// keyboard's done key blurs, and "typed a name, tapped elsewhere" must
// not silently discard). Escape cancels; an UNCHANGED value never writes
// (a no-op close must not bump the version — reading never mutates).
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
	go func() {
		tile, err := a.postRename(tileID, alt)
		if err != nil {
			a.reportErr(errsurface.Error, "rename", "rename failed: "+rpcErrText(err))
			return
		}
		if tile != nil {
			a.c.UpdateTile(tile.GridID, *tile)
		}
		a.draw()
	}()
}

// postRename is the one rename door: the versioned SetTile rename arm
// (2026-07-26 redesign — a rename is a real user edit and claims a version).
// The claim comes from the cached row; a conflict retries once with a fresh
// read, since the racing writer of alt is only ever an automatic capture the
// latch will out-rank anyway.
func (a *App) postRename(tileID, alt string) (*rpc.Tile, error) {
	version := int64(0)
	if t := a.cachedTileByID(tileID); t != nil {
		version = t.Version
	}
	tile, err := a.cl.RenameTile(context.Background(), tileID, version, alt)
	if err != nil && isVersionConflict(err) {
		if fresh, gerr := a.cl.GetTile(context.Background(), tileID); gerr == nil {
			tile, err = a.cl.RenameTile(context.Background(), tileID, fresh.Version, alt)
		}
	}
	return tile, err
}

// pxOf formats a float as a CSS pixel length.
func pxOf(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64) + "px"
}
