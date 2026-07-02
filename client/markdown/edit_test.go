package markdown

import "testing"

// editKeyOn lays out src and applies one key at caret — one editor keystroke
// against the same ops the painter would have drawn.
func editKeyOn(t *testing.T, src string, caret int, key string) KeyResult {
	t.Helper()
	r := layoutOf(t, src, 1000)
	return EditKey(src, caret, key, r.Ops, DefaultLayoutStyle(), monoMeasure)
}

func TestEditKeyTyping(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		caret     int
		key       string
		wantSrc   string
		wantCaret int
	}{
		{"printable inserts at caret", "hello", 2, "x", "hexllo", 3},
		{"typing in a heading stays in the heading", "# Title\n", 4, "x", "# Tixtle\n", 5},
		{"typing at doc end", "ab", 2, "c", "abc", 3},
		{"first keystroke of an empty doc", "", 0, "h", "h", 1},
		{"multibyte rune", "ab", 1, "é", "aéb", 3},
		{"space is printable", "ab", 1, " ", "a b", 2},
		{"tab inserts a literal tab", "ab", 1, "Tab", "a\tb", 2},
		{"backspace deletes one rune", "aéb", 3, "Backspace", "ab", 1},
		{"backspace at start is a no-op", "ab", 0, "Backspace", "ab", 0},
		{"delete removes forward", "ab", 0, "Delete", "b", 0},
		{"delete at end is a no-op", "ab", 2, "Delete", "ab", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := editKeyOn(t, c.src, c.caret, c.key)
			if !got.Handled {
				t.Fatal("not handled")
			}
			if got.Src != c.wantSrc || got.Caret != c.wantCaret {
				t.Errorf("= (%q,%d), want (%q,%d)", got.Src, got.Caret, c.wantSrc, c.wantCaret)
			}
			if got.Changed != (got.Src != c.src) {
				t.Errorf("Changed = %v with src %q -> %q", got.Changed, c.src, got.Src)
			}
		})
	}
}

// TestEditKeyEnter is the paragraph contract: Enter splices a normalized
// blank line in flowing text, a single newline in code, nothing in a table.
func TestEditKeyEnter(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		caret     int
		wantSrc   string
		wantCaret int
	}{
		{"end of paragraph makes a paragraph", "hello", 5, "hello\n\n", 7},
		{"mid-paragraph splits it", "one two", 3, "one\n\n two", 5},
		{"at an existing break: no accumulation, caret advances", "a\n\nb", 1, "a\n\nb", 3},
		{"heading end: next text is a plain paragraph", "# Title", 7, "# Title\n\n", 9},
		{"list item end: next text leaves the list", "- item", 6, "- item\n\n", 8},
		{"extra blank lines normalize", "a\n\n\n\nb", 2, "a\n\nb", 3},
		{"code block: one literal newline", "```\nab\n```\n", 6, "```\nab\n\n```\n", 7},
		{"code block end-inclusive edge", "```\nab\n```\n", 7, "```\nab\n\n```\n", 8},
		{"indented code: one literal newline", "    code", 8, "    code\n", 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := editKeyOn(t, c.src, c.caret, "Enter")
			if !got.Handled {
				t.Fatal("not handled")
			}
			if got.Src != c.wantSrc || got.Caret != c.wantCaret {
				t.Errorf("= (%q,%d), want (%q,%d)", got.Src, got.Caret, c.wantSrc, c.wantCaret)
			}
		})
	}
}

func TestEditKeyEnterInTableIsNoop(t *testing.T) {
	src := "| a | b |\n|---|---|\n| 1 | 2 |\n"
	got := editKeyOn(t, src, 2, "Enter") // caret on the "a" header cell
	if !got.Handled {
		t.Fatal("not handled (the key must not fall through to the browser)")
	}
	if got.Changed || got.Src != src {
		t.Errorf("table Enter changed the source: %q", got.Src)
	}
	// But Enter in a paragraph NEXT TO a table still works.
	src2 := "para\n\n" + src
	got2 := editKeyOn(t, src2, 2, "Enter") // mid-"para"
	if !got2.Changed || got2.Src != "pa\n\nra\n\n"+src {
		t.Errorf("Enter in the paragraph before a table = %q, want a split", got2.Src)
	}
}

