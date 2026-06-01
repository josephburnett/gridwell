package embed

import (
	"strings"
	"testing"
)

func TestClassifyDocTarget(t *testing.T) {
	tests := []struct {
		name string
		in   PaneState
		want DocTarget
	}{
		{
			name: "no text focus → not a doc target",
			in:   PaneState{HasTextFocus: false, TextMode: ModeRaw, Inside: true},
			want: DocTargetNone,
		},
		{
			name: "URL descent → not a doc target",
			in:   PaneState{HasTextFocus: true, IsURLDescent: true, TextMode: "", Inside: true},
			want: DocTargetNone,
		},
		{
			name: "outside inner box → not a doc target",
			in:   PaneState{HasTextFocus: true, TextMode: ModeRaw, Inside: false},
			want: DocTargetNone,
		},
		{
			name: "raw mode descent, inside → accept drop",
			in:   PaneState{HasTextFocus: true, TextMode: ModeRaw, Inside: true},
			want: DocTargetRaw,
		},
		{
			name: "rendered mode descent, inside → reject (read-only)",
			in:   PaneState{HasTextFocus: true, TextMode: ModeRendered, Inside: true},
			want: DocTargetRendered,
		},
		{
			name: "URL descent in rendered mode is still not a doc target",
			in:   PaneState{HasTextFocus: true, IsURLDescent: true, TextMode: ModeRendered, Inside: true},
			want: DocTargetNone,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyDocTarget(tc.in); got != tc.want {
				t.Errorf("ClassifyDocTarget(%+v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// Regression: I once shipped this with raw/rendered inverted. The
// table above protects against a swap, but pin the two named modes
// explicitly so the intent is clear.
func TestRawIsTheOnlyDropTarget(t *testing.T) {
	raw := ClassifyDocTarget(PaneState{HasTextFocus: true, TextMode: ModeRaw, Inside: true})
	rnd := ClassifyDocTarget(PaneState{HasTextFocus: true, TextMode: ModeRendered, Inside: true})
	if raw != DocTargetRaw {
		t.Errorf("raw classification = %d, want DocTargetRaw (%d)", raw, DocTargetRaw)
	}
	if rnd != DocTargetRendered {
		t.Errorf("rendered classification = %d, want DocTargetRendered (%d)", rnd, DocTargetRendered)
	}
	if raw == rnd {
		t.Fatal("raw and rendered must classify differently")
	}
}

func TestHrefForTile(t *testing.T) {
	cases := []struct {
		origin string
		id     int64
		want   string
	}{
		{"", 5, "/5"},
		{"http://localhost:8080", 5, "http://localhost:8080/5"},
		{"http://localhost:8080/", 5, "http://localhost:8080/5"}, // trailing slash trimmed
		{"https://gridwell.example.com", 12345, "https://gridwell.example.com/12345"},
	}
	for _, tc := range cases {
		got := HrefForTile(tc.origin, tc.id)
		if got != tc.want {
			t.Errorf("HrefForTile(%q,%d) = %q, want %q", tc.origin, tc.id, got, tc.want)
		}
	}
}

func TestLeafTileIDFromHref(t *testing.T) {
	cases := []struct {
		href string
		want int64
	}{
		{"/5", 5},
		{"/3/4/5", 5}, // descent path → leaf
		{"/12345", 12345},
		// Absolute URLs — the path component is what matters.
		{"http://localhost:8080/5", 5},
		{"http://localhost:8080/3/4/5", 5},
		{"https://gridwell.example.com/42", 42},
		{"", 0},
		{"https://example.com", 0},      // external URL, no path
		{"example.com/5", 0},             // missing leading slash and no scheme
		{"#anchor", 0},
		{"mailto:x@example.com", 0},
		{"/notanumber", 0},
		{"/", 0},
		{"/0", 0}, // tile id must be positive
		{"  /5  ", 5}, // trims whitespace
		{"http://localhost:8080/path/notnumeric", 0},
	}
	for _, tc := range cases {
		t.Run(tc.href, func(t *testing.T) {
			if got := LeafTileIDFromHref(tc.href); got != tc.want {
				t.Errorf("LeafTileIDFromHref(%q) = %d, want %d", tc.href, got, tc.want)
			}
		})
	}
}

func TestDimensions(t *testing.T) {
	cases := []struct {
		name                       string
		cellsW, cellsH             int64
		cellPx, defaultW, defaultH int
		wantW, wantH               int
	}{
		{"3x2 at 64px", 3, 2, 64, 192, 128, 192, 128},
		{"1x1 at 64px", 1, 1, 64, 192, 128, 64, 64},
		{"10x8 at 64px", 10, 8, 64, 192, 128, 640, 512},
		{"zero cells falls back to defaults", 0, 0, 64, 192, 128, 192, 128},
		{"zero W only → only W defaults", 0, 2, 64, 192, 128, 192, 128},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := Dimensions(tc.cellsW, tc.cellsH, tc.cellPx, tc.defaultW, tc.defaultH)
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Errorf("Dimensions(%d,%d,%d,%d,%d) = %d,%d; want %d,%d",
					tc.cellsW, tc.cellsH, tc.cellPx, tc.defaultW, tc.defaultH,
					gotW, gotH, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestMarkdown(t *testing.T) {
	cases := []struct {
		origin string
		id     int64
		alt    string
		want   string
	}{
		{"", 5, "first heading", "[first heading](/5)"},
		{"http://localhost:8080", 5, "Tab Title", "[Tab Title](http://localhost:8080/5)"},
		{"http://localhost:8080", 5, "", "[](http://localhost:8080/5)"}, // empty alt allowed
	}
	for _, tc := range cases {
		got := Markdown(tc.origin, tc.id, tc.alt)
		if got != tc.want {
			t.Errorf("Markdown(%q,%d,%q) = %q, want %q",
				tc.origin, tc.id, tc.alt, got, tc.want)
		}
	}
}

func TestDefaultAlt(t *testing.T) {
	if got := DefaultAlt("text", 5); got != "text tile 5" {
		t.Errorf("DefaultAlt = %q", got)
	}
}

func TestInsert(t *testing.T) {
	cases := []struct {
		name        string
		src         string
		off         int
		link        string
		want        string
	}{
		{
			name: "into empty doc",
			src:  "",
			off:  0,
			link: "[link]",
			want: "[link]",
		},
		{
			name: "at start of word — needs trailing space",
			src:  "hello",
			off:  0,
			link: "[link]",
			want: "[link] hello",
		},
		{
			name: "at end of word — needs leading space",
			src:  "hello",
			off:  5,
			link: "[link]",
			want: "hello [link]",
		},
		{
			name: "in the middle of a word — both sides pad",
			src:  "helloworld",
			off:  5,
			link: "[link]",
			want: "hello [link] world",
		},
		{
			name: "between two spaces — no padding either side",
			src:  "hello  world",
			off:  6,
			link: "[link]",
			want: "hello [link] world",
		},
		{
			name: "between two newlines — no padding either side",
			src:  "hello\n\nworld",
			off:  6,
			link: "[link]",
			want: "hello\n[link]\nworld",
		},
		{
			name: "after newline, before word — pads after only",
			src:  "hello\nworld",
			off:  6,
			link: "[link]",
			want: "hello\n[link] world",
		},
		{
			name: "negative offset clamps to 0",
			src:  "hello",
			off:  -5,
			link: "[link]",
			want: "[link] hello",
		},
		{
			name: "offset past end clamps to len",
			src:  "hi",
			off:  999,
			link: "[link]",
			want: "hi [link]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Insert(tc.src, tc.link, tc.off); got != tc.want {
				t.Errorf("Insert(%q, %q, %d) = %q, want %q",
					tc.src, tc.link, tc.off, got, tc.want)
			}
		})
	}
}

func TestLineEndOffset(t *testing.T) {
	src := "one\ntwo\nthree\nfour"
	cases := []struct {
		row  int
		want int
	}{
		{0, 3},  // end of "one"
		{1, 7},  // end of "two"
		{2, 13}, // end of "three"
		{3, len(src)},
		{4, len(src)},  // past last line
		{99, len(src)}, // way past
		{-1, 3},        // negative clamps to row 0
	}
	for _, tc := range cases {
		got := LineEndOffset(src, tc.row)
		if got != tc.want {
			t.Errorf("LineEndOffset(%q, %d) = %d, want %d", src, tc.row, got, tc.want)
		}
	}
}

func TestLineEndOffsetEmptyAndTrailingNewline(t *testing.T) {
	if got := LineEndOffset("", 0); got != 0 {
		t.Errorf("empty src row 0 = %d, want 0", got)
	}
	// Trailing newline: the "third line" is empty but exists.
	src := "a\nb\n"
	if got := LineEndOffset(src, 0); got != 1 {
		t.Errorf("row 0 of %q = %d, want 1", src, got)
	}
	if got := LineEndOffset(src, 1); got != 3 {
		t.Errorf("row 1 of %q = %d, want 3", src, got)
	}
	if got := LineEndOffset(src, 2); got != len(src) {
		t.Errorf("row 2 of %q = %d, want %d", src, got, len(src))
	}
}

func TestRowAt(t *testing.T) {
	cases := []struct {
		name                   string
		innerY, sy, scrollY, h float64
		want                   int
	}{
		{"first line, no scroll", 100, 105, 0, 20, 0},
		{"second line", 100, 125, 0, 20, 1},
		{"with scroll, second line visible at top of pane", 100, 100, 20, 20, 1},
		{"above inner box clamps to row 0", 100, 50, 0, 20, 0},
		{"line height 0 returns 0 (no division by zero)", 100, 200, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RowAt(tc.innerY, tc.sy, tc.scrollY, tc.h); got != tc.want {
				t.Errorf("RowAt = %d, want %d", got, tc.want)
			}
		})
	}
}

// End-to-end through the small layer: a drop at a known coordinate
// inside a known document produces the expected resulting source.
// This is the kind of test that would have caught the textarea-sync
// bug if the sync layer had been a function rather than a DOM call —
// future work to also wrap that.
func TestInsertAtComputedOffset(t *testing.T) {
	src := "# heading\n\nsome body text\n"
	// Drop on the heading line.
	off := LineEndOffset(src, 0)
	if off != len("# heading") {
		t.Fatalf("offset = %d, want %d", off, len("# heading"))
	}
	link := Markdown("http://localhost:8080", 5, "first heading")
	got := Insert(src, link, off)
	wantPrefix := "# heading [first heading](http://localhost:8080/5)"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("got = %q, want prefix %q", got, wantPrefix)
	}
	wantSuffix := "\n\nsome body text\n"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("got = %q, want suffix %q", got, wantSuffix)
	}
}
