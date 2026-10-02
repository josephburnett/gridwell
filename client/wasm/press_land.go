//go:build js && wasm

package main

import (
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// The keyboard is set once, by the surface that took the press (CLAUDE.md,
// 2026-10-02). takeKeyboard is the one writer of DOM keyboard focus outside a
// modal.

// takeKeyboard gives DOM keyboard focus to the focused pane's surface: the
// textarea in raw text, a shown terminal, the canvas otherwise. A live url view
// holds OS focus itself, so its pane moves nothing. Only a press, a user's
// descent or ascent, or a modal closing calls it, so nothing deferred moves
// the keyboard; a refresh that only re-lays an overlay never does.
func (a *App) takeKeyboard() {
	if a.overlays.renameEditing || a.overlays.urlModalOpen {
		return
	}
	p := a.tree.FocusedPane()
	if p == nil {
		return
	}
	if a.urlViewFor(p.ID) != nil {
		return
	}
	if fp, _, _, d := a.focusedTextDescent(); fp == p && d.Mode == rpc.TextModeText &&
		a.hasTextarea() && a.overlays.textTextarea.Get("style").Get("display").String() != "none" {
		a.overlays.textTextarea.Call("focus")
		return
	}
	if c := a.shellConnFor(p.ID); c != nil && c.shown {
		c.term.Call("focus")
		return
	}
	a.canvas.Call("focus")
}

// installFocusTrace records every DOM focus change, so a trace shows a steal
// and a spec can assert there was none.
func (a *App) installFocusTrace() {
	a.win.Call("addEventListener", "focusin", js.FuncOf(func(_ js.Value, args []js.Value) any {
		el, paneID := a.focusTarget(args[0].Get("target"))
		a.emit(traceevent.DOMFocus(el, paneID))
		return nil
	}), js.ValueOf(true))
}

// focusTarget names the surface an element belongs to and the pane it serves.
func (a *App) focusTarget(el js.Value) (string, string) {
	switch {
	case el.Equal(a.canvas):
		return "canvas", a.tree.Focus
	case a.hasTextarea() && el.Equal(a.overlays.textTextarea):
		return "textarea", a.tree.Focus
	}
	for paneID, pl := range a.locals {
		if c := pl.shellConn; c != nil && c.container.Truthy() && c.container.Call("contains", el).Bool() {
			return "terminal", paneID
		}
	}
	if id := el.Get("id"); id.Truthy() && id.String() != "" {
		return "#" + id.String(), ""
	}
	if t := el.Get("tagName"); t.Truthy() {
		return strings.ToLower(t.String()), ""
	}
	return "unknown", ""
}
