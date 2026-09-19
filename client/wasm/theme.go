//go:build js && wasm

package main

import (
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/theme"
)

// The shim's half of client/theme: the palette reaches the DOM as --gw-
// custom properties, and the surfaces that hold a color of their own rather
// than reading one per frame — the rendered-document stylesheet and a live
// terminal — are restyled. The canvas needs nothing but a redraw.

// applyTheme installs t as the active palette everywhere something is already
// wearing a color. It is the one writer of a.pal, so no surface can be left in
// the palette before it.
func (a *App) applyTheme(t theme.Theme) {
	a.themeName = t
	a.pal = theme.Of(t)

	root := a.doc.Get("documentElement").Get("style")
	for _, v := range a.pal.CSSVars() {
		root.Call("setProperty", v.Name, v.Value)
	}

	if a.overlays.renderedStyle.Truthy() {
		a.overlays.renderedStyle.Set("textContent", markdown.RenderedCSS(renderedViewSel, a.pal))
	}
	// A rendered preview is rasterized in the palette it was made in, and
	// rasterprev.Key carries the theme, so those redraw by missing the cache.
	for _, pl := range a.locals {
		conn := pl.shellConn
		if conn == nil {
			continue
		}
		if conn.term.Truthy() {
			conn.term.Get("options").Set("theme", a.termTheme())
		}
		if conn.container.Truthy() {
			conn.container.Get("style").Set("background", a.pal.Bg)
		}
	}
	a.refreshRenderedOverlayBg()
	a.draw()
}

// refreshRenderedOverlayBg restyles the one DOM surface whose background is
// set at creation rather than per frame.
func (a *App) refreshRenderedOverlayBg() {
	if a.overlays.renderedView.Truthy() {
		a.overlays.renderedView.Get("style").Set("background", a.pal.FileInnerBg)
	}
	if a.overlays.textTextarea.Truthy() {
		st := a.overlays.textTextarea.Get("style")
		st.Set("color", a.pal.TextFg)
		st.Set("caretColor", a.pal.TextFg)
	}
}
