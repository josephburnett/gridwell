package markdown

import (
	"fmt"
	"strings"
)

// renderedCSSRules is the one reading stylesheet for rendered text: the
// focused overlay div (#gw-rendered-view) and the rasterized grid preview
// both wear it, scoped through RenderedCSS, so a preview can never drift
// from what the descent shows. goldmark emits bare semantic
// HTML; this supplies the app's dark reading style, em-relative so the
// base font size scales everything.
const renderedCSSRules = `
SCOPE { color: #d8d9de; font-family: ui-sans-serif, system-ui, -apple-system, sans-serif; line-height: 1.5; }
SCOPE h1 { font-size: 1.7em; margin: 0.6em 0 0.4em; }
SCOPE h2 { font-size: 1.35em; margin: 0.6em 0 0.35em; }
SCOPE h3, SCOPE h4 { font-size: 1.15em; margin: 0.5em 0 0.3em; }
SCOPE p, SCOPE ul, SCOPE ol, SCOPE blockquote, SCOPE table, SCOPE pre { margin: 0.35em 0 0.6em; }
SCOPE ul, SCOPE ol { padding-left: 1.6em; }
SCOPE a { color: #7a9fd4; text-decoration: underline; cursor: pointer; }
SCOPE code { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; font-size: 0.92em; background: #1c1d24; padding: 0.08em 0.3em; border-radius: 3px; }
SCOPE pre { background: #1c1d24; padding: 0.6em 0.8em; border-radius: 4px; overflow-x: auto; }
SCOPE pre code { background: none; padding: 0; }
SCOPE blockquote { border-left: 3px solid #3a4b5a; padding-left: 0.8em; color: #9ca0ad; }
SCOPE table { border-collapse: collapse; }
SCOPE th, SCOPE td { border: 1px solid #3a4150; padding: 0.25em 0.6em; }
SCOPE img { max-width: 100%; }
SCOPE hr { border: 0; border-top: 1px solid #3a4150; }
SCOPE input[type=checkbox] { margin-right: 0.4em; }
`

// RenderedCSS returns the rendered-view stylesheet scoped under sel.
func RenderedCSS(sel string) string {
	return strings.ReplaceAll(renderedCSSRules, "SCOPE", sel)
}

// PreviewSVG wraps an XML-serialized rendered body in an SVG foreignObject
// document — the standard way to rasterize styled HTML onto a canvas without
// a second layout engine. markdown.RenderHTML stays the one renderer and the
// preview draws its output as an image. xhtml must be well-formed XML: the
// wasm caller serializes
// the sanitized DOM through XMLSerializer, since foreignObject is an XML
// context and goldmark's HTML5 output (unclosed <br>, <img>) is not. The
// base font size is the overlay's 14px at scale 1; the caller's drawImage
// applies the preview scale.
func PreviewSVG(xhtml string, w, h float64, bg string) string {
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f">`+
			`<foreignObject width="100%%" height="100%%">`+
			`<div xmlns="http://www.w3.org/1999/xhtml" class="gw-md-root" `+
			`style="width:%.0fpx;box-sizing:border-box;padding:6px 10px;font-size:14px;background:%s">`+
			`<style>%s</style>%s</div></foreignObject></svg>`,
		w, h, w, bg, RenderedCSS(".gw-md-root"), xhtml)
}
