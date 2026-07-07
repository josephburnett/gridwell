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

// The name bubble — "name the room you're in" (issues #61, #118). The
// focused pane always carries a small pill at its top-center naming whatever
// is in the pane at that moment. LEFT-click opens the rename input when the
// name is user-editable (Enter commits via SetTileAlt — a USER-owned name the
// automatic captures never overwrite; Escape/blur cancels); read-only
// contexts (the node grid, a plugin root, an ephemeral visit, a text tile's
// derived name) just show their label. RIGHT-click toggles the tmux-style
// pane zoom (Tree.ToggleZoom) — the bubble is the pane's one universal
// handle, replacing the old double-right-click gesture.
//
// A DOM element (not canvas chrome) so it paints above the xterm shell
// overlay (zIndex 5) the same way the shell ascend circle does. Over a LIVE
// url pane the native WebContentsView occludes DOM — those panes get a
// native twin (webviews.ts namePill) that forwards its clicks here.

// renameTarget returns the tile the focused pane's rename pill edits, or
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

// bubbleLabel is what the focused pane's bubble shows and whether it is
// user-editable: the renameTarget's name when one exists; otherwise a
// read-only context label ("home" on the node grid, the plugin's config
// label at its root, "ephemeral" inside a dying visit, a text tile's derived
// first-line name).
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

// syncRenamePill positions the bubble each draw, mirroring how the shell
// overlay and textarea track their pane. Hidden while an overlay gesture is
// in flight, while the editor input is open (it replaces the pill in place),
// and on live-url panes (the native twin shows there instead — DOM cannot
// paint above a WebContentsView).
func (a *App) syncRenamePill() {
	pill := a.renamePillEl()
	if a.renameEditing {
		pill.Get("style").Set("display", "none")
		return
	}
	p := a.tree.FocusedPane()
	if p == nil || a.transition != nil || a.liveOverlaysHidden() {
		pill.Get("style").Set("display", "none")
		return
	}
	if a.urlViewFor(p.ID) != nil {
		// Live url pane: the NATIVE pill twin shows instead (DOM cannot paint
		// above a WebContentsView). Push the label when it changes.
		pill.Get("style").Set("display", "none")
		label, _, _ := a.bubbleLabel(p)
		if a.lastNativePill != p.ID+"\x00"+label {
			a.lastNativePill = p.ID + "\x00" + label
			bridgeSetNameLabel(p.ID, label)
		}
		return
	}
	label, editable, muted := a.bubbleLabel(p)
	pill.Set("textContent", label)
	color := colorPlusFg
	if muted {
		color = colorMuted
	}
	st := pill.Get("style")
	st.Set("color", color)
	cursor := "pointer"
	if !editable {
		cursor = "default"
	}
	st.Set("cursor", cursor)
	st.Set("display", "block")
	r := a.paneRectByID(p.ID)
	st.Set("left", pxOf(r.X+r.W/2))
	st.Set("top", pxOf(r.Y+paneBorderPx+4))
}

// renamePillEl lazily creates the pill element (one shared node, like the
// text toggle button).
func (a *App) renamePillEl() js.Value {
	if a.renamePill.Truthy() {
		return a.renamePill
	}
	doc := js.Global().Get("document")
	pill := doc.Call("createElement", "div")
	pill.Set("id", "gw-rename-pill")
	st := pill.Get("style")
	st.Set("position", "absolute")
	st.Set("transform", "translateX(-50%)")
	st.Set("zIndex", "7") // above the xterm overlay (5) and its circle (6)
	st.Set("display", "none")
	st.Set("background", colorPlusBg)
	st.Set("border", "1px solid "+colorPaneBorder)
	st.Set("borderRadius", "10px")
	st.Set("padding", "1px 10px")
	st.Set("font", "12px sans-serif")
	st.Set("color", colorPlusFg)
	st.Set("cursor", "pointer")
	st.Set("maxWidth", "40%")
	st.Set("overflow", "hidden")
	st.Set("textOverflow", "ellipsis")
	st.Set("whiteSpace", "nowrap")
	a.renamePillClickCb = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		if ev.Get("type").String() == "contextmenu" {
			ev.Call("preventDefault")
			return nil
		}
		ev.Call("stopPropagation")
		switch ev.Get("button").Int() {
		case 0:
			a.openRenameInput()
		case 2:
			// The bubble is the pane's universal handle: right-click toggles
			// the tmux-style pane zoom (issue #118, replacing the old
			// double-right-click gesture).
			ev.Call("preventDefault")
			a.togglePaneZoom()
		}
		return nil
	})
	pill.Call("addEventListener", "mousedown", a.renamePillClickCb)
	pill.Call("addEventListener", "contextmenu", a.renamePillClickCb)
	doc.Get("body").Call("appendChild", pill)
	a.renamePill = pill
	return pill
}

// onNativeNameClick routes a click on the NATIVE bubble twin (issue #118):
// same contract as the DOM pill's listener.
func (a *App) onNativeNameClick(paneID string, button int) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	a.focusToPane(p)
	switch button {
	case 0:
		a.openRenameInput() // renameEditing parks the view; the DOM input shows
	case 2:
		a.togglePaneZoom()
	}
}

// togglePaneZoom zooms the focused pane to the full layout, or back —
// right-click on the bubble (issue #118).
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

// openRenameInput swaps the pill for a text input at the same spot. Enter
// commits, Escape or blur cancels. A no-op on read-only contexts.
func (a *App) openRenameInput() {
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	target, ok := a.renameTarget(p)
	if !ok {
		return
	}
	doc := js.Global().Get("document")
	in := doc.Call("createElement", "input")
	in.Set("id", "gw-rename-input")
	in.Set("value", target.AltText)
	st := in.Get("style")
	st.Set("position", "absolute")
	st.Set("transform", "translateX(-50%)")
	st.Set("zIndex", "8")
	st.Set("background", colorMenuBg)
	st.Set("border", "1px solid "+colorFocusBorder)
	st.Set("borderRadius", "10px")
	st.Set("padding", "1px 10px")
	st.Set("font", "12px sans-serif")
	st.Set("color", colorMenuItemHi)
	st.Set("outline", "none")
	st.Set("width", "180px")
	r := a.paneRectByID(p.ID)
	st.Set("left", pxOf(r.X+r.W/2))
	st.Set("top", pxOf(r.Y+paneBorderPx+3))
	a.renameEditing = true
	tileID := target.ID

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
		if commit {
			a.commitRename(tileID, val)
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
		closeInput(false)
		return nil
	})
	in.Call("addEventListener", "keydown", keyCb)
	in.Call("addEventListener", "blur", blurCb)
	doc.Get("body").Call("appendChild", in)
	in.Call("focus")
	in.Call("select")
	a.draw() // hides the pill while editing
}

// commitRename posts the user-owned name and patches the cache so the pill
// (and any banner) reflects it immediately; the TileChanged event confirms.
func (a *App) commitRename(tileID, alt string) {
	go func() {
		tile, err := a.cl.SetTileAlt(context.Background(), tileID, alt)
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

// pxOf formats a float as a CSS pixel length.
func pxOf(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64) + "px"
}
