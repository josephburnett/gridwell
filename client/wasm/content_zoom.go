//go:build js && wasm

package main

import (
	"context"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
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
// base scale times the tile's content zoom. The painter,
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
	next := contentZoomNext(ev.Get("key").String())
	if next == nil {
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

// contentZoomNext maps a zoom-chord key to its step function; nil for keys
// that are not part of the chord. One mapping for both entry points (the
// canvas keydown and the live-view forward), so they can never step
// differently.
func contentZoomNext(key string) func(cur float64) float64 {
	switch key {
	case "+", "=":
		return func(c float64) float64 { return clampContentZoom(c * contentZoomStep) }
	case "-":
		return func(c float64) float64 { return clampContentZoom(c / contentZoomStep) }
	case "0":
		return func(float64) float64 { return 1.0 }
	}
	return nil
}

// contentZoomKeyFromView applies a zoom chord forwarded from a LIVE URL view
// (issue #170): the view owns OS keyboard focus, so the window-level keydown
// never fires — main intercepts the chord in before-input-event and relays
// it keyed by pane. Same guard set as handleContentZoomKey, same one owner
// (applyContentZoom) underneath.
func (a *App) contentZoomKeyFromView(paneID, key string) {
	next := contentZoomNext(key)
	if next == nil {
		return
	}
	p := a.tree.FindPane(paneID)
	if p == nil || p.TextFocus == "" {
		return
	}
	t, ok := a.descendedTile(p)
	if !ok || !rpc.IsContentDescentKind(t.Kind) {
		return
	}
	a.applyContentZoom(p, &t, next(contentZoomOf(&t)))
}

// applyContentZoom updates the cache (the one client copy every reader uses),
// pokes the live surface for the kinds that hold native state, and persists.
func (a *App) applyContentZoom(p *pane.Pane, t *rpc.Tile, z float64) {
	if t.ServesPage {
		// A serves_page descent has no persisted content_zoom (the owning
		// plugin stores no url state), and a client-only zoom would violate
		// the no-client-state rule — the chord is simply inert here.
		return
	}
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
	// Through the framing dispatcher like every other framing write (this
	// call site used to fire-and-forget: no verdict reconcile, and a
	// transport failure silently left the zoom client-only until the next
	// descent snapped it back — audit #7, 2026-08-14). The cache patch above
	// is the optimistic write the dispatcher's policy expects. No beacon
	// form: content zoom is the one framing write with no *Beacon builder,
	// so a quit inside its settle window still loses it.
	tileID := t.ID
	a.postFramingPersist("SetContentZoom", nt.GridID, tileID,
		func(ctx context.Context) error {
			_, err := a.cl.SetContentZoom(ctx, &rpc.SetContentZoomRequest{
				TileID: tileID, ContentZoom: z,
			})
			return err
		})
}

// applyShellZooms sets the live terminal's font for the pane; the per-draw
// overlay sync re-fits the cell grid, which resizes the PTY to match.
func (a *App) applyShellZoom(paneID string, z float64) {
	if conn := a.shellConnFor(paneID); conn != nil {
		conn.term.Get("options").Set("fontSize", int(shellBaseFontPx*z+0.5))
	}
}
