//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/client/touchgest"
)

// touch.go bridges touch-screen input to the existing mouse-driven gesture
// pipeline. All classification (tap vs drag vs long-press-right vs two-finger
// pinch/scroll/tap) lives in the pure client/touchgest machine; this file only
// feeds it events and dispatches the synthetic MouseEvents / WheelEvents it
// returns, so the input handlers need no touch-specific branches and the whole
// policy is unit-tested without a browser.
//
// ONE install (installOverlayTouch) serves every surface — the canvas AND the
// DOM overlays that implement right-button semantics (the corner toggle
// circle, the name pill, the xterm container, the file textarea). Real mouse
// events reach those overlays by browser hit-testing; the touch translation
// must follow the same routing or a long-press right-click never reaches them
// (issue #191: the canvas-only install was exactly that gap — long-press on
// the menu button of a text or shell descent did nothing).

// installTouchInput wires touch→mouse translation on the canvas. touch-action
// is set to none so the browser doesn't claim the gesture for scrolling/zoom
// (which would swallow the touchmove stream).
func (a *App) installTouchInput() {
	a.touch = touchgest.New()
	// One retained timer callback (js.Func allocations leak if made per
	// press); armed blindly on every touchstart, and the machine ignores
	// firings that don't land on a still-held press.
	a.touchTimerCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.dispatchTouchActions(a.touch.Timer(touchNow()))
		return nil
	})
	a.canvas.Get("style").Set("touchAction", "none")
	a.installOverlayTouch(a.canvas, nil)
}

// touchNow returns the shared clock every machine feed uses. ONE time domain:
// the long-press Timer compares against performance.now(), so the Start/Move/
// End feeds must read the same clock — an event's own timeStamp is epoch-based
// on some engines, which would put t0 in a different domain and silently kill
// the long-press classification everywhere.
func touchNow() float64 {
	return js.Global().Get("performance").Call("now").Float()
}

// installOverlayTouch wires the shared touch→mouse translation onto el,
// feeding the ONE touchgest machine. claim decides at touchstart whether this
// element takes the gesture (nil claims everything): the textarea claims only
// multi-finger so native caret/selection/keyboard keep single-finger; the
// xterm container claims multi-finger plus a single finger starting on the
// visible ascend circle, leaving the terminal's native touch alone. Once
// claimed, the whole gesture tail is forwarded, per-finger lifts included.
// The listeners are non-passive so preventDefault stops the browser's own
// handling and its duplicate compatibility mouse events.
func (a *App) installOverlayTouch(el js.Value, claim func(pts []touchgest.Point) bool) {
	active := false
	opts := js.Global().Get("Object").New()
	opts.Set("passive", false)
	handler := func(phase string) js.Func {
		return js.FuncOf(func(_ js.Value, args []js.Value) any {
			ev := args[0]
			touches := ev.Get("touches")
			pts := touchPoints(touches)
			if !active {
				if phase != "start" || (claim != nil && !claim(pts)) {
					return nil
				}
				active = true
				// MouseDown routes to THIS element for the rest of the
				// gesture — the same routing the browser gives a real mouse.
				a.touchDownTarget = el
			}
			ev.Call("preventDefault")
			t := touchNow()
			switch phase {
			case "start":
				a.dispatchTouchActions(a.touch.Start(pts, t))
				js.Global().Call("setTimeout", a.touchTimerCb, int(touchgest.HoldMs)+5)
			case "move":
				a.dispatchTouchActions(a.touch.Move(pts, t))
			default: // end / cancel
				a.dispatchTouchActions(a.touch.End(pts, t))
				if touches.Get("length").Int() == 0 {
					active = false
				}
			}
			return nil
		})
	}
	el.Call("addEventListener", "touchstart", handler("start"), opts)
	el.Call("addEventListener", "touchmove", handler("move"), opts)
	el.Call("addEventListener", "touchend", handler("end"), opts)
	el.Call("addEventListener", "touchcancel", handler("end"), opts)
}

// touchPoints converts a TouchList to touchgest points (clientX/Y — the same
// coordinate space mouseXY reads from MouseEvents).
func touchPoints(list js.Value) []touchgest.Point {
	n := list.Get("length").Int()
	pts := make([]touchgest.Point, n)
	for i := 0; i < n; i++ {
		t := list.Index(i)
		pts[i] = touchgest.Point{X: t.Get("clientX").Float(), Y: t.Get("clientY").Float()}
	}
	return pts
}

