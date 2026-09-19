package markdown

import (
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/client/theme"
)

// renderedCSSRules is the one reading stylesheet for rendered text. The
// focused overlay div and the rasterized grid preview both wear it, so a
// preview cannot drift from the descent. Sizes are em-relative. Colors are
// substituted rather than named as custom properties, because the preview
// raster is an SVG document of its own and inherits none from the page.
const renderedCSSRules = `
@SCOPE@ { color: @FG@; font-family: ui-sans-serif, system-ui, -apple-system, sans-serif; line-height: 1.5; }
@SCOPE@ h1 { font-size: 1.7em; margin: 0.6em 0 0.4em; }
@SCOPE@ h2 { font-size: 1.35em; margin: 0.6em 0 0.35em; }
@SCOPE@ h3, @SCOPE@ h4 { font-size: 1.15em; margin: 0.5em 0 0.3em; }
@SCOPE@ p, @SCOPE@ ul, @SCOPE@ ol, @SCOPE@ blockquote, @SCOPE@ table, @SCOPE@ pre { margin: 0.35em 0 0.6em; }
@SCOPE@ ul, @SCOPE@ ol { padding-left: 1.6em; }
@SCOPE@ a { color: @LINK@; text-decoration: underline; cursor: pointer; }
@SCOPE@ code { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; font-size: 0.92em; background: @CODEBG@; padding: 0.08em 0.3em; border-radius: 3px; }
@SCOPE@ pre { background: @CODEBG@; padding: 0.6em 0.8em; border-radius: 4px; overflow-x: auto; }
@SCOPE@ pre code { background: none; padding: 0; }
@SCOPE@ blockquote { border-left: 3px solid @QUOTEBAR@; padding-left: 0.8em; color: @QUOTEFG@; }
@SCOPE@ table { border-collapse: collapse; }
@SCOPE@ th, @SCOPE@ td { border: 1px solid @RULE@; padding: 0.25em 0.6em; }
@SCOPE@ img { max-width: 100%; }
@SCOPE@ hr { border: 0; border-top: 1px solid @RULE@; }
@SCOPE@ input[type=checkbox] { margin-right: 0.4em; }
`

// RenderedCSS returns the rendered-view stylesheet scoped under sel, in p's
// colors.
func RenderedCSS(sel string, p theme.Palette) string {
	r := strings.NewReplacer(
		"@SCOPE@", sel,
		"@FG@", p.DocFg,
		"@LINK@", p.DocLink,
		"@CODEBG@", p.DocCodeBg,
		"@QUOTEBAR@", p.DocQuoteBar,
		"@QUOTEFG@", p.DocQuoteFg,
		"@RULE@", p.DocRule,
	)
	return r.Replace(renderedCSSRules)
}

// PreviewSVG wraps a rendered body in an SVG foreignObject, which rasterizes
// styled HTML without a second layout engine, so RenderHTML stays the one
// renderer. xhtml must be well-formed XML, goldmark's HTML5 output leaving
// <br> and <img> unclosed, so the wasm caller serializes through
// XMLSerializer. The base font size is 14px at scale 1. The raster is made in
// the theme on screen, so switching theme re-rasterizes rather than leaving a
// dark document on a pale grid; rasterprev.Key carries the theme that decides.
func PreviewSVG(xhtml string, w, h float64, p theme.Palette) string {
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f">`+
			`<foreignObject width="100%%" height="100%%">`+
			`<div xmlns="http://www.w3.org/1999/xhtml" class="gw-md-root" `+
			`style="width:%.0fpx;box-sizing:border-box;padding:6px 10px;font-size:14px;background:%s">`+
			`<style>%s</style>%s</div></foreignObject></svg>`,
		w, h, w, p.FileInnerBg, RenderedCSS(".gw-md-root", p), xhtml)
}
