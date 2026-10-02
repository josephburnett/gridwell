//go:build js && wasm

package main

import (
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textcursor"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// A click lands on what it hits, in any pane, and the keyboard is set once,
// by the surface that took the press (CLAUDE.md, 2026-10-02). This file owns
// both halves: landPress finishes a press the canvas received for a surface
// that was not there to take it, and takeKeyboard is the one writer of DOM
// keyboard focus outside a modal.

// landPress runs gesture.Land for a left press inside p's content descent.
// focusToPane has run, so p is the focused pane and its overlays are shown.
func (a *App) landPress(p *pane.Pane, r pane.Rect, sx, sy float64, ev js.Value) {
	switch gesture.Land(a.surfaceOf(p), pointInFileInner(r, sx, sy)) {
	case gesture.LandCaret:
		a.caretAt(p, r, sx, sy)
	case gesture.LandElement:
		a.clickRenderedAt(ev)
	case gesture.LandAscend:
		a.ascend(p, 1, true)
	}
	// LandTerminal and LandPane are the keyboard alone: takeKeyboard.
}

func (a *App) surfaceOf(p *pane.Pane) gesture.Surface {
	switch a.descentKind(p) {
	case rpc.DescentShell:
		if a.shellConnFor(p.ID) != nil {
			return gesture.SurfaceShell
		}
		return gesture.SurfaceFace
	case rpc.DescentURL:
		if a.urlViewFor(p.ID) != nil {
			return gesture.SurfaceURL
		}
		return gesture.SurfaceFace
	}
	switch fp, _, _, d := a.focusedTextDescent(); {
	case fp != p:
	case d.Mode == rpc.TextModeText:
		return gesture.SurfaceRawText
	case d.Mode == rpc.TextModeRendered:
		return gesture.SurfaceRendered
	}
	return gesture.SurfaceFace
}

// caretAt puts the textarea's caret on the character painted under (sx, sy).
// The geometry is drawMarkdownInPane's, since that face is what was clicked.
func (a *App) caretAt(p *pane.Pane, r pane.Rect, sx, sy float64) {
	if !a.hasTextarea() {
		return
	}
	scale := a.textScaleFor(p)
	x, y, _, _ := textInnerBox(r)
	st := a.defaultMarkdownStyle()
	c := a.cctx
	c.Call("save")
	setFont(c, st.codePx*scale, st.monospace, false)
	m := c.Call("measureText", "M")
	c.Call("restore")
	slot := markdown.RawTextLineSlot(st.codePx, rawTextLineHeight, scale, st.pad, 0, 0, 0)
	g := textcursor.Grid{
		Cols: rawWrapCols(m, a.textContentWidth(p), scale, st.pad),
		Left: st.pad * scale,
		Top:  slot.Top0,
		Slot: slot.Slot,
		Adv:  m.Get("width").Float(),
	}
	ta := a.overlays.textTextarea
	off := textcursor.CaretAt(ta.Get("value").String(), g,
		sx-(x-p.TextScrollX*scale), sy-(y-p.TextScrollY*scale))
	ta.Call("setSelectionRange", off, off)
}

// clickRenderedAt hands the press to the rendered element under it, through
// the view's own click handler.
func (a *App) clickRenderedAt(ev js.Value) {
	if !a.overlays.renderedView.Truthy() {
		return
	}
	el := a.doc.Call("elementFromPoint", ev.Get("clientX"), ev.Get("clientY"))
	if el.Truthy() && a.overlays.renderedView.Call("contains", el).Bool() {
		el.Call("click")
	}
}

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
