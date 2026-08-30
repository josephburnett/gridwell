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
)

// The read-only rendered view: a singleton DOM overlay div — the textarea
// pattern, one element positioned over the focused rendered text descent each
// frame — whose innerHTML is markdown.RenderHTML's sanitized output (goldmark
// for markdown, go-org for org names). The canvas paints raw source for every
// non-focused view, and this div is the one styled surface.

// ensureRenderedView creates (once) the overlay div and its scoped
// stylesheet.
func (a *App) ensureRenderedView() {
	if a.renderedView.Truthy() {
		return
	}
	st := a.doc.Call("createElement", "style")
	st.Set("textContent", markdown.RenderedCSS("#gw-rendered-view"))
	a.doc.Get("head").Call("appendChild", st)

	div := a.doc.Call("createElement", "div")
	div.Set("id", "gw-rendered-view")
	s := div.Get("style")
	s.Set("position", "absolute")
	s.Set("display", "none")
	s.Set("overflow", "auto")
	s.Set("boxSizing", "border-box")
	s.Set("background", colorFileInnerBg)
	s.Set("zIndex", "5")
	s.Set("padding", "6px 10px")

	// Links never navigate the app page. An http(s) link opens as an
	// ephemeral visit in a split below — the one live-link vocabulary — and
	// everything else is inert. Task-list checkboxes are the one interactive
	// control: clicking one toggles its source marker through the normal
	// text-edit door.
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

	// Scroll writes back to the pane's TextScrollX/Y — the same fact the
	// textarea mirrors (previews and ascent-restore read the stored scroll).
	scrollCb := js.FuncOf(func(js.Value, []js.Value) any {
		p := a.tree.FocusedPane()
		if p == nil || p.ContentID() == "" || p.TextMode != rpc.TextModeRendered {
			return nil
		}
		p.TextScrollY = a.renderedView.Get("scrollTop").Float()
		p.TextScrollX = a.renderedView.Get("scrollLeft").Float()
		a.scheduleFileSave()
		return nil
	})
	div.Call("addEventListener", "scroll", scrollCb)

	a.doc.Get("body").Call("appendChild", div)
	a.renderedView = div
}

// refreshRenderedOverlay shows/positions/fills the rendered view for the
// focused pane, or hides it. Content is set only when the render key
// (tile id + version + org-ness) changes, so scrolling never re-renders.
func (a *App) refreshRenderedOverlay() {
	a.ensureRenderedView()
	div := a.renderedView
	hide := func() {
		div.Get("style").Set("display", "none")
		a.renderedReady = false
		a.lastRenderedKey = ""
	}
	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" {
		hide()
		return
	}
	t, ok := a.descendedTile(p)
	if !ok || t.Kind != rpc.KindText {
		hide()
		return
	}
	mode := p.TextMode
	if mode == "" {
		mode = rpc.TextModeRendered
	}
	// A read-only tile always shows its rendered, selectable face, whatever
	// mode the session carried in. This display-time guard makes
	// descentTextMode's rule hold from every entry point, including a
	// restored session.
	if a.tileReadOnly(&t) {
		mode = rpc.TextModeRendered
	}
	if mode != rpc.TextModeRendered {
		hide()
		return
	}
	body, ok := a.tileBody(&t)
	if !ok {
		hide() // canvas paints raw source until the fetch lands (issue #35 guard)
		return
	}
	r := a.barAwarePaneRect(p)
	if r.W <= 0 || r.H <= 0 {
		hide()
		return
	}
	x, y, w, h := textInnerBox(r)
	s := div.Get("style")
	setBoundsPx(s, x, y, w, h)
	// Content zoom rides the base font size (the CSS is em-relative).
	s.Set("fontSize", pxf(14*a.textScaleFor(p)))
	s.Set("display", "block")

	key := t.ID + "\x00" + strconv.FormatInt(t.Version, 10) + "\x00" +
		strconv.FormatBool(markdown.IsOrg(t.AltText)) + "\x00" + fmt.Sprint(len(body))
	if key != a.lastRenderedKey {
		div.Set("innerHTML", presentationHTML(&t, body))
		a.lastRenderedKey = key
		div.Set("scrollTop", p.TextScrollY)
		div.Set("scrollLeft", p.TextScrollX)
	}
	a.renderedReady = true
}

// onRenderedCheckboxClick toggles the task marker behind a clicked checkbox.
// The input's DOM position among the overlay's checkboxes is its
// document-order index; markdown.ToggleTask maps that index to the one source
// byte to flip, unit-tested and parity-pinned; and the edit rides the same
// cache entry and debounced flush a keystroke does, so there is no second
// write path. The overlay then re-renders from the toggled source, so what
// the checkbox shows is the document's truth rather than bare DOM state. A
// refused toggle preventDefaults so the native flip reverts: the box must not
// look saved when nothing was.
func (a *App) onRenderedCheckboxClick(ev, input js.Value) {
	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" {
		ev.Call("preventDefault")
		return
	}
	t, ok := a.descendedTile(p)
	if !ok || t.Kind != rpc.KindText || markdown.IsOrg(t.AltText) {
		// Org checkboxes render via go-org and have no source mapping here.
		ev.Call("preventDefault")
		return
	}
	if a.tileReadOnly(&t) {
		ev.Call("preventDefault")
		a.reportErr(errsurface.Info, "textedit",
			"this document is read-only — the checkbox was not changed")
		return
	}
	body, ok := a.tileBody(&t)
	if !ok {
		ev.Call("preventDefault")
		return
	}
	inputs := a.renderedView.Call("querySelectorAll", `input[type="checkbox"]`)
	idx := -1
	for i := 0; i < inputs.Length(); i++ {
		if inputs.Index(i).Equal(input) {
			idx = i
			break
		}
	}
	toggled, ok := markdown.ToggleTask(body, idx)
	if !ok {
		// The DOM index found no matching source marker: refuse loudly
		// rather than flip the wrong byte.
		ev.Call("preventDefault")
		a.reportErr(errsurface.Error, "textedit",
			"checkbox did not map to a task marker — nothing was changed")
		return
	}
	a.putEditedContent(t.ContentID(), toggled)
	a.scheduleFileSave()
	// Re-render from the toggled source: the render key won't change (same
	// tile, same version, same length), so force it.
	a.lastRenderedKey = ""
	a.refreshRenderedOverlay()
	a.draw()
}

// The overlay's stylesheet lives in markdown.RenderedCSS — one stylesheet
// shared with the rasterized grid preview, scoped per surface.

// presentationHTML routes a text body to its declared renderer: the plugin's
// text_presentation "plain" shows verbatim preformatted text
// (markdown.RenderPlainHTML), and everything else takes the document
// renderer. One router for the descent overlay and the grid preview
// rasterizer, so the two faces of a tile cannot disagree.
func presentationHTML(t *rpc.Tile, body []byte) string {
	if t.TextPresentation == rpc.TextPresentationPlain {
		return markdown.RenderPlainHTML(body)
	}
	return markdown.RenderHTML(body, markdown.IsOrg(t.AltText))
}
