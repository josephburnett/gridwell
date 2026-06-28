//go:build js && wasm

package main

import (
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/palette"
	"github.com/josephburnett/gridwell/client/pane"
)

// This file exposes a read-only introspection surface, window.__gridwellTest,
// used exclusively by the Electron end-to-end tests (apps/desktop/e2e). It is
// compiled into the normal wasm binary but installs nothing unless the page was
// loaded with ?e2e=1, so production never sees it.
//
// Almost every accessor is a pure READ over state the app already holds,
// reusing the same geometry helpers the input handlers hit-test against
// (plusButtonCenter, paletteTileRect, paneToDragdrop) — it just lets a test
// learn where to click and when the world has settled, while the server's
// GetGrid remains the independent oracle for what was created. The lone
// exception is shellVisitURL, an e2e-only ACTION that fires the exact callback
// xterm's link provider invokes on a click — a terminal-cell link click can't
// be hit-tested from the canvas, so this drives it directly, mirroring the
// desktop's __gwRegistry e2e seam. Both are gated to ?e2e=1, so production
// never sees them.

// installTestHook wires window.__gridwellTest when the page carries ?e2e=1.
func (a *App) installTestHook() {
	search := js.Global().Get("location").Get("search").String()
	if !strings.Contains(search, "e2e=1") {
		return
	}
	js.Global().Set("__gridwellTest", js.ValueOf(map[string]any{
		"idle":       js.FuncOf(a.thIdle),
		"origin":     js.FuncOf(a.thOrigin),
		"panes":      js.FuncOf(a.thPanes),
		"launcher":   js.FuncOf(a.thLauncher),
		"palette":      js.FuncOf(a.thPalette),
		"cellCenter":   js.FuncOf(a.thCellCenter),
		"shellVisitURL": js.FuncOf(a.thShellVisitURL),
	}))
}

// thShellVisitURL fires the focused shell's url-click path (what xterm's link
// provider activate callback does) so an e2e can exercise the shell→ephemeral-
// url descent without hit-testing a terminal cell. e2e-only; mutates state.
func (a *App) thShellVisitURL(_ js.Value, args []js.Value) any {
	if len(args) >= 1 && args[0].Type() == js.TypeString {
		a.shellURLActivate(a.tree.Focus, args[0].String())
	}
	return nil
}

// thIdle reports true when no transition, drag, or fetch is in flight — i.e. the
// descent/ascent animation has finished AND every pending GetGrid/GetTile has
// resolved. Tests poll this instead of sleeping, so they never race the ~350ms
// zoom animation or the async create→fetchGrid refresh.
func (a *App) thIdle(js.Value, []js.Value) any {
	return a.transition == nil &&
		a.dragging == nil &&
		len(a.gridInflight) == 0 &&
		len(a.tileInflight) == 0
}

// thOrigin returns the loopback origin the window is served from, so the test
// can reach the same server's Connect-RPC endpoint (the GetGrid oracle).
func (a *App) thOrigin(js.Value, []js.Value) any {
	return js.Global().Get("location").Get("origin").String()
}

// thPanes returns one descriptor per live pane: its screen rect, whether it is
// focused, and the qualified grid id / anchor / path it currently frames.
func (a *App) thPanes(js.Value, []js.Value) any {
	rects := a.layoutPanes()
	out := make([]any, 0, len(rects))
	for id, r := range rects {
		p := a.tree.FindPane(id)
		if p == nil {
			continue
		}
		out = append(out, map[string]any{
			"id":      id,
			"x":       r.X,
			"y":       r.Y,
			"w":       r.W,
			"h":       r.H,
			"focused": id == a.tree.Focus,
			"gridID":  a.gridIDForPane(p),
			"anchor":  p.Anchor,
			"path":    stringsToAny(p.Path),
			// The tile this pane is descended into ("" when on a grid) — lets a
			// test tell a shell descent from the url it descended further into.
			"textFocus": p.TextFocus,
			// Viewport center (grid cell coords) + zoom, so a test can drop on a
			// cell guaranteed to be on-screen regardless of the stored framing.
			"cx":   p.Cx,
			"cy":   p.Cy,
			"zoom": p.Zoom,
		})
	}
	return out
}

