//go:build js && wasm

package main

import (
	"fmt"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/textedit"
)

// The read-only rendered view: one DOM overlay div positioned over the
// focused rendered text descent each frame, whose innerHTML is
// markdown.RenderHTML's sanitized output. The canvas paints raw source for
// every non-focused view, and this div is the one styled surface.

// renderedViewSel scopes the overlay's stylesheet; the rasterized preview
// wears the same rules under its own root class.
const renderedViewSel = "#gw-rendered-view"

// ensureRenderedView creates the overlay div and its scoped stylesheet.
func (a *App) ensureRenderedView() {
	if a.overlays.renderedView.Truthy() {
		return
	}
	st := a.doc.Call("createElement", "style")
	st.Set("textContent", markdown.RenderedCSS(renderedViewSel, a.pal))
	a.doc.Get("head").Call("appendChild", st)
	a.overlays.renderedStyle = st

	div := a.doc.Call("createElement", "div")
	div.Set("id", "gw-rendered-view")
	s := div.Get("style")
	s.Set("position", "absolute")
	s.Set("display", "none")
	s.Set("overflow", "auto")
	s.Set("boxSizing", "border-box")
	s.Set("background", a.pal.FileInnerBg)
	s.Set("zIndex", "5")
	s.Set("padding", "6px 10px")

	// Links never navigate the app page: an http(s) link opens as an
	// ephemeral visit below, the one live-link vocabulary, and everything
	// else is inert. Task-list checkboxes are the one interactive control.
	clickCb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		t := ev.Get("target")
		if t.Truthy() && t.Get("tagName").String() == "INPUT" {
			a.onRenderedCheckboxClick(ev, t)
			return nil
		}
		for t.Truthy() && t.Get("tagName").String() != "A" {
			t = t.Get("parentElement")
		}
		if !t.Truthy() {
			return nil
		}
		ev.Call("preventDefault")
		href := t.Get("href").String()
		if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
			a.openLinkBelow(a.tree.Focus, href)
		}
		return nil
	})
	div.Call("addEventListener", "click", clickCb)

	// Scroll writes back to the pane's TextScrollX/Y, the same fact the
	// textarea mirrors.
	scrollCb := js.FuncOf(func(js.Value, []js.Value) any {
		p := a.tree.FocusedPane()
		if p == nil || p.ContentID() == "" || p.TextMode != rpc.TextModeRendered {
			return nil
		}
		p.TextScrollY = a.overlays.renderedView.Get("scrollTop").Float()
		p.TextScrollX = a.overlays.renderedView.Get("scrollLeft").Float()
		a.scheduleFileSave()
		return nil
	})
	div.Call("addEventListener", "scroll", scrollCb)

	a.doc.Get("body").Call("appendChild", div)
	a.overlays.renderedView = div
}

// refreshRenderedOverlay shows, positions and fills the rendered view for the
// focused pane, or hides it. Content is set only when the render key changes,
// so scrolling never re-renders.
func (a *App) refreshRenderedOverlay() {
	a.ensureRenderedView()
	div := a.overlays.renderedView
	hide := func() {
		div.Get("style").Set("display", "none")
		a.overlays.renderedReady = false
		a.overlays.lastRenderedKey = ""
	}
	p, t, r, d := a.focusedTextDescent()
	if t == nil || d.Mode != rpc.TextModeRendered {
		hide()
		return
	}
	body, ok := a.tileBody(t)
	if !ok {
		hide() // the canvas paints raw source until the fetch lands
		return
	}
	if r.W <= 0 || r.H <= 0 {
		hide()
		return
	}
	x, y, w, h := textInnerBox(r)
	s := div.Get("style")
	setBoundsPx(s, x, y, w, h)
	// The CSS is em-relative, so content zoom rides the base font size.
	s.Set("fontSize", pxf(14*a.textScaleFor(p)))
	s.Set("display", "block")

	key := t.Id + "\x00" + strconv.FormatInt(t.Version, 10) + "\x00" +
		strconv.FormatBool(markdown.IsOrg(t.AltText)) + "\x00" + fmt.Sprint(len(body))
	if key != a.overlays.lastRenderedKey {
		div.Set("innerHTML", textedit.PresentationHTML(t, body))
		a.overlays.lastRenderedKey = key
		div.Set("scrollTop", p.TextScrollY)
		div.Set("scrollLeft", p.TextScrollX)
	}
	a.overlays.renderedReady = true
}

// onRenderedCheckboxClick toggles the task marker behind a clicked checkbox.
// markdown.ToggleTask maps the input's document-order index to the source
// byte, and the edit rides the same cache entry and debounced flush a
// keystroke does. A refused toggle preventDefaults, so the native flip
// reverts rather than looking saved.
func (a *App) onRenderedCheckboxClick(ev, input js.Value) {
	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" {
		ev.Call("preventDefault")
		return
	}
	t, ok := a.descendedTile(p)
	if !ok {
		ev.Call("preventDefault")
		return
	}
	body, cached := a.tileBody(t)
	// textedit.DecideCheckboxClick owns the refusals; the marker mapping below
	// is markdown.ToggleTask's own verdict.
	switch textedit.DecideCheckboxClick(rpc.TextDocument(t), markdown.IsOrg(t.AltText), a.tileReadOnly(t), cached) {
	case textedit.CheckboxRevert:
		ev.Call("preventDefault")
		return
	case textedit.CheckboxReadOnly:
		ev.Call("preventDefault")
		a.reportErr(errsurface.Info, "textedit",
			"this document is read-only — the checkbox was not changed")
		return
	}
	inputs := a.overlays.renderedView.Call("querySelectorAll", `input[type="checkbox"]`)
	idx := -1
	for i := 0; i < inputs.Length(); i++ {
		if inputs.Index(i).Equal(input) {
			idx = i
			break
		}
	}
	toggled, ok := markdown.ToggleTask(body, idx)
	if !ok {
		// No matching source marker: refuse loudly rather than flip the
		// wrong byte.
		ev.Call("preventDefault")
		a.reportErr(errsurface.Error, "textedit",
			"checkbox did not map to a task marker — nothing was changed")
		return
	}
	a.putEditedContent(rpc.ContentID(t), toggled)
	a.scheduleFileSave()
	// The render key does not change on a toggle, so force the re-render.
	a.overlays.lastRenderedKey = ""
	a.refreshRenderedOverlay()
	a.draw()
}
