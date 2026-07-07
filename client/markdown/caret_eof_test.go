package markdown

import "testing"

// Issue #91: a document ending in a link (or any construct whose trailing
// markup the renderer consumed — "](url)", a code fence, an embed) must keep
// EOF inside the caret's editable domain, so the user can arrow/click past the
// last visible glyph and type at the end. And a bare GFM autolink must be a
// mapped verbatim run — not an opaque span the caret skips entirely.

// runEnd returns SrcStart+SrcLen of the first op with the given text.
func runEnd(t *testing.T, r LayoutResult, text string) int {
	t.Helper()
	op, ok := opByText(r, text)
	if !ok {
		t.Fatalf("no %q op", text)
	}
	if op.SrcLen == 0 {
		t.Fatalf("%q op is opaque (SrcLen 0)", text)
	}
	return op.SrcStart + op.SrcLen
}

func TestCaretReachesEOFPastTrailingLink(t *testing.T) {
	src := "see [x](https://u)"
	r := layoutOf(t, src, 1000)
	xEnd := runEnd(t, r, "x") // end of the link's visible text

	if !IsCaretStop(r.Ops, src, len(src)) {
		t.Fatalf("EOF (offset %d) is not a caret stop", len(src))
	}
	// Offsets inside the consumed "](https://u)" are NOT stops.
	for o := xEnd + 1; o < len(src); o++ {
		if IsCaretStop(r.Ops, src, o) {
			t.Errorf("offset %d inside consumed link markup is a stop", o)
		}
	}
	// Arrow-right from the link text's end reaches EOF (not stranded).
	if got := NextCaretStop(r.Ops, src, xEnd); got != len(src) {
		t.Errorf("NextCaretStop(%d) = %d, want EOF %d", xEnd, got, len(src))
	}
	// And back again.
	if got := PrevCaretStop(r.Ops, src, len(src)); got != xEnd {
		t.Errorf("PrevCaretStop(EOF) = %d, want %d", got, xEnd)
	}
	// The EOF caret paints at the link text's right edge (consumed markup has
	// no glyphs, so no phantom advance).
	xOp, _ := opByText(r, "x")
	wantX := xOp.X + 8 // one mono rune
	if x, ok := caretX(t, r, src, len(src)); !ok || x != wantX {
		t.Errorf("PointFromCaret(EOF) = (%v, %v), want (%v, true)", x, ok, wantX)
	}
	// A click far to the right of the link lands at EOF.
	if got, ok := CaretFromPoint(r.Ops, src, xOp.X+1000, xOp.Y+1, monoMeasure); !ok || got != len(src) {
		t.Errorf("CaretFromPoint(far right) = (%d, %v), want (%d, true)", got, ok, len(src))
	}
}

func TestCaretStopsAfterTrailingLinkBeforeNewline(t *testing.T) {
	src := "[x](u)\n"
	r := layoutOf(t, src, 1000)
	xEnd := runEnd(t, r, "x")

	// The position right after the ")" (before the trailing newline) and EOF
	// are both stops; positions inside "](u)" are not.
	if !IsCaretStop(r.Ops, src, 6) {
		t.Error("offset 6 (after the consumed markup) is not a stop")
	}
	if !IsCaretStop(r.Ops, src, len(src)) {
		t.Error("EOF is not a stop")
	}
	for o := xEnd + 1; o < 6; o++ {
		if IsCaretStop(r.Ops, src, o) {
			t.Errorf("offset %d inside consumed link markup is a stop", o)
		}
	}
	if got := NextCaretStop(r.Ops, src, xEnd); got != 6 {
		t.Errorf("NextCaretStop(%d) = %d, want 6", xEnd, got)
	}
}

func TestBareAutolinkIsAMappedRun(t *testing.T) {
	src := "see https://example.com"
	r := layoutOf(t, src, 1000)
	op, ok := opByText(r, "https://example.com")
	if !ok {
		t.Fatal("no autolink op")
	}
	if op.SrcStart != 4 || op.SrcLen != len("https://example.com") {
		t.Fatalf("autolink source mapping = (%d, %d), want (4, %d)",
			op.SrcStart, op.SrcLen, len("https://example.com"))
	}
	// The caret can enter and traverse the url, and EOF is a stop (run end).
	if !IsCaretStop(r.Ops, src, 10) {
		t.Error("offset inside the autolink is not a stop")
	}
	if !IsCaretStop(r.Ops, src, len(src)) {
		t.Error("EOF at the autolink's end is not a stop")
	}
	if got := NextCaretStop(r.Ops, src, 4); got != 5 {
		t.Errorf("NextCaretStop(4) = %d, want 5", got)
	}
}

func TestWWWAutolinkShowsSourceTextButLinksWithProtocol(t *testing.T) {
	// goldmark prepends http:// to the HREF of a www autolink; the rendered
	// text must stay the verbatim source (that is what the caret maps into).
	src := "www.example.com"
	r := layoutOf(t, src, 1000)
	op, ok := opByText(r, "www.example.com")
	if !ok {
		t.Fatal("www autolink not rendered as its source text")
	}
	if op.SrcStart != 0 || op.SrcLen != len(src) {
		t.Fatalf("www autolink mapping = (%d, %d), want (0, %d)", op.SrcStart, op.SrcLen, len(src))
	}
	if op.Href != "http://www.example.com" {
		t.Errorf("href = %q, want the protocol-qualified url", op.Href)
	}
}

func TestAutolinkAfterLinkWithSameURLMapsToItself(t *testing.T) {
	// The consumed link DEST contains the same bytes as the later autolink —
	// the source-recovery search must not land inside the consumed markup.
	src := "[x](https://u.io) https://u.io"
	r := layoutOf(t, src, 1000)
	op, ok := opByText(r, "https://u.io")
	if !ok {
		t.Fatal("no autolink op")
	}
	want := len("[x](https://u.io) ")
	if op.SrcStart != want {
		t.Fatalf("autolink SrcStart = %d (inside the consumed dest?), want %d", op.SrcStart, want)
	}
}
