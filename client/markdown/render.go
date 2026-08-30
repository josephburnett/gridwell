package markdown

import (
	"bytes"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/niklasfasching/go-org/org"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
	"html"

	"github.com/josephburnett/gridwell/internal/doctype"
)

// The read-only rendered view: source bytes → sanitized HTML string, handed
// by the wasm overlay code to a DOM div. The decision lives here, js-free and
// unit-tested. goldmark — already this package's parser — does markdown,
// go-org (Hugo's org engine) does org files, and bluemonday sanitizes both as
// defense in depth; goldmark is safe by default, omitting raw HTML in
// markdown rather than passing it through.

// gmRenderer shares the GFM configuration with gmParser: one dialect,
// whether the bytes are being lowered for alt-text derivation, rendered
// for the overlay, or scanned for task markers (tasklist.go).
var gmRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(
		util.Prioritized(taskCheckboxRenderer{}, 100),
	)),
)

// taskCheckboxRenderer overrides GFM's task-list checkbox renderer to emit
// the input without `disabled`: task-list checkboxes are the one interactive
// control in the otherwise read-only rendered view, and clicking one toggles
// the source marker through the normal text-edit door (tasklist.go owns the
// mapping). A disabled input swallows clicks entirely, so it could never be a
// control.
type taskCheckboxRenderer struct{}

func (taskCheckboxRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(east.KindTaskCheckBox, renderTaskCheckbox)
}

func renderTaskCheckbox(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	if node.(*east.TaskCheckBox).IsChecked {
		_, _ = w.WriteString(`<input checked="" type="checkbox"> `)
	} else {
		_, _ = w.WriteString(`<input type="checkbox"> `)
	}
	return ast.WalkContinue, nil
}

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
func IsOrg(name string) bool { return doctype.IsOrg(name) }

// Renderable / IsOrg are re-exports of internal/doctype — the neutral
// home both sides of the plugin seam import (the fs plugin must not
// depend on a client rendering package). One rule, re-exported so this
// package's render pipeline and the classification can never disagree.
func Renderable(name string) bool { return doctype.Renderable(name) }

// RenderHTML renders source bytes to sanitized HTML for the read-only
// rendered view. org selects the org-mode renderer; anything else is GFM
// markdown. Errors degrade to an escaped <pre> of the source: a document must
// never render as nothing.
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

// RenderPlainHTML presents a plain-text body — source code, logs, config:
// anything the owning plugin declares text_presentation "plain" — verbatim in
// a preformatted block. No markdown interpretation, so a shell comment can
// never become a heading, and escaped, so it is inert HTML by construction.
func RenderPlainHTML(src []byte) string {
	return `<pre class="gw-plain" style="margin:0;white-space:pre-wrap;word-break:break-word;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:0.9em;">` +
		html.EscapeString(string(src)) + `</pre>`
}
