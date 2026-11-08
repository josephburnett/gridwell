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

// The read-only rendered view: source bytes to sanitized HTML for the wasm
// overlay's DOM div. goldmark does markdown, go-org does org files, and
// bluemonday sanitizes both as defense in depth, goldmark already omitting raw
// HTML by default.

// gmRenderer is the one GFM configuration, so the overlay's render and
// tasklist.go's marker scan read the same dialect.
var gmRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(
		util.Prioritized(taskCheckboxRenderer{}, 100),
	)),
)

// taskCheckboxRenderer drops `disabled`. Task-list checkboxes are the one
// interactive control in the read-only rendered view, and a disabled input
// swallows clicks.
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

// htmlPolicy is bluemonday's UGC policy plus the class and id attributes
// go-org leans on, and goldmark's task-list checkboxes.
var htmlPolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class", "id").Globally()
	p.AllowAttrs("type", "checked", "disabled").OnElements("input")
	return p
}()

// IsOrg reads the tile's user-visible name: a tile has no filename, so the
// name carries the type.
func IsOrg(name string) bool { return doctype.IsOrg(name) }

// Renderable and IsOrg re-export internal/doctype, the neutral home both sides
// of the source seam import, so classification and the render pipeline cannot
// disagree.
func Renderable(name string) bool { return doctype.Renderable(name) }

// RenderHTML renders source bytes to sanitized HTML; isOrg selects the org
// renderer. Errors degrade to an escaped <pre>, because a document must never
// render as nothing.
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

func renderFallback(src []byte) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return "<pre>" + esc.Replace(string(src)) + "</pre>"
}

// RenderPlainHTML serves text_presentation "plain". Nothing is interpreted as
// markdown, so a shell comment cannot become a heading, and the escaped source
// is inert HTML.
func RenderPlainHTML(src []byte) string {
	return `<pre class="gw-plain" style="margin:0;white-space:pre-wrap;word-break:break-word;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:0.9em;">` +
		html.EscapeString(string(src)) + `</pre>`
}
