//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/client/touchgest"
)

// touch.go bridges touch-screen input to the canvas's existing mouse-driven
// gesture pipeline. All classification (tap vs drag vs long-press-right vs
// two-finger pinch/scroll/tap) lives in the pure client/touchgest machine;
// this file only feeds it events and dispatches the synthetic MouseEvents /
// WheelEvents it returns, so the input handlers need no touch-specific
// branches and the whole policy is unit-tested without a browser.

// installTouchInput wires touch→mouse translation on the canvas. touch-action
// is set to none so the browser doesn't claim the gesture for scrolling/zoom
// (which would swallow the touchmove stream), and the listeners are registered
// non-passive so preventDefault both stops that browser handling and suppresses
// the duplicate compatibility mouse events the browser would otherwise emit.
func (a *App) installTouchInput() {
	a.touch = touchgest.New()
	// One retained timer callback (js.Func allocations leak if made per
	// press); armed blindly on every touchstart, and the machine ignores
	// firings that don't land on a still-held press.
	a.touchTimerCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.dispatchTouchActions(a.touch.Timer(js.Global().Get("performance").Call("now").Float()))
		return nil
	})
	a.canvas.Get("style").Set("touchAction", "none")
	opts := js.Global().Get("Object").New()
	opts.Set("passive", false)
	a.canvas.Call("addEventListener", "touchstart", js.FuncOf(a.onTouchStart), opts)
	a.canvas.Call("addEventListener", "touchmove", js.FuncOf(a.onTouchMove), opts)
	a.canvas.Call("addEventListener", "touchend", js.FuncOf(a.onTouchEnd), opts)
	a.canvas.Call("addEventListener", "touchcancel", js.FuncOf(a.onTouchEnd), opts)
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

// dispatchTouchActions turns the machine's decisions into real DOM events at
// the canvas so the normal mouse/wheel listeners handle them.
func (a *App) dispatchTouchActions(actions []touchgest.Action) {
	for _, act := range actions {
		switch act.Kind {
		case touchgest.MouseDown:
			a.dispatchMouse("mousedown", act.Pos.X, act.Pos.Y, act.Button, buttonsMask(act.Button))
		case touchgest.MouseMove:
			a.dispatchMouse("mousemove", act.Pos.X, act.Pos.Y, act.Button, buttonsMask(act.Button))
		case touchgest.MouseUp:
			a.dispatchMouse("mouseup", act.Pos.X, act.Pos.Y, act.Button, 0)
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

// dispatchMouse builds and fires a synthetic MouseEvent at the canvas so the
// normal mousedown/mousemove/mouseup listeners handle it.
func (a *App) dispatchMouse(typ string, clientX, clientY float64, button, buttons int) {
	init := js.Global().Get("Object").New()
	init.Set("clientX", clientX)
	init.Set("clientY", clientY)
	init.Set("button", button)
	init.Set("buttons", buttons)
	init.Set("bubbles", true)
	init.Set("cancelable", true)
	ev := js.Global().Get("MouseEvent").New(typ, init)
	a.canvas.Call("dispatchEvent", ev)
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

// onTouchStart feeds the (new) full touch list to the machine and blindly
// arms a long-press timer; the machine ignores stale firings, so the timer
// never needs cancelling.
func (a *App) onTouchStart(_ js.Value, args []js.Value) any {
	ev := args[0]
	ev.Call("preventDefault")
	t := ev.Get("timeStamp").Float()
	a.dispatchTouchActions(a.touch.Start(touchPoints(ev.Get("touches")), t))
	js.Global().Call("setTimeout", a.touchTimerCb, int(touchgest.HoldMs)+5)
	return nil
}

func (a *App) onTouchMove(_ js.Value, args []js.Value) any {
	ev := args[0]
	ev.Call("preventDefault")
	a.dispatchTouchActions(a.touch.Move(touchPoints(ev.Get("touches")), ev.Get("timeStamp").Float()))
	return nil
}

func (a *App) onTouchEnd(_ js.Value, args []js.Value) any {
	ev := args[0]
	ev.Call("preventDefault")
	a.dispatchTouchActions(a.touch.End(touchPoints(ev.Get("touches")), ev.Get("timeStamp").Float()))
	return nil
}
