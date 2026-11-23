//go:build js && wasm

package main

import (
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// Typing goes to the keyboard's owner, gesture.Keyboard, and this file is
// the only code in client/wasm that focuses or blurs an element: applyKeyboard
// makes the owner true in the page, at every press and at every draw whose
// owner differs from the one last applied, and tells main, which makes it true
// in the OS. A focus that lands anywhere else with no press behind it is put
// back.

// keyboardState is what applyKeyboard last made true.
type keyboardState struct {
	applied gesture.Owner
	el      js.Value
	// pressing is a pointer held down: the focus a press lands is the user's,
	// and the press's end applies the owner.
	pressing bool
}

var ownerNames = map[gesture.OwnerKind]string{
	gesture.OwnerCanvas:   "canvas",
	gesture.OwnerInput:    "input",
	gesture.OwnerEditor:   "editor",
	gesture.OwnerTerminal: "terminal",
	gesture.OwnerPage:     "page",
}

// keyboardOwner gathers gesture.Keyboard's inputs from live state.
func (a *App) keyboardOwner() gesture.Owner {
	st := gesture.KeyboardState{InputOpen: a.overlays.rename != nil || a.overlays.urlModalOpen}
	p := a.tree.FocusedPane()
	if p == nil {
		return gesture.Keyboard(st)
	}
	st.Pane, st.Surface = p.ID, a.surfaceOf(p)
	switch st.Surface {
	case gesture.SurfaceRawText:
		st.Up = a.hasTextarea() && a.overlays.textTextarea.Get("style").Get("display").String() != "none"
	case gesture.SurfaceShell:
		c := a.shellConnFor(p.ID)
		st.Up = c != nil && c.shown
	case gesture.SurfaceURL:
		st.Up = !pane.ParkSurface(a.canvasGesture(), p.ID)
	}
	return gesture.Keyboard(st)
}

// keyboardElement is the element the owner types into in this page. A page's
// is the canvas: its keys go to its own webContents, and the root's share of
// the keyboard rests there.
func (a *App) keyboardElement(o gesture.Owner) js.Value {
	switch o.Kind {
	case gesture.OwnerInput:
		if r := a.overlays.rename; r != nil {
			return r.el
		}
		return a.doc.Call("getElementById", "gw-url-input")
	case gesture.OwnerEditor:
		return a.overlays.textTextarea
	case gesture.OwnerTerminal:
		if c := a.shellConnFor(o.Pane); c != nil {
			return c.container
		}
	}
	return a.canvas
}

// holdsKeyboard reports whether el, a focused element, is the owner's.
func holdsKeyboard(owner, el js.Value) bool {
	return el.Truthy() && (el.Equal(owner) || owner.Call("contains", el).Bool())
}

// applyKeyboard makes the owner true in the page. A press applies it
// whatever the page holds, inside the user's gesture so a phone's keyboard
// rises; otherwise it runs only when the owner or its element changed, so a
// blur to nothing (a phone dismissing its keyboard) stays until the next.
func (a *App) applyKeyboard(press bool) {
	o := a.keyboardOwner()
	el := a.keyboardElement(o)
	changed := o != a.kb.applied || !el.Equal(a.kb.el)
	if !press && !changed {
		return
	}
	took := a.focusOn(o, el)
	if changed {
		a.emit(traceevent.Keyboard(ownerNames[o.Kind], o.Pane, took))
	}
	page := ""
	if o.Kind == gesture.OwnerPage {
		page = o.Pane
	}
	a.bridgeSetKeyboard(page)
	if took {
		a.kb.applied, a.kb.el = o, el
	}
}

// focusOn focuses o's element el unless it holds the keyboard already, and
// reports whether it does after.
func (a *App) focusOn(o gesture.Owner, el js.Value) bool {
	if !holdsKeyboard(el, a.doc.Get("activeElement")) {
		if o.Kind == gesture.OwnerTerminal {
			a.shellConnFor(o.Pane).term.Call("focus")
		} else {
			el.Call("focus")
		}
	}
	return holdsKeyboard(el, a.doc.Get("activeElement"))
}

// endInput commits an open rename, as its blur would: a press anywhere but on
// it ends it.
func (a *App) endInput() {
	if r := a.overlays.rename; r != nil {
		r.end(true)
	}
}

// installKeyboard tracks the press and puts back a focus that lands with
// none behind it. In the capture phase at the window, ahead of every
// element's own listener.
func (a *App) installKeyboard() {
	capture := js.ValueOf(true)
	a.win.Call("addEventListener", "pointerdown", js.FuncOf(func(_ js.Value, args []js.Value) any {
		a.kb.pressing = true
		if r := a.overlays.rename; r != nil && !args[0].Get("target").Equal(r.el) {
			a.endInput()
		}
		return nil
	}), capture)
	up := js.FuncOf(func(js.Value, []js.Value) any {
		a.kb.pressing = false
		a.applyKeyboard(true)
		return nil
	})
	a.win.Call("addEventListener", "pointerup", up, capture)
	a.win.Call("addEventListener", "pointercancel", up, capture)
	// A finger's tap the page does not cancel ends its pointer events before
	// the mouse events it is retold as, whose mousedown runs the control and
	// the browser's own focus change, so the press ends again at their mouseup.
	a.win.Call("addEventListener", "mouseup", up, capture)
	a.win.Call("addEventListener", "focusin", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if a.kb.pressing {
			return nil
		}
		target := args[0].Get("target")
		o := a.keyboardOwner()
		el := a.keyboardElement(o)
		if holdsKeyboard(el, target) {
			return nil
		}
		a.emit(traceevent.KeyboardPutBack(a.focusName(target), ownerNames[o.Kind], o.Pane))
		a.focusOn(o, el)
		return nil
	}), capture)
}

// focusName names the element a stray focus landed on, for its record.
func (a *App) focusName(el js.Value) string {
	switch {
	case el.Equal(a.canvas):
		return "canvas"
	case a.hasTextarea() && el.Equal(a.overlays.textTextarea):
		return "textarea"
	}
	for _, pl := range a.locals {
		if c := pl.shellConn; c != nil && c.container.Truthy() && c.container.Call("contains", el).Bool() {
			return "terminal"
		}
	}
	if id := el.Get("id"); id.Truthy() && id.String() != "" {
		return "#" + id.String()
	}
	if t := el.Get("tagName"); t.Truthy() {
		return strings.ToLower(t.String())
	}
	return "unknown"
}
