package markdown

import (
	"strings"
	"testing"
)

func TestRenderHTMLMarkdown(t *testing.T) {
	got := RenderHTML([]byte("# Title\n\nsome *emphasis* and a [link](https://example.com)\n\n- [x] done"), false)
	for _, want := range []string{"<h1", "Title", "<em>emphasis</em>", `href="https://example.com"`, "<input", "checked"} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown render missing %q in:\n%s", want, got)
		}
	}
	// GFM table — the dialect the parse half already speaks.
	got = RenderHTML([]byte("| a | b |\n|---|---|\n| 1 | 2 |"), false)
	if !strings.Contains(got, "<table") {
		t.Errorf("GFM table not rendered:\n%s", got)
	}
}

func TestRenderHTMLOrg(t *testing.T) {
	got := RenderHTML([]byte("* Heading\n\nSome /italic/ text.\n\n- item one\n- item two"), true)
	for _, want := range []string{"Heading", "<em>italic</em>", "<li>"} {
		if !strings.Contains(got, want) {
			t.Errorf("org render missing %q in:\n%s", want, got)
		}
	}
}

// Sanitization is load-bearing even with goldmark's safe defaults: script
// injection through raw HTML, javascript: hrefs, and event handlers must
// never reach the overlay's innerHTML.
func TestRenderHTMLSanitizes(t *testing.T) {
	cases := []string{
		"<script>alert(1)</script>",
		"[x](javascript:alert(1))",
		"<img src=x onerror=alert(1)>",
	}
	for _, src := range cases {
		got := RenderHTML([]byte(src), false)
		if strings.Contains(got, "script") || strings.Contains(got, "onerror") || strings.Contains(got, "javascript:") {
			t.Errorf("unsafe output for %q:\n%s", src, got)
		}
	}
}

func TestRenderHTMLNeverBlank(t *testing.T) {
	if got := RenderHTML([]byte("plain text"), false); !strings.Contains(got, "plain text") {
		t.Errorf("plain text lost: %s", got)
	}
}

func TestIsOrg(t *testing.T) {
	for name, want := range map[string]bool{
		"notes.org": true, "NOTES.ORG": true, " todo.org ": true,
		"notes.md": false, "org": false, "": false, "orgfile": false,
	} {
		if IsOrg(name) != want {
			t.Errorf("IsOrg(%q) = %v, want %v", name, !want, want)
		}
	}
}

// RenderPlainHTML shows a body verbatim: escaped (inert by construction)
// and never interpreted as markdown — a shell comment stays a comment.
func TestRenderPlainHTML(t *testing.T) {
	out := RenderPlainHTML([]byte("# not a heading\n<script>x</script>"))
	if !strings.Contains(out, "# not a heading") {
		t.Errorf("plain body mangled: %q", out)
	}
	if strings.Contains(out, "<script>") || !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("plain body not escaped: %q", out)
	}
	if !strings.HasPrefix(out, "<pre") {
		t.Errorf("plain body not preformatted: %q", out)
	}
}
