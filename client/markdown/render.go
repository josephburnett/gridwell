package markdown

import (
	"bytes"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/niklasfasching/go-org/org"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// The read-only rendered view (issue #218): source bytes → sanitized HTML
// string, handed by the wasm overlay code to a DOM div. The decision lives
// here, js-free and unit-tested (charter §5); goldmark — already this
// package's parser — does markdown, go-org (Hugo's org engine) does org
// files, and bluemonday sanitizes both as defense-in-depth (goldmark is
// safe-by-default: raw HTML in markdown is omitted, not passed through).

// gmRenderer shares the GFM configuration with gmParser: ONE dialect,
// whether the bytes are being lowered for alt-text derivation or rendered
// for the overlay.
var gmRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// htmlPolicy is bluemonday's user-generated-content policy plus the class/
// id attributes go-org's output leans on (outline containers, headline
// anchors, footnotes) and goldmark's task-list checkboxes.
var htmlPolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class", "id").Globally()
	p.AllowAttrs("type", "checked", "disabled").OnElements("input")
	return p
}()

// IsOrg reports whether a tile's name marks it as an org-mode document —
// the one detection rule (a tile has no filename; its user-visible name is
// the alt text).
func IsOrg(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".org")
}

// RenderHTML renders source bytes to sanitized HTML for the read-only
// rendered view. org selects the org-mode renderer; anything else is
// GFM markdown. Errors degrade to an escaped <pre> of the source — a
// document must never render as nothing (charter §6).
func RenderHTML(src []byte, isOrg bool) string {
	var out string
	if isOrg {
		w := org.NewHTMLWriter()
		doc := org.New().Parse(bytes.NewReader(src), "")
		s, err := doc.Write(w)
		if err != nil {
			return renderFallback(src)
		}
		out = s
	} else {
		var buf bytes.Buffer
		if err := gmRenderer.Convert(src, &buf); err != nil {
			return renderFallback(src)
		}
		out = buf.String()
	}
	return htmlPolicy.Sanitize(out)
}

// renderFallback is the never-blank degradation: the raw source, escaped,
// in a <pre>.
func renderFallback(src []byte) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return "<pre>" + esc.Replace(string(src)) + "</pre>"
}
