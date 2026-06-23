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
		id     string
		want   string
	}{
		{"", "5", "/5"},
		{"http://localhost:8080", "5", "http://localhost:8080/5"},
		{"http://localhost:8080/", "5", "http://localhost:8080/5"}, // trailing slash trimmed
		{"https://gridwell.example.com", "12345", "https://gridwell.example.com/12345"},
		// UUID-qualified IDs: prefix is stripped.
		{"", "uuid-abc/5", "/5"},
		{"http://localhost:8080", "uuid-abc/42", "http://localhost:8080/42"},
	}
	for _, tc := range cases {
		got := HrefForTile(tc.origin, tc.id)
		if got != tc.want {
			t.Errorf("HrefForTile(%q,%s) = %q, want %q", tc.origin, tc.id, got, tc.want)
		}
	}
}

func TestLeafTileIDFromHref(t *testing.T) {
	cases := []struct {
		href string
		want string
	}{
		{"/5", "5"},
		{"/3/4/5", "5"}, // descent path → leaf
		{"/12345", "12345"},
		// Absolute URLs — the path component is what matters.
		{"http://localhost:8080/5", "5"},
		{"http://localhost:8080/3/4/5", "5"},
		{"https://gridwell.example.com/42", "42"},
		{"", ""},
		{"https://example.com", ""},     // external URL, no path
		{"example.com/5", ""},           // missing leading slash and no scheme
		{"#anchor", ""},
		{"mailto:x@example.com", ""},
		{"/notanumber", ""},
		{"/", ""},
		{"/0", ""},     // tile id must be positive
		{"  /5  ", "5"}, // trims whitespace
		{"http://localhost:8080/path/notnumeric", ""},
		// Non-numeric LEAF must not be an embed even when an ancestor segment
		// is numeric — the regression for external links being mis-classified.
		{"/2024/recap", ""},
		{"https://blog.example.com/2024/01/post", ""},
		{"/page/3/comments", ""},
		{"/5/", "5"},      // trailing slash tolerated
		{"/3/4/5?x=1", "5"}, // query stripped
		{"/-5", ""},      // negative leaf is not a tile id
	}
	for _, tc := range cases {
		t.Run(tc.href, func(t *testing.T) {
			if got := LeafTileIDFromHref(tc.href); got != tc.want {
				t.Errorf("LeafTileIDFromHref(%q) = %q, want %q", tc.href, got, tc.want)
			}
		})
	}
}

func TestMarkdown(t *testing.T) {
	cases := []struct {
		origin string
		id     string
		alt    string
		want   string
	}{
		{"", "5", "first heading", "[first heading](/5)"},
		{"http://localhost:8080", "5", "Tab Title", "[Tab Title](http://localhost:8080/5)"},
		{"http://localhost:8080", "5", "", "[](http://localhost:8080/5)"}, // empty alt allowed
		// UUID-qualified: prefix stripped in link.
		{"", "uuid-abc/5", "x", "[x](/5)"},
	}
	for _, tc := range cases {
		got := Markdown(tc.origin, tc.id, tc.alt)
		if got != tc.want {
			t.Errorf("Markdown(%q,%s,%q) = %q, want %q",
				tc.origin, tc.id, tc.alt, got, tc.want)
		}
	}
}

func TestDefaultAlt(t *testing.T) {
	if got := DefaultAlt("text", "5"); got != "text tile 5" {
		t.Errorf("DefaultAlt = %q", got)
	}
	// UUID-qualified: prefix stripped in display.
	if got := DefaultAlt("text", "uuid-abc/5"); got != "text tile 5" {
		t.Errorf("DefaultAlt (qualified) = %q", got)
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

func TestDecideTextareaSync(t *testing.T) {
	cases := []struct {
		name string
		in   TextareaSyncInput
		want TextareaSyncDecision
	}{
		{
			// The bug the user reported: descend into new tile 7 while
			// textarea still holds "old content" from tile 4 and the new
			// tile's blob hasn't arrived in the cache yet. The textarea
			// must clear so the user doesn't see 4's content as 7's
			// default; LastTileID must advance so the blob fetch's
			// follow-up call seeds rather than re-clears.
			name: "different tile, blob not cached → clear and advance",
			in: TextareaSyncInput{
				FocusedTileID: "7",
				LastTileID:    "4",
				CurrentValue:  "old content",
				BlobCached:    false,
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "",
				NewLastTileID: "7",
			},
		},
		{
			name: "different tile, blob cached → seed with content",
			in: TextareaSyncInput{
				FocusedTileID: "7",
				LastTileID:    "4",
				CurrentValue:  "old content",
				BlobCached:    true,
				BlobContent:   "tile 7 body",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "tile 7 body",
				NewLastTileID: "7",
			},
		},
		{
			name: `first focus (LastTileID ""), blob cached → seed`,
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "",
				CurrentValue:  "",
				BlobCached:    true,
				BlobContent:   "first focus body",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "first focus body",
				NewLastTileID: "5",
			},
		},
		{
			name: "same tile, textarea empty (post-toggle), blob cached → seed",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "",
				BlobCached:    true,
				BlobContent:   "tile 5 body",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "tile 5 body",
				NewLastTileID: "5",
			},
		},
		{
			name: "same tile, textarea non-empty → preserve typing",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "user just typed this",
				BlobCached:    true,
				BlobContent:   "stale cache content",
			},
			want: TextareaSyncDecision{
				SetValue:      false,
				NewLastTileID: "5",
			},
		},
		{
			name: "same tile, textarea empty, blob still loading → wait",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "",
				BlobCached:    false,
			},
			want: TextareaSyncDecision{
				SetValue:      false,
				NewLastTileID: "5",
			},
		},
		{
			name: "different tile, blob cached but empty (fresh tile) → clear",
			in: TextareaSyncInput{
				FocusedTileID: "9",
				LastTileID:    "4",
				CurrentValue:  "previous content",
				BlobCached:    true,
				BlobContent:   "",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "",
				NewLastTileID: "9",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideTextareaSync(tc.in)
			if got != tc.want {
				t.Errorf("DecideTextareaSync(%+v) = %+v, want %+v",
					tc.in, got, tc.want)
			}
		})
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
	link := Markdown("http://localhost:8080", "5", "first heading")
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


func TestEmbedDescentAllowed(t *testing.T) {
	cases := []struct {
		name          string
		hitTileID     string
		targetFound   bool
		targetGridID  string
		currentGridID string
		want          bool
	}{
		{"all gates pass", "5", true, "100", "100", true},
		{"empty tile id rejected", "", true, "100", "100", false},
		{"target not found rejected", "5", false, "100", "100", false},
		{"cross-grid target rejected", "5", true, "200", "100", false},
		{"empty id beats found+same-grid", "", true, "100", "100", false},
	}
	for _, c := range cases {
		if got := EmbedDescentAllowed(c.hitTileID, c.targetFound, c.targetGridID, c.currentGridID); got != c.want {
			t.Errorf("%s: EmbedDescentAllowed(%q,%v,%q,%q) = %v, want %v",
				c.name, c.hitTileID, c.targetFound, c.targetGridID, c.currentGridID, got, c.want)
		}
	}
}
