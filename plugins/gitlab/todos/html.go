package todos

import (
	"bytes"
	"html"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// md renders GitLab-flavored markdown bodies. Raw HTML in a body is
// dropped (goldmark's default): the node sandboxes every served page,
// but the body is third-party text and the page is ours.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

var page = template.Must(template.New("todo").Parse(`<!doctype html>
<meta charset="utf-8">
<title>{{.Title}}</title>
<style>
  body { font: 15px/1.5 system-ui, sans-serif; margin: 0; padding: 1.5rem; color: #1f2328; background: #fff; }
  @media (prefers-color-scheme: dark) { body { color: #e6edf3; background: #0d1117; } a { color: #8ab4f8; } }
  h1 { font-size: 1.3rem; margin: 0 0 .5rem; }
  .badge { display: inline-block; padding: .1em .6em; border-radius: 1em; font-size: .8em; font-weight: 600; vertical-align: middle; }
  .pending { background: #fff3c4; color: #7a5a00; }
  .done { background: #d3f5dc; color: #0b5a2a; }
  .meta { color: #6b7280; font-size: .9em; margin-bottom: 1rem; }
  .meta span + span::before { content: " · "; }
  .body { border-top: 1px solid #d0d7de; padding-top: 1rem; }
  .body pre { overflow-x: auto; }
  .gone { color: #b42318; }
</style>
<h1>{{if .Ref}}<span class="ref">{{.Ref}}</span> {{end}}{{.Title}} <span class="badge {{.State}}">{{.State}}</span></h1>
<div class="meta">
  <span>{{.Action}}</span>
  {{if .Author}}<span>by {{.Author}}</span>{{end}}
  {{if .Project}}<span>{{.Project}}</span>{{end}}
  <span>{{.Created}}</span>
  {{if .TargetURL}}<span><a href="{{.TargetURL}}">open in GitLab</a></span>{{end}}
</div>
{{if .Body}}<div class="body">{{.Body}}</div>{{end}}
`))

// Page renders a todo's HTML page — the content the tile serves.
func Page(t *Todo) []byte {
	var body bytes.Buffer
	if strings.TrimSpace(t.Body) != "" {
		if err := md.Convert([]byte(t.Body), &body); err != nil {
			body.Reset()
			body.WriteString("<pre>" + html.EscapeString(t.Body) + "</pre>")
		}
	}
	var out bytes.Buffer
	_ = page.Execute(&out, map[string]any{
		"Title":     t.Title(),
		"Ref":       t.Ref(),
		"State":     t.State,
		"Action":    t.Action(),
		"Author":    t.Author.Name,
		"Project":   t.Project.PathWithNamespace,
		"Created":   t.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"),
		"TargetURL": t.TargetURL,
		"Body":      template.HTML(body.String()),
	})
	return out.Bytes()
}

// GonePage is the page for a key the memory does not hold: the node
// remembers the tile (a todo never disappears), but this process has
// not seen its content since it started — say so rather than 404 blank.
func GonePage(key string) []byte {
	return []byte(`<!doctype html><meta charset="utf-8"><title>not in memory</title>
<p class="gone">This todo (<code>` + html.EscapeString(key) + `</code>) is no longer in GitLab, or has not been seen since the provider started.</p>
`)
}
