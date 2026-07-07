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

// The rename pill — "name the room you're in" (issue #61). While the focused
// pane is descended into something renamable, a small DOM pill floats at the
// pane's top-center showing its current name; clicking it swaps in a text
// input, Enter commits via SetTileAlt (a USER-owned name: the server latches
// it so automatic captures — a url's page title, a shell's foreground command
// — never overwrite it), Escape/blur cancels.
//
// A DOM element (not canvas chrome) so it paints above the xterm shell
// overlay (zIndex 5) the same way the shell ascend circle does. Over a LIVE
// url pane the native WebContentsView still occludes it — rename a url while
// frozen (the freeze's title capture defers to the user name, so it sticks).

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

// syncRenamePill positions/hides the pill each draw, mirroring how the shell
// overlay and textarea track their pane. Hidden while an overlay gesture is
// in flight (parked overlays mean the user is mid-drag) and while the editor
// input is open (the input replaces the pill in place).
func (a *App) syncRenamePill() {
	pill := a.renamePillEl()
	if a.renameEditing {
		pill.Get("style").Set("display", "none")
		return
	}
	p := a.tree.FocusedPane()
	target, ok := rpc.Tile{}, false
	if p != nil && a.transition == nil && !a.liveOverlaysHidden() {
		target, ok = a.renameTarget(p)
	}
	if !ok {
		pill.Get("style").Set("display", "none")
		return
	}
	label := target.AltText
	muted := false
	if label == "" {
		label, muted = "unnamed", true
	}
	pill.Set("textContent", label)
	color := colorPlusFg
	if muted {
		color = colorMuted
	}
	st := pill.Get("style")
	st.Set("color", color)
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
		if len(args) > 0 {
			args[0].Call("stopPropagation")
		}
		a.openRenameInput()
		return nil
	})
	pill.Call("addEventListener", "mousedown", a.renamePillClickCb)
	doc.Get("body").Call("appendChild", pill)
	a.renamePill = pill
	return pill
}

// openRenameInput swaps the pill for a text input at the same spot. Enter
// commits, Escape or blur cancels.
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
