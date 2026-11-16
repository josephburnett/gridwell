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
	// GFM table, the dialect the parse half already speaks.
	got = RenderHTML([]byte("| a | b |\n|---|---|\n| 1 | 2 |"), false)
	if !strings.Contains(got, "<table") {
		t.Errorf("GFM table not rendered:\n%s", got)
	}
}

// A bare URL is a link whatever names its host, a reference to a node at
// 127.0.0.1 as one at its name, and a word that only looks like one is not.
func TestRenderHTMLLinksBareURLs(t *testing.T) {
	for in, link := range map[string]bool{
		"https://rtb.example.ts.net:10100/n1abcde/3/12": true,
		"http://127.0.0.1:38367/n1abcde/1/2":            true,
		"http://localhost:3000/?w=n1abcde%2F12":         true,
		"www.example.com/a":                             true,
		"http://nohost/a":                               false,
		"127.0.0.1:8080":                                false,
	} {
		got := RenderHTML([]byte("see "+in+" here"), false)
		if has := strings.Contains(got, `href="`); has != link {
			t.Errorf("%s: linked %v, want %v:\n%s", in, has, link, got)
		}
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

// Script injection through raw HTML, javascript: hrefs and event handlers must
// never reach the overlay's innerHTML, even with goldmark's safe defaults.
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

// RenderPlainHTML shows a body verbatim and escaped, so a shell comment stays
// a comment and the output is inert.
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
