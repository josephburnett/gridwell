//go:build js && wasm

package main

import (
	"fmt"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// The read-only rendered view (issue #218): a singleton DOM overlay div —
// the textarea pattern, one element positioned over the focused rendered
// text descent each frame — whose innerHTML is markdown.RenderHTML's
// sanitized output (goldmark for markdown, go-org for .org names). The
// custom canvas markdown engine is gone; the canvas paints raw source for
// every non-focused view, and this div is the one styled surface.

// ensureRenderedView creates (once) the overlay div and its scoped
// stylesheet.
func (a *App) ensureRenderedView() {
	if a.renderedView.Truthy() {
		return
	}
	st := a.doc.Call("createElement", "style")
	st.Set("textContent", renderedCSS)
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

	// Links: never navigate the app page. An http(s) link opens as an
	// ephemeral visit in a split below — the one live-link vocabulary
	// (issue #207); everything else is inert.
	clickCb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		t := ev.Get("target")
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
		if p == nil || p.TextFocus == "" || p.TextMode != rpc.TextModeRendered {
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
	if p == nil || p.TextFocus == "" {
		hide()
		return
	}
	mode := p.TextMode
	if mode == "" {
		mode = rpc.TextModeRendered
	}
	if mode != rpc.TextModeRendered {
		hide()
		return
	}
	t, ok := a.descendedTile(p)
	if !ok || t.Kind != rpc.KindText {
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
		div.Set("innerHTML", markdown.RenderHTML(body, markdown.IsOrg(t.AltText)))
		a.lastRenderedKey = key
		div.Set("scrollTop", p.TextScrollY)
		div.Set("scrollLeft", p.TextScrollX)
	}
	a.renderedReady = true
}

// renderedCSS is the overlay's scoped stylesheet: goldmark emits bare
// semantic HTML, so the div supplies the reading style — the app's own
// dark palette, em-relative so content zoom scales everything.
const renderedCSS = `
#gw-rendered-view { color: #d8d9de; font-family: ui-sans-serif, system-ui, -apple-system, sans-serif; line-height: 1.5; }
#gw-rendered-view h1 { font-size: 1.7em; margin: 0.6em 0 0.4em; }
#gw-rendered-view h2 { font-size: 1.35em; margin: 0.6em 0 0.35em; }
#gw-rendered-view h3, #gw-rendered-view h4 { font-size: 1.15em; margin: 0.5em 0 0.3em; }
#gw-rendered-view p, #gw-rendered-view ul, #gw-rendered-view ol, #gw-rendered-view blockquote, #gw-rendered-view table, #gw-rendered-view pre { margin: 0.35em 0 0.6em; }
#gw-rendered-view ul, #gw-rendered-view ol { padding-left: 1.6em; }
#gw-rendered-view a { color: #7a9fd4; text-decoration: underline; cursor: pointer; }
#gw-rendered-view code { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; font-size: 0.92em; background: #1c1d24; padding: 0.08em 0.3em; border-radius: 3px; }
#gw-rendered-view pre { background: #1c1d24; padding: 0.6em 0.8em; border-radius: 4px; overflow-x: auto; }
#gw-rendered-view pre code { background: none; padding: 0; }
#gw-rendered-view blockquote { border-left: 3px solid #3a4b5a; padding-left: 0.8em; color: #9ca0ad; }
#gw-rendered-view table { border-collapse: collapse; }
#gw-rendered-view th, #gw-rendered-view td { border: 1px solid #3a4150; padding: 0.25em 0.6em; }
#gw-rendered-view img { max-width: 100%; }
#gw-rendered-view hr { border: 0; border-top: 1px solid #3a4150; }
#gw-rendered-view input[type=checkbox] { margin-right: 0.4em; }
`
