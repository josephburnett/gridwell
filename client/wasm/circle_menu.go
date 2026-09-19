//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/client/circlemenu"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/theme"
)

// The circle slot's right-click. circlemenu.For says which menu the slot's
// verdict stands for; this dispatches it to one of two renderers. The live
// url view's menu is the host's, because its rows are the page's. Everything
// else is a list of declared choices, rendered natively where the host has a
// menu and as a DOM popover where it has not.

// openCircleMenu runs the right-click on the focused pane's slot.
func (a *App) openCircleMenu(p *pane.Pane) {
	switch circlemenu.For(a.barSlotMode(p)) {
	case circlemenu.MenuURL:
		a.bridgeShowMenu(p.ID)
	case circlemenu.MenuTheme:
		items := circlemenu.ThemeItems(a.themeName)
		a.openChoiceMenu(items, func(id string) {
			if t, ok := theme.Parse(id); ok {
				a.setTheme(t)
			}
		})
	}
}

// openChoiceMenu pops items and runs onPick with the chosen id. A dismissal
// picks nothing, so nothing changes.
func (a *App) openChoiceMenu(items []circlemenu.Item, onPick func(string)) {
	if a.caps.ChoiceMenu {
		a.bridgeChoiceMenu(items, onPick)
		return
	}
	a.openDOMChoiceMenu(items, onPick)
}

// bridgeChoiceMenu hands the rows to the host's native menu, which answers
// with the chosen id or null.
func (a *App) bridgeChoiceMenu(items []circlemenu.Item, onPick func(string)) {
	g := bridge()
	if !g.Truthy() {
		return
	}
	rows := js.Global().Get("Array").New()
	for _, it := range items {
		o := js.Global().Get("Object").New()
		o.Set("id", it.ID)
		o.Set("label", it.Label)
		o.Set("checked", it.Checked)
		rows.Call("push", o)
	}
	args := js.Global().Get("Object").New()
	args.Set("items", rows)
	a.bridgeCall(g, "showChoiceMenu", args, func(res js.Value) {
		if res.Type() == js.TypeString {
			onPick(res.String())
		}
	}, nil)
}

// domChoiceMenuID is the popover's element id; a spec and the dismissal
// listener both address it.
const domChoiceMenuID = "gw-circle-menu"

// openDOMChoiceMenu is the browser host's menu: there is no native one, so the
// client draws it, in the palette's own menu colors and anchored over the
// circle it came from. It is session chrome and nothing else knows it exists,
// so it is torn out whole on a pick, a press outside, or Escape.
func (a *App) openDOMChoiceMenu(items []circlemenu.Item, onPick func(string)) {
	a.closeDOMChoiceMenu()

	box := a.doc.Call("createElement", "div")
	box.Set("id", domChoiceMenuID)
	st := box.Get("style")
	st.Set("position", "absolute")
	st.Set("zIndex", "21")
	st.Set("minWidth", "150px")
	st.Set("padding", "4px")
	st.Set("borderRadius", "6px")
	st.Set("background", a.pal.MenuBg)
	st.Set("border", "1px solid "+a.pal.Border)
	st.Set("boxShadow", "0 8px 32px "+a.pal.CardShadow)
	st.Set("fontSize", "12px")
	st.Set("userSelect", "none")

	pick := func(id string) {
		a.closeDOMChoiceMenu()
		onPick(id)
	}
	for _, it := range items {
		row := a.doc.Call("createElement", "div")
		row.Call("setAttribute", "data-gw-choice", it.ID)
		label := it.Label
		if it.Checked {
			label = "✓ " + label
		}
		row.Set("textContent", label)
		rs := row.Get("style")
		rs.Set("padding", "6px 10px")
		rs.Set("borderRadius", "4px")
		rs.Set("cursor", "pointer")
		rs.Set("whiteSpace", "nowrap")
		if it.Checked {
			rs.Set("color", a.pal.MenuItemHi)
		} else {
			rs.Set("color", a.pal.SubtleText)
		}
		id := it.ID
		a.overlays.choiceMenuCbs = append(a.overlays.choiceMenuCbs,
			listen(row, "mousedown", func(ev js.Value) {
				// mousedown, not click: the window-level dismissal listener
				// below fires first on a click and would tear the row out.
				ev.Call("preventDefault")
				ev.Call("stopPropagation")
				pick(id)
			}))
		box.Call("appendChild", row)
	}

	a.doc.Get("body").Call("appendChild", box)
	a.overlays.choiceMenu = box

	// Anchored over the circle: right edge aligned to it, sitting above the
	// bar, clamped into the window so a narrow phone still shows every row.
	cx, cy := a.plusButtonCenter()
	w := box.Get("offsetWidth").Float()
	h := box.Get("offsetHeight").Float()
	left := cx + plusButtonRadius - w
	if left < 4 {
		left = 4
	}
	top := cy - plusButtonRadius - 6 - h
	if top < 4 {
		top = 4
	}
	st.Set("left", pxf(left))
	st.Set("top", pxf(top))

	// One task later: the press that opened the menu is still bubbling, and a
	// window listener added now would be called by it and close the menu at
	// once.
	var arm js.Func
	arm = js.FuncOf(func(js.Value, []js.Value) any {
		arm.Release()
		if !a.overlays.choiceMenu.Equal(box) {
			return nil // already dismissed
		}
		a.overlays.choiceMenuCbs = append(a.overlays.choiceMenuCbs,
			listen(a.win, "mousedown", func(js.Value) { a.closeDOMChoiceMenu() }),
			listen(a.win, "keydown", func(ev js.Value) {
				if ev.Get("key").String() == "Escape" {
					a.closeDOMChoiceMenu()
				}
			}))
		return nil
	})
	js.Global().Call("setTimeout", arm, 0)
}

// closeDOMChoiceMenu removes the popover and releases its callbacks. It is
// idempotent, so every dismissal path can call it.
func (a *App) closeDOMChoiceMenu() {
	for _, off := range a.overlays.choiceMenuCbs {
		off()
	}
	a.overlays.choiceMenuCbs = nil
	if a.overlays.choiceMenu.Truthy() {
		a.overlays.choiceMenu.Call("remove")
		a.overlays.choiceMenu = js.Value{}
	}
}

// listen adds a DOM listener and returns the remover, which also releases the
// js.Func: a popover opened and closed repeatedly must not leak one per open.
func listen(target js.Value, event string, fn func(js.Value)) func() {
	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		ev := js.Undefined()
		if len(args) > 0 {
			ev = args[0]
		}
		fn(ev)
		return nil
	})
	target.Call("addEventListener", event, cb)
	return func() {
		target.Call("removeEventListener", event, cb)
		cb.Release()
	}
}

// slotHasMenu is what wsbar.RouteClick reads to send a right press to the
// slot's menu rather than swallowing it.
func (a *App) slotHasMenu(p *pane.Pane) bool {
	return circlemenu.For(a.barSlotMode(p)) != circlemenu.MenuNone
}
