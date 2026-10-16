//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/client/touchgest"
)

// Bridges touch input to the mouse-driven gesture pipeline. Classification
// lives in the pure client/touchgest machine, so the input handlers need no
// touch-specific branches. One install serves every surface, because real
// mouse events reach the DOM overlays by browser hit-testing and the touch
// translation must follow the same routing.

// installTouchInput wires touch-to-mouse translation on the canvas.
// touch-action is none so the browser does not claim the gesture and swallow
// the touchmove stream.
func (a *App) installTouchInput() {
	a.touch = touchgest.New()
	// One retained callback, because a js.Func made per press leaks. Armed
	// blindly on every touchstart; the machine ignores a firing that does not
	// land on a still-held press.
	a.touchTimerCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.dispatchTouchActions(a.touch.Timer(touchNow()))
		return nil
	})
	a.canvas.Get("style").Set("touchAction", "none")
	a.installOverlayTouch(a.canvas, nil)
}

// touchNow is the one clock every machine feed uses. An event's own timeStamp
// is epoch-based on some engines, which would put t0 in a different domain
// from the long-press Timer and silently kill the classification.
func touchNow() float64 {
	return js.Global().Get("performance").Call("now").Float()
}

// installOverlayTouch wires the shared touch-to-mouse translation onto el.
// claim decides at touchstart whether this element takes the gesture, nil
// claiming everything; once claimed the whole gesture tail is forwarded. The
// listeners are non-passive so preventDefault stops the browser's duplicate
// compatibility mouse events. A caller whose element is per-session, such as
// the shell container, must Release the returned js.Funcs on teardown.
func (a *App) installOverlayTouch(el js.Value, claim func(pts []touchgest.Point) bool) []js.Func {
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
				// MouseDown routes to this element for the rest of the
				// gesture, the routing a real mouse gets.
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
	fns := []js.Func{handler("start"), handler("move"), handler("end"), handler("end")}
	for i, name := range []string{"touchstart", "touchmove", "touchend", "touchcancel"} {
		el.Call("addEventListener", name, fns[i], opts)
	}
	return fns
}

// touchPoints converts a TouchList to touchgest points in clientX/Y, the same
// space mouseXY reads.
func touchPoints(list js.Value) []touchgest.Point {
	n := list.Get("length").Int()
	pts := make([]touchgest.Point, n)
	for i := 0; i < n; i++ {
		t := list.Index(i)
		pts[i] = touchgest.Point{X: t.Get("clientX").Float(), Y: t.Get("clientY").Float()}
	}
	return pts
}

// dispatchTouchActions turns the machine's decisions into real DOM events,
// routed as a real mouse would be. MouseDown fires at the element the gesture
// started on, since every overlay acts on mousedown; the middle button goes
// to the canvas, having no element semantics. The tail fires at the canvas,
// because a mousedown parks the overlay.
func (a *App) dispatchTouchActions(actions []touchgest.Action) {
	for _, act := range actions {
		switch act.Kind {
		case touchgest.MouseDown:
			target := a.touchDownTarget
			if target.IsUndefined() || target.IsNull() || act.Button == 1 {
				target = a.canvas
			}
			a.dispatchMouse(target, "mousedown", act, buttonsMask(act.Button))
		case touchgest.MouseMove:
			a.dispatchMouse(a.canvas, "mousemove", act, buttonsMask(act.Button))
		case touchgest.MouseUp:
			a.dispatchMouse(a.canvas, "mouseup", act, 0)
		case touchgest.Wheel:
			a.dispatchWheel(act.Pos.X, act.Pos.Y, act.DeltaY)
		}
	}
}

// buttonsMask maps a MouseEvent.button ordinal to the held-down
// MouseEvent.buttons bitmask.
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

func (a *App) dispatchMouse(target js.Value, typ string, act touchgest.Action, buttons int) {
	init := js.Global().Get("Object").New()
	init.Set("clientX", act.Pos.X)
	init.Set("clientY", act.Pos.Y)
	init.Set("button", act.Button)
	init.Set("buttons", buttons)
	init.Set("shiftKey", act.Shift)
	init.Set("bubbles", true)
	init.Set("cancelable", true)
	ev := js.Global().Get("MouseEvent").New(typ, init)
	target.Call("dispatchEvent", ev)
}

// dispatchWheel fires a synthetic WheelEvent at the canvas so onWheel routes
// it exactly like a physical wheel.
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

// installTextareaTouch forwards multi-finger touches from the editing
// textarea into the same gesture machine the canvas feeds, so two-finger tap
// and pinch work over a text descent. Single-finger touches stay native for
// caret placement, selection and the OS keyboard.
func (a *App) installTextareaTouch(ta js.Value) {
	a.installOverlayTouch(ta, func(pts []touchgest.Point) bool {
		return len(pts) >= 2
	})
}

// shellTouchClaim routes multi-finger gestures to the shared translation and
// leaves single fingers native to the terminal. The ascend handle lives in
// the bottom bar, outside the container, so no single-finger claim remains.
func shellTouchClaim() func(pts []touchgest.Point) bool {
	return func(pts []touchgest.Point) bool {
		return len(pts) >= 2
	}
}
