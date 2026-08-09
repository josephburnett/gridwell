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
)

// The read-only rendered view (issue #218): source bytes → sanitized HTML
// string, handed by the wasm overlay code to a DOM div. The decision lives
// here, js-free and unit-tested (charter §5); goldmark — already this
// package's parser — does markdown, go-org (Hugo's org engine) does org
// files, and bluemonday sanitizes both as defense-in-depth (goldmark is
// safe-by-default: raw HTML in markdown is omitted, not passed through).

// gmRenderer shares the GFM configuration with gmParser: ONE dialect,
// whether the bytes are being lowered for alt-text derivation, rendered
// for the overlay, or scanned for task markers (tasklist.go).
var gmRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(
		util.Prioritized(taskCheckboxRenderer{}, 100),
	)),
)

// taskCheckboxRenderer overrides GFM's task-list checkbox renderer to emit
// the input WITHOUT `disabled`: the rendered view's checkboxes are
// interactive — clicking one toggles the source marker through the normal
// text-edit door (owner decision 2026-08-09, carving this one control out
// of #218's read-only rendered view; tasklist.go owns the mapping). A
// disabled input swallows clicks entirely, so it could never be a control.
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
func IsOrg(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".org")
}

// Renderable reports whether a name marks content this package can render
// (issue #236) — THE renderability rule: the fs plugin serves a
// renderable file's real bytes as the descent body (metadata otherwise),
// and the client colors file tiles by the same verdict (green vs grey),
// so what looks renderable and what actually renders can never disagree.
func Renderable(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(n, ".md") || strings.HasSuffix(n, ".markdown") || IsOrg(n)
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
