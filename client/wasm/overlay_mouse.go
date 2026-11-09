//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/client/pane"
)

// Owns how a DOM overlay over a pane shares the mouse and the screen with the
// canvas, which it paints above and hit-tests before: a pane gesture's press
// and its preview are the canvas's.

// overlayParked is pane.ParkSurface for the text overlay over the focused p;
// the canvas paints p's text meanwhile.
func (a *App) overlayParked(p *pane.Pane) bool {
	return p != nil && pane.ParkSurface(a.canvasGesture(), p.ID)
}

// overlayClip parks it by clipping it whole, which keeps the keyboard, caret
// and scroll that display none would drop.
func (a *App) overlayClip(p *pane.Pane) string {
	if a.overlayParked(p) {
		return "inset(50%)"
	}
	return "none"
}

// installOverlayMouse gives the overlay el the canvas's press behavior. The
// right button and the context menu are Gridwell's on every overlay, so they
// are owned here; claim decides each other press, true keeping it for the
// overlay and false letting it fall through to the canvas path, which owns
// the middle-button ascent. Capture phase always: an overlay whose own
// library listens first, as xterm's does, would otherwise take the press
// before Gridwell sees it, and the overlays that have no such library cannot
// tell the difference. A caller whose element is per-session must Release the
// returned js.Funcs on teardown; see installOverlayTouch.
func (a *App) installOverlayMouse(el js.Value, claim func(ev js.Value, sx, sy float64) bool) []js.Func {
	press := js.FuncOf(func(_ js.Value, args []js.Value) any {
		ev := args[0]
		sx, sy := mouseXY(ev, a.canvas)
		if ev.Get("button").Int() != 2 {
			if claim != nil && claim(ev, sx, sy) {
				return nil
			}
			a.onMouseDown(js.Null(), args)
			return nil
		}
		ev.Call("preventDefault")
		ev.Call("stopPropagation")
		a.onMouseDown(js.Null(), args)
		// onRightDown arms the gesture but does not redraw. Park the overlay
		// so the rest of the drag lands on the canvas, not on it.
		a.draw()
		return nil
	})
	menu := js.FuncOf(func(_ js.Value, args []js.Value) any {
		args[0].Call("preventDefault")
		return nil
	})
	capture := js.ValueOf(true)
	el.Call("addEventListener", "mousedown", press, capture)
	el.Call("addEventListener", "contextmenu", menu, capture)
	return []js.Func{press, menu}
}
