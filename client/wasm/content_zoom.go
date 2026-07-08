//go:build js && wasm

package main

import (
	"context"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// Content zoom (issue #82): Ctrl/Cmd +/‑/0 while descended into a text,
// shell, or url tile scales the CONTENT — the text font, the terminal font,
// the page zoom. The zoom is per-tile FRAMING: server-owned (content_zoom,
// written via SetContentZoom, never bumps version) and restored on every
// descent, so a zoomed doc reads at your size on every return — face #3 of
// the guiding rule, one more field riding the same machinery.

const (
	contentZoomStep = 1.1
	contentZoomMin  = 0.5
	contentZoomMax  = 3.0
	// shellBaseFontPx is the terminal font at zoom 1.0.
	shellBaseFontPx = 13.0
)

// contentZoomOf reads a tile's zoom with the unset (0) default of 1.0.
func contentZoomOf(t *rpc.Tile) float64 {
	if t != nil && t.ContentZoom > 0 {
		return t.ContentZoom
	}
	return 1.0
}

func clampContentZoom(z float64) float64 {
	if z < contentZoomMin {
		return contentZoomMin
	}
	if z > contentZoomMax {
		return contentZoomMax
	}
	return z
}

// textScaleFor is the render transform for a descended text pane: the fixed
// base scale times the tile's content zoom. The painter, the caret hit-test,
// the logical wrap width, and the textarea box all derive from this ONE
// helper, so they can never disagree about how big the text is.
func (a *App) textScaleFor(p *pane.Pane) float64 {
	if t, ok := a.descendedTile(p); ok {
		return textFixedScale * contentZoomOf(&t)
	}
	return textFixedScale
}

// handleContentZoomKey consumes a Ctrl/Cmd +/-/0 chord for the focused
// descended pane. Returns true when the key was handled (caller stops).
func (a *App) handleContentZoomKey(ev js.Value) bool {
	if !(ev.Get("ctrlKey").Bool() || ev.Get("metaKey").Bool()) {
		return false
	}
	var next func(cur float64) float64
	switch ev.Get("key").String() {
	case "+", "=":
		next = func(c float64) float64 { return clampContentZoom(c * contentZoomStep) }
	case "-":
		next = func(c float64) float64 { return clampContentZoom(c / contentZoomStep) }
	case "0":
		next = func(float64) float64 { return 1.0 }
	default:
		return false
	}
	p := a.tree.FocusedPane()
	if p == nil || p.TextFocus == "" {
		return false
	}
	// Content zoom applies exactly to the content-descent kinds — the set has
	// one owner (it drifted once and dropped shell descents; see its comment).
	t, ok := a.descendedTile(p)
	if !ok || !rpc.IsContentDescentKind(t.Kind) {
		return false
	}
	ev.Call("preventDefault")
	a.applyContentZoom(p, &t, next(contentZoomOf(&t)))
	return true
}

// applyContentZoom updates the cache (the one client copy every reader uses),
// pokes the live surface for the kinds that hold native state, and persists.
func (a *App) applyContentZoom(p *pane.Pane, t *rpc.Tile, z float64) {
	nt := *t
	nt.ContentZoom = z
	a.c.UpdateTile(nt.GridID, nt)
	switch t.Kind {
	case rpc.KindText:
		// The next draw reads the cache through textScaleFor; keep the pane's
		// live scale (scroll math divides by it) in step.
		p.TextZoom = textFixedScale * z
	case rpc.KindShell:
		a.applyShellZoom(p.ID, z)
	case rpc.KindURL:
		bridgeSetZoom(p.ID, z)
	}
	a.refreshFileOverlay() // textarea font tracks the scale in text mode
	a.draw()
	tileID, version := t.ID, t.Version
	go func() {
		_, err := a.cl.SetContentZoom(context.Background(), &rpc.SetContentZoomRequest{
			TileID: tileID, Version: version, ContentZoom: z,
		})
		if err != nil {
			// The zoom the user sees is not persisted — the next descent
			// would silently snap back (charter §6).
			a.reportErr(errsurface.Error, "zoom", "content zoom save failed: "+rpcErrText(err))
		}
	}()
}

// applyShellZooms sets the live terminal's font for the pane; the per-draw
// overlay sync re-fits the cell grid, which resizes the PTY to match.
func (a *App) applyShellZoom(paneID string, z float64) {
	if conn := a.shellConnFor(paneID); conn != nil {
		conn.term.Get("options").Set("fontSize", int(shellBaseFontPx*z+0.5))
	}
}
