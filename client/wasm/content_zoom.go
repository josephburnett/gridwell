//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/contentzoom"
	"github.com/josephburnett/gridwell/client/pane"
)

// Content zoom: Ctrl/Cmd +/-/0 while descended into a text, shell or url tile
// scales the content. The zoom is per-tile framing, server-owned and never
// bumping version, restored on every descent. What a chord does is
// client/contentzoom; this file is the hands.

// textScaleFor is the render transform for a descended text pane. The
// painter, the wrap width and the textarea box all derive from it, so they
// cannot disagree about how big the text is.
func (a *App) textScaleFor(p *pane.Pane) float64 {
	if t, ok := a.descendedTile(p); ok {
		return textFixedScale * contentzoom.Of(t.GetContentZoom())
	}
	return textFixedScale
}

// handleContentZoomKey consumes a Ctrl/Cmd +/-/0 chord for the focused
// descended pane. True means the caller stops.
func (a *App) handleContentZoomKey(ev js.Value) bool {
	if !(ev.Get("ctrlKey").Bool() || ev.Get("metaKey").Bool()) {
		return false
	}
	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" {
		return false
	}
	t, ok := a.descendedTile(p)
	if !ok {
		return false
	}
	v := a.contentZoomVerdict(p, t, ev.Get("key").String())
	if !v.Consume {
		return false
	}
	ev.Call("preventDefault")
	a.applyContentZoom(p, t, v)
	return true
}

// contentZoomKeyFromView applies a zoom chord forwarded from a live URL view.
// The view owns OS keyboard focus, so the window-level keydown never fires
// and main relays the chord keyed by pane.
func (a *App) contentZoomKeyFromView(paneID, key string) {
	p := a.tree.FindPane(paneID)
	if p == nil || p.ContentID() == "" {
		return
	}
	t, ok := a.descendedTile(p)
	if !ok {
		return
	}
	a.applyContentZoom(p, t, a.contentZoomVerdict(p, t, key))
}

func (a *App) contentZoomVerdict(p *pane.Pane, t *gridwellv1.Tile, key string) contentzoom.Verdict {
	return contentzoom.Decide(t.Kind, key, contentzoom.Of(t.GetContentZoom()))
}

// applyContentZoom holds the zoom in the cache until its echo, since the
// next press reads it there, pokes the live surface for the kinds that hold
// native state, and persists, each arm as the verdict says.
func (a *App) applyContentZoom(p *pane.Pane, t *gridwellv1.Tile, v contentzoom.Verdict) {
	if !v.Apply {
		return
	}
	hz := a.c.Hold(t.Id, cache.WroteZoom, func(n *gridwellv1.Tile) { n.ContentZoom = v.Next })
	switch k := rpc.KindOf(t.Kind); {
	case k.Content == rpc.ContentBody:
		// Keep the pane's live scale, which the scroll math divides by, in
		// step with what the next draw reads.
		p.TextZoom = textFixedScale * v.Next
	case k.Live == rpc.DescentShell:
		a.applyShellZoom(p.ID, v.Next)
	case k.Live == rpc.DescentURL:
		a.bridgeSetZoom(p.ID, v.Next)
	}
	a.refreshFileOverlay() // textarea font tracks the scale in text mode
	a.draw()
	// Through the framing dispatcher like every other framing write, because
	// a fire-and-forget call would reconcile no verdict and a transport
	// failure would leave the zoom client-only. There is no beacon form,
	// content zoom being the one framing write without one, so a quit inside
	// its settle window still loses it.
	tileID, z := t.Id, v.Next
	var resp *gridwellv1.Tile
	a.postFramingPersist(write{label: "SetContentZoom", gid: t.GridId, id: tileID, chain: tileID,
		call: func(ctx context.Context) error {
			var err error
			resp, err = a.cl.SetContentZoom(ctx, tileID, z)
			return err
		},
		then: func() { hz.Landed(resp) },
		done: func() { hz.Released() }})
}

// applyShellZoom sets the live terminal's font. The per-draw overlay sync
// re-fits the cell grid, which resizes the PTY to match.
func (a *App) applyShellZoom(paneID string, z float64) {
	if conn := a.shellConnFor(paneID); conn != nil {
		conn.term.Get("options").Set("fontSize", contentzoom.ShellFontPx(z))
	}
}
