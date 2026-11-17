//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textcursor"
)

// A click lands on what it hits, in any pane (CLAUDE.md, 2026-10-02):
// landPress finishes a press the canvas received for a surface that was not
// there to take it. Where the keyboard goes is keyboard.go's.

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
	// LandPane is the keyboard alone: the press moved its owner.
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
// The geometry is drawMarkdownInPane's, since that preview is what was clicked.
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
