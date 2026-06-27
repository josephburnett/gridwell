package markdown

import "testing"

// TestOpTextSourceRangeIsVerbatim is the core invariant of the rendered-mode
// caret threading: every OpText with SrcLen > 0 maps back to the exact source
// bytes it renders, so the caret can place itself at a real source offset.
// Opaque runs (SrcLen == 0: inline code, list markers, breaks) are exempt.
func TestOpTextSourceRangeIsVerbatim(t *testing.T) {
	srcs := []string{
		"hello world\n",
		"# A Heading\n\nA paragraph with some words.\n",
		"a **bold** and *italic* and a [link](/5) here\n",
		"first line\nsecond line\n", // soft break between
		"- one\n- two\n- three\n",
		"text with `code` inline and more text\n",
		"> a quote with words\n",
		"| a | b |\n|---|---|\n| 1 | 2 |\n",
	}
	for _, src := range srcs {
		t.Run(src, func(t *testing.T) {
			r := layoutOf(t, src, 1000) // wide so nothing wraps awkwardly
			for _, op := range opsOfKind(r, OpText) {
				if op.SrcLen == 0 {
					continue // opaque run, no mapping promised
				}
				if op.SrcStart < 0 || op.SrcStart+op.SrcLen > len(src) {
					t.Fatalf("op %q range [%d,%d) out of bounds (len=%d)",
						op.Text, op.SrcStart, op.SrcStart+op.SrcLen, len(src))
				}
				if got := src[op.SrcStart : op.SrcStart+op.SrcLen]; got != op.Text {
					t.Errorf("op text %q maps to source %q (range [%d,%d))",
						op.Text, got, op.SrcStart, op.SrcStart+op.SrcLen)
				}
			}
		})
	}
}

// TestBoldInnerTextMapsPastMarkers pins that the markers goldmark consumes
// (the ** of bold) are excluded from the run's source range: the caret lands on
// the visible word, not the syntax.
func TestBoldInnerTextMapsPastMarkers(t *testing.T) {
	src := "x **bold** y\n"
	r := layoutOf(t, src, 1000)
	var found bool
	for _, op := range opsOfKind(r, OpText) {
		if op.Text != "bold" {
			continue
		}
		found = true
		// "bold" sits at byte 4 in "x **bold** y" (after "x **").
		if op.SrcStart != 4 || op.SrcLen != 4 {
			t.Errorf("bold run range = [%d,%d), want [4,8)", op.SrcStart, op.SrcStart+op.SrcLen)
		}
	}
	if !found {
		t.Fatal("no OpText with text \"bold\" found")
	}
}