// thLauncher returns the launcher plugin tiles for the focused pane (the start
// screen has no grid and no + menu — the plugin tiles are the whole page). Each
// entry carries the plugin identity plus the screen-space center to click to
// enter it. Empty when the focused pane has already entered a plugin.
func (a *App) thLauncher(js.Value, []js.Value) any {
	p, r, ok := a.focusedPaneRect()
	if !ok || !isLauncherPane(p) {
		return []any{}
	}
	ps := paneToDragdrop(p, r)
	n := len(a.plugins)
	out := make([]any, 0, n)
	for i, pl := range a.plugins {
		cr := palette.LauncherCellRect(i, n)
		// Center of the launcher cell, in screen pixels.
		sx, sy := ps.CellToScreen(cr.X+cr.W/2, cr.Y+cr.H/2)
		out = append(out, map[string]any{
			"index":      i,
			"kind":       pl.Kind,
			"label":      pl.Label,
			"uuid":          pl.UUID,
			"rootGridID":    pl.RootGridID,
			"scratchGridID": pl.ScratchGridID,
			"x":             sx,
			"y":             sy,
		})
	}
	return out
}

// thPalette returns the creation palette for the focused pane: whether it is
// open, the + button center, and one entry per swatch with its screen rect and
// identity. The rects are the SAME ones paletteTileIndexAt hit-tests, so a click
// at an entry's center lands on that swatch.
func (a *App) thPalette(js.Value, []js.Value) any {
	p, r, ok := a.focusedPaneRect()
	if !ok {
		return map[string]any{"open": false}
	}
	px, py := plusButtonCenter(p, r)
	items := a.paletteItems(p)
	entries := make([]any, 0, len(items))
	for i, item := range items {
		tx, ty, tw, th := a.paletteTileRect(p, r, i)
		e := map[string]any{
			"index":    i,
			"isPlugin": item.isPlugin,
			"x":        tx,
			"y":        ty,
			"w":        tw,
			"h":        th,
		}
		if item.isPlugin {
			e["kind"] = item.plugin.Kind
			e["label"] = item.plugin.Label
			e["uuid"] = item.plugin.UUID
		} else {
			e["kind"] = templateKindName(item.primitive)
		}
		entries = append(entries, e)
	}
	return map[string]any{
		"open":  a.menu.OpenOn(p.ID),
		"plusX": px,
		"plusY": py,
		"items": entries,
	}
}

// thCellCenter maps a grid cell (cx, cy) in the named pane to its screen-space
// center, so a test can target a drop on an exact cell. args: paneID, cx, cy.
func (a *App) thCellCenter(_ js.Value, args []js.Value) any {
	if len(args) < 3 {
		return nil
	}
	p := a.tree.FindPane(args[0].String())
	rects := a.layoutPanes()
	r, ok := rects[args[0].String()]
	if p == nil || !ok {
		return nil
	}
	cx, cy := args[1].Float(), args[2].Float()
	sx, sy := paneToDragdrop(p, r).CellToScreen(cx+0.5, cy+0.5)
	return map[string]any{"x": sx, "y": sy}
}

// focusedPaneRect returns the focused pane and its laid-out screen rect.
func (a *App) focusedPaneRect() (*pane.Pane, pane.Rect, bool) {
	p := a.tree.FindPane(a.tree.Focus)
	if p == nil {
		return nil, pane.Rect{}, false
	}
	r, ok := a.layoutPanes()[a.tree.Focus]
	return p, r, ok
}

// templateKindName is the stable string a test uses to pick a primitive swatch.
func templateKindName(k templateKind) string {
	switch k {
	case tplWell:
		return "well"
	case tplMarkdown:
		return "markdown"
	case tplURL:
		return "url"
	case tplShell:
		return "shell"
	}
	return ""
}

// stringsToAny lifts a []string to the []any js.ValueOf needs for a JS array.
func stringsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