// TestEditKeyArrowsSkipMarkers: horizontal movement walks caret stops, so the
// caret never enters the consumed ** markers around bold text.
func TestEditKeyArrowsSkipMarkers(t *testing.T) {
	src := "x **bold** y\n"
	// Right from the end of "x " jumps over the marker into "bold".
	if got := editKeyOn(t, src, 2, "ArrowRight"); got.Caret != 4 || got.Changed {
		t.Errorf("ArrowRight over marker: caret %d (changed=%v), want 4", got.Caret, got.Changed)
	}
	// Left from the start of "bold" jumps back out.
	if got := editKeyOn(t, src, 4, "ArrowLeft"); got.Caret != 2 {
		t.Errorf("ArrowLeft over marker: caret %d, want 2", got.Caret)
	}
	// At the edges movement clamps in place.
	if got := editKeyOn(t, src, 0, "ArrowLeft"); got.Caret != 0 {
		t.Errorf("ArrowLeft at start: caret %d, want 0", got.Caret)
	}
	if got := editKeyOn(t, src, len(src), "ArrowRight"); got.Caret != len(src) {
		t.Errorf("ArrowRight at end: caret %d, want %d", got.Caret, len(src))
	}
}

func TestEditKeyVerticalMovement(t *testing.T) {
	src := "alpha\n\nbeta\n"
	// From two runes into "alpha", down lands two runes into "beta" (same x).
	down := editKeyOn(t, src, 2, "ArrowDown")
	if down.Caret != 9 {
		t.Errorf("ArrowDown: caret %d, want 9 (two runes into beta)", down.Caret)
	}
	up := editKeyOn(t, src, 9, "ArrowUp")
	if up.Caret != 2 {
		t.Errorf("ArrowUp: caret %d, want 2", up.Caret)
	}
	// Down from the last line stays put (no line to land on may still resolve
	// to the same line's nearest boundary; it must not move backwards).
	last := editKeyOn(t, src, 9, "ArrowDown")
	if last.Caret < 9 {
		t.Errorf("ArrowDown at bottom moved backwards to %d", last.Caret)
	}
}

func TestEditKeyHomeEnd(t *testing.T) {
	src := "hello world  \nnext\n" // trailing spaces the renderer drops
	if got := editKeyOn(t, src, 7, "Home"); got.Caret != 0 {
		t.Errorf("Home: caret %d, want 0", got.Caret)
	}
	// End lands at the line's last typed column — past the dropped whitespace.
	if got := editKeyOn(t, src, 7, "End"); got.Caret != 13 {
		t.Errorf("End: caret %d, want 13 (end of line incl trailing spaces)", got.Caret)
	}
}

// TestEditKeyBackspaceJoinsParagraphsInTwoSteps documents the deletion
// contract at a paragraph break: rune-wise. The first Backspace turns the
// break into a soft break (the paragraphs join, separated by a rendered
// space); the second joins the words.
func TestEditKeyBackspaceJoinsParagraphsInTwoSteps(t *testing.T) {
	got := editKeyOn(t, "a\n\nb", 3, "Backspace")
	if got.Src != "a\nb" || got.Caret != 2 {
		t.Fatalf("first backspace = (%q,%d), want (%q,2)", got.Src, got.Caret, "a\nb")
	}
	got = editKeyOn(t, got.Src, got.Caret, "Backspace")
	if got.Src != "ab" || got.Caret != 1 {
		t.Fatalf("second backspace = (%q,%d), want (%q,1)", got.Src, got.Caret, "ab")
	}
}

func TestEditKeyUnhandledKeys(t *testing.T) {
	for _, key := range []string{"Escape", "PageUp", "PageDown", "F5", "Shift", "Control", "Dead"} {
		got := editKeyOn(t, "ab", 1, key)
		if got.Handled || got.Changed || got.Src != "ab" || got.Caret != 1 {
			t.Errorf("%s: = %+v, want untouched and unhandled", key, got)
		}
	}
}

// TestEditKeyTypingSessionSeam drives a whole typing session through the real
// pipeline — EditKey against a fresh Layout of the evolving source after
// every keystroke, exactly as the app re-renders between keys — and asserts
// the source AND that the caret stays mappable (a caret the painter cannot
// place is the "caret disappeared" bug).
func TestEditKeyTypingSessionSeam(t *testing.T) {
	keys := []string{"h", "i", "Enter", "w", "o", "w", "Enter", "Enter", "!", "Backspace"}
	src, caret := "", 0
	for i, key := range keys {
		r := layoutOf(t, src, 1000)
		res := EditKey(src, caret, key, r.Ops, DefaultLayoutStyle(), monoMeasure)
		if !res.Handled {
			t.Fatalf("step %d (%s): not handled", i, key)
		}
		src, caret = res.Src, res.Caret
		nr := layoutOf(t, src, 1000)
		if _, _, _, ok := PointFromCaret(nr.Ops, src, caret, DefaultLayoutStyle(), monoMeasure); !ok {
			t.Fatalf("step %d (%s): caret %d unmappable in %q", i, key, caret, src)
		}
	}
	// Two Enters at the same boundary produced ONE break (idempotent), so:
	if want := "hi\n\nwow\n\n"; src != want {
		t.Fatalf("session source = %q, want %q", src, want)
	}
	if caret != len(src) {
		t.Errorf("session caret = %d, want %d", caret, len(src))
	}
}