// dispatchTouchActions turns the machine's decisions into real DOM events.
// Routing mirrors the real-mouse flow:
//   - MouseDown fires at the element the gesture started on (every overlay
//     acts on mousedown: toggle/ascend, rename/zoom, xterm's forward) —
//     EXCEPT the middle button, which has no element semantics anywhere and
//     goes straight to the canvas (the ascend shortcut's owner).
//   - MouseMove / MouseUp / Wheel fire at the canvas: once a mousedown armed
//     a canvas gesture the overlay is parked and the drag tail belongs to
//     the gesture pipeline, exactly as with a real mouse.
func (a *App) dispatchTouchActions(actions []touchgest.Action) {
	for _, act := range actions {
		switch act.Kind {
		case touchgest.MouseDown:
			target := a.touchDownTarget
			if target.IsUndefined() || target.IsNull() || act.Button == 1 {
				target = a.canvas
			}
			a.dispatchMouse(target, "mousedown", act.Pos.X, act.Pos.Y, act.Button, buttonsMask(act.Button))
		case touchgest.MouseMove:
			a.dispatchMouse(a.canvas, "mousemove", act.Pos.X, act.Pos.Y, act.Button, buttonsMask(act.Button))
		case touchgest.MouseUp:
			a.dispatchMouse(a.canvas, "mouseup", act.Pos.X, act.Pos.Y, act.Button, 0)
		case touchgest.Wheel:
			a.dispatchWheel(act.Pos.X, act.Pos.Y, act.DeltaY)
		}
	}
}

// buttonsMask maps a MouseEvent.button ordinal to the MouseEvent.buttons
// bitmask (left=1, right=2, middle=4) for the held-down state.
func buttonsMask(button int) int {
	switch button {
	case 1:
		return 4
	case 2:
		return 2
	default:
		return 1
	}
}

// dispatchMouse builds and fires a synthetic MouseEvent at target so its
// normal mousedown/mousemove/mouseup listeners handle it.
func (a *App) dispatchMouse(target js.Value, typ string, clientX, clientY float64, button, buttons int) {
	init := js.Global().Get("Object").New()
	init.Set("clientX", clientX)
	init.Set("clientY", clientY)
	init.Set("button", button)
	init.Set("buttons", buttons)
	init.Set("bubbles", true)
	init.Set("cancelable", true)
	ev := js.Global().Get("MouseEvent").New(typ, init)
	target.Call("dispatchEvent", ev)
}

// dispatchWheel fires a synthetic WheelEvent at the canvas so onWheel routes
// it exactly like a physical wheel/trackpad: zoom over a grid, scroll over a
// rendered doc.
func (a *App) dispatchWheel(clientX, clientY, deltaY float64) {
	init := js.Global().Get("Object").New()
	init.Set("clientX", clientX)
	init.Set("clientY", clientY)
	init.Set("deltaY", deltaY)
	init.Set("bubbles", true)
	init.Set("cancelable", true)
	ev := js.Global().Get("WheelEvent").New("wheel", init)
	a.canvas.Call("dispatchEvent", ev)
}

// installTextareaTouch forwards MULTI-finger touches from the file textarea
// into the same gesture machine the canvas feeds, so two-finger tap (ascend)
// and pinch work over a text descent — the touch analogue of the textarea's
// mouse forwarding in text_overlay.go. Single-finger touches are left
// entirely native (caret placement, text selection, OS keyboard); the machine
// accepts a two-finger Start from idle for exactly this case.
func (a *App) installTextareaTouch(ta js.Value) {
	a.installOverlayTouch(ta, func(pts []touchgest.Point) bool {
		return len(pts) >= 2
	})
}

// shellTouchClaim builds the xterm container's claim: multi-finger gestures
// (pinch, two-finger-tap ascend), plus a single finger starting on the
// VISIBLE corner ascend circle — the menu button — so a long-press there
// arms the same right-button ascend a mouse gets. Everything else stays
// native to the terminal (tap-to-focus, xterm's own touch scrolling).
func shellTouchClaim(circle js.Value) func(pts []touchgest.Point) bool {
	return func(pts []touchgest.Point) bool {
		if len(pts) >= 2 {
			return true
		}
		if len(pts) != 1 {
			return false
		}
		if circle.Get("style").Get("display").String() == "none" {
			return false
		}
		r := circle.Call("getBoundingClientRect")
		x, y := pts[0].X, pts[0].Y
		return x >= r.Get("left").Float() && x <= r.Get("right").Float() &&
			y >= r.Get("top").Float() && y <= r.Get("bottom").Float()
	}
}
