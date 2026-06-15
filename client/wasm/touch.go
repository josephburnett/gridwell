//go:build js && wasm

package main

import "syscall/js"

// touch.go bridges touch-screen input to the canvas's existing mouse-driven
// gesture pipeline. The canvas reads no modifiers and every gesture is a
// button drag, so a single finger maps cleanly onto the left button: a tap is
// a click, a drag is a left-drag (pan / move / etc.). We synthesize real
// MouseEvents and dispatch them at the canvas, so the input handlers need no
// touch-specific branches.
//
// Multi-finger gestures (pinch-zoom and friends) are intentionally left alone
// — only single-finger interactions are translated.

// installTouchInput wires touch→mouse translation on the canvas. touch-action
// is set to none so the browser doesn't claim the gesture for scrolling/zoom
// (which would swallow the touchmove stream), and the listeners are registered
// non-passive so preventDefault both stops that browser handling and suppresses
// the duplicate compatibility mouse events Chromium would otherwise emit.
func (a *App) installTouchInput() {
	a.canvas.Get("style").Set("touchAction", "none")
	opts := js.Global().Get("Object").New()
	opts.Set("passive", false)
	a.canvas.Call("addEventListener", "touchstart", js.FuncOf(a.onTouchStart), opts)
	a.canvas.Call("addEventListener", "touchmove", js.FuncOf(a.onTouchMove), opts)
	a.canvas.Call("addEventListener", "touchend", js.FuncOf(a.onTouchEnd), opts)
	a.canvas.Call("addEventListener", "touchcancel", js.FuncOf(a.onTouchEnd), opts)
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

// onTouchStart begins a left-button gesture from the first finger. Extra
// fingers are ignored (no preventDefault) so multi-touch reaches the browser.
func (a *App) onTouchStart(_ js.Value, args []js.Value) any {
	ev := args[0]
	touches := ev.Get("touches")
	if touches.Get("length").Int() != 1 {
		return nil
	}
	ev.Call("preventDefault")
	t := touches.Index(0)
	a.dispatchMouse("mousedown", t.Get("clientX").Float(), t.Get("clientY").Float(), 0, 1)
	return nil
}

// onTouchMove drives the in-flight left-drag from the moving finger.
func (a *App) onTouchMove(_ js.Value, args []js.Value) any {
	ev := args[0]
	touches := ev.Get("touches")
	if touches.Get("length").Int() != 1 {
		return nil
	}
	ev.Call("preventDefault")
	t := touches.Index(0)
	a.dispatchMouse("mousemove", t.Get("clientX").Float(), t.Get("clientY").Float(), 0, 1)
	return nil
}

// onTouchEnd releases the left button when the last finger lifts.
func (a *App) onTouchEnd(_ js.Value, args []js.Value) any {
	ev := args[0]
	// Only finish once no fingers remain on screen.
	if ev.Get("touches").Get("length").Int() != 0 {
		return nil
	}
	ev.Call("preventDefault")
	changed := ev.Get("changedTouches")
	if changed.Get("length").Int() == 0 {
		return nil
	}
	t := changed.Index(0)
	a.dispatchMouse("mouseup", t.Get("clientX").Float(), t.Get("clientY").Float(), 0, 0)
	return nil
}
