package embed

import (
	"strings"
	"testing"
	"unicode/utf8"
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
			name: "rendered mode descent, inside → accept (insert at caret)",
			in:   PaneState{HasTextFocus: true, TextMode: ModeRendered, Inside: true},
			want: DocTargetRendered,
		},
		{
			name: "URL descent in rendered mode is still not a doc target",
			in:   PaneState{HasTextFocus: true, IsURLDescent: true, TextMode: ModeRendered, Inside: true},
			want: DocTargetNone,
		},
		{
			name: "read-only raw doc → reject",
			in:   PaneState{HasTextFocus: true, TextMode: ModeRaw, Inside: true, ReadOnly: true},
			want: DocTargetReject,
		},
		{
			name: "read-only rendered doc → reject",
			in:   PaneState{HasTextFocus: true, TextMode: ModeRendered, Inside: true, ReadOnly: true},
			want: DocTargetReject,
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

// Regression: I once shipped this with raw/rendered inverted. Pin the two
// editable modes explicitly — both are drop targets now (raw inserts at the
// line, rendered at the caret), but they must stay distinct so the wasm side
// computes the right insert offset for each.
func TestRawAndRenderedAreDistinctDropTargets(t *testing.T) {
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
		// Qualified IDs keep the plugin uuid so the link is globally routable.
		{"", "0123456789abcdef0123456789abcdef/5", "/0123456789abcdef0123456789abcdef/5"},
		{"http://localhost:8080", "0123456789abcdef0123456789abcdef/42", "http://localhost:8080/0123456789abcdef0123456789abcdef/42"},
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
		{"https://example.com", ""}, // external URL, no path
		{"example.com/5", ""},       // missing leading slash and no scheme
		{"#anchor", ""},
		{"mailto:x@example.com", ""},
		{"/notanumber", ""},
		{"/", ""},
		{"/0", ""},      // tile id must be positive
		{"  /5  ", "5"}, // trims whitespace
		{"http://localhost:8080/path/notnumeric", ""},
		// Non-numeric LEAF must not be an embed even when an ancestor segment
		// is numeric — the regression for external links being mis-classified.
		{"/2024/recap", ""},
		{"https://blog.example.com/2024/01/post", ""},
		{"/page/3/comments", ""},
		{"/5/", "5"},        // trailing slash tolerated
		{"/3/4/5?x=1", "5"}, // query stripped
		{"/-5", ""},         // negative leaf is not a tile id
		// Plugin-qualified links: "/<uuid>/<id>" → leaf id.
		{"/0123456789abcdef0123456789abcdef/42", "42"},
		{"http://localhost:8080/0123456789abcdef0123456789abcdef/7", "7"},
		{"/0123456789abcdef0123456789abcdef/3/4/5", "5"}, // qualified descent chain
		{"/0123456789abcdef0123456789abcdef", ""},        // a uuid alone is not a tile link
		{"/0123456789abcdef0123456789abcdef/x", ""},      // qualified but non-numeric leaf
		// Short plugin ids (7-char base36, leading letter — 2026-07-25) are
		// the second qualified shape; before isPluginID knew it, these fell
		// through to the bare-legacy branch, re-qualified with the embedding
		// doc's plugin, and silently resolved to the wrong tile.
		{"/k3x9m2q/42", "42"},
		{"http://localhost:8080/k3x9m2q/7", "7"},
		{"/k3x9m2q/3/4/5", "5"}, // qualified descent chain
		{"/k3x9m2q", ""},        // a plugin id alone is not a tile link
		{"/k3x9m2q/x", ""},      // qualified but non-numeric leaf
		// An external link whose first segment merely isn't a uuid stays an
		// external link — the regression a relaxed "non-numeric prefix" rule
		// would cause (a real link mis-rendered as a tile embed).
		{"/user/42", ""},     // 4 chars: neither id shape
		{"/username/42", ""}, // 8 chars: neither id shape
		{"/2024xyz/42", ""},  // 7 chars but leading digit: not a short id
		{"/K3X9M2Q/42", ""},  // uppercase: ids are lowercase-only
		{"https://github.com/owner/123", ""},
		// A near-uuid (wrong length / non-hex) is not a plugin uuid.
		{"/0123456789abcdef0123456789abcde/42", ""},  // 31 hex chars
		{"/0123456789abcdef0123456789abcdeg/42", ""}, // 'g' is not hex
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
		// Qualified: the plugin uuid is kept in the link.
		{"", "0123456789abcdef0123456789abcdef/5", "x", "[x](/0123456789abcdef0123456789abcdef/5)"},
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
		name string
		src  string
		off  int
		link string
		want string
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
			// The foreign-writer visibility rule: with NO pending edit the
			// buffer is a mere view of the cached body and must follow it.
			// Another device edited this tile; the event evicted the stale
			// body, the refetch landed the foreign bytes — the open editor
			// repaints. (Real typing always sets PendingEdit, so this input
			// combination IS the stale-view case; the old "non-empty →
			// preserve" rule kept the stale buffer, and the ascent flush
			// then saved it back over the foreign edit — the stomp.)
			name: "same tile, clean buffer differs from cache → follow the cache",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "stale buffer from before the foreign edit",
				BlobCached:    true,
				BlobContent:   "foreign edit, refetched",
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "foreign edit, refetched",
				NewLastTileID: "5",
			},
		},
		{
			// Clean buffer already matches the cache: no write, no churn (a
			// SetValue would move the caret/scroll for nothing).
			name: "same tile, clean buffer equals cache → leave alone",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "settled body",
				BlobCached:    true,
				BlobContent:   "settled body",
			},
			want: TextareaSyncDecision{
				SetValue:      false,
				NewLastTileID: "5",
			},
		},
		{
			// Deleting everything is an edit like any other: an empty DIRTY
			// buffer must not be "helpfully" reseeded from the cache — that
			// would resurrect the deleted text under the user's caret.
			name: "same tile, pending edit emptied the buffer → preserve",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "",
				BlobCached:    true,
				BlobContent:   "deleted content",
				PendingEdit:   true,
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
			// The fast-pane-switch case (issue #35): typing into tile 4 arms
			// the debounced save; switching to another text descent within
			// the debounce rebinds the textarea. The rebind simply seeds the
			// new tile — tile 4's typing already lives in ITS content-store
			// entry (every keystroke mirrors), and the dirty sweep posts it
			// regardless of where focus went. Nothing to rescue at the seam.
			name: "different tile with pending edit → rebind; the old edit is cache-owned",
			in: TextareaSyncInput{
				FocusedTileID: "7",
				LastTileID:    "4",
				CurrentValue:  "unsaved typing for 4",
				BlobCached:    true,
				BlobContent:   "tile 7 body",
				PendingEdit:   true,
			},
			want: TextareaSyncDecision{
				SetValue:      true,
				Value:         "tile 7 body",
				NewLastTileID: "7",
			},
		},
		{
			// Same tile: the buffer still belongs to the focused tile; the
			// debounced save owns persistence, not the rebind flush.
			name: "same tile with pending edit → no flush, preserve typing",
			in: TextareaSyncInput{
				FocusedTileID: "5",
				LastTileID:    "5",
				CurrentValue:  "user just typed this",
				BlobCached:    true,
				BlobContent:   "stale cache content",
				PendingEdit:   true,
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

func TestPlanEmbedDescent(t *testing.T) {
	cases := []struct {
		name                                   string
		hitTileID, targetGridID, currentGridID string
		want                                   EmbedDescent
	}{
		{
			// The doc and the target share a grid: focus / descend in place,
			// no re-anchor. (The same-grid text embed `[Inside](/9)`.)
			"same grid focuses in place",
			"uuid/9", "uuid/1", "uuid/1",
			EmbedDescent{OK: true},
		},
		{
			// The headline bug: a url tile (tile 12) lives in grid 4, embedded
			// in a doc in grid 1. The old gate rejected this; now we re-anchor
			// the pane onto grid 4 so the url renders, then descend.
			"cross grid re-anchors onto the target grid",
			"uuid/12", "uuid/4", "uuid/1",
			EmbedDescent{OK: true, Reanchor: true, Anchor: "uuid/4"},
		},
		{
			// Cross-plugin: the target's grid carries a different plugin uuid,
			// so re-anchoring there also switches plugins.
			"cross plugin re-anchors onto the other plugin's grid",
			"other/7", "other/3", "uuid/1",
			EmbedDescent{OK: true, Reanchor: true, Anchor: "other/3"},
		},
		{"unresolved href is not followed", "", "uuid/1", "uuid/1", EmbedDescent{}},
		{"missing target (uncached grid) is not followed", "uuid/9", "", "uuid/1", EmbedDescent{}},
	}
	for _, c := range cases {
		got := PlanEmbedDescent(c.hitTileID, c.targetGridID, c.currentGridID)
		if got.OK != c.want.OK || got.Reanchor != c.want.Reanchor || got.Anchor != c.want.Anchor {
			t.Errorf("%s: PlanEmbedDescent(%q,%q,%q) = %+v, want %+v",
				c.name, c.hitTileID, c.targetGridID, c.currentGridID, got, c.want)
		}
	}
}

func TestResolveEmbedTileID(t *testing.T) {
	cases := []struct {
		name   string
		anchor string
		href   string
		want   string
	}{
		{"bare leaf re-qualified with anchor", "uuid", "/42", "uuid/42"},
		{"absolute url re-qualified", "uuid", "http://localhost:8080/42", "uuid/42"},
		{"descent chain takes the leaf, re-qualified", "uuid", "/3/4/5", "uuid/5"},
		{"no anchor leaves the bare leaf", "", "/42", "42"},
		{"non-tile href resolves to empty", "uuid", "/blog/post", ""},
		{"empty href resolves to empty", "uuid", "", ""},
		// Qualified hrefs carry their own plugin uuid → resolve directly,
		// ignoring the anchor. This is what lets an embed descend regardless of
		// which plugin's doc holds it (the #4 fix).
		{"qualified href resolves directly", "anchoruuid", "/0123456789abcdef0123456789abcdef/42", "0123456789abcdef0123456789abcdef/42"},
		{"qualified href wins over a different anchor", "0000000000000000000000000000abcd", "http://localhost:8080/0123456789abcdef0123456789abcdef/9", "0123456789abcdef0123456789abcdef/9"},
		{"qualified descent chain takes the leaf, keeps its uuid", "anchoruuid", "/0123456789abcdef0123456789abcdef/3/4/5", "0123456789abcdef0123456789abcdef/5"},
	}
	for _, c := range cases {
		if got := ResolveEmbedTileID(c.anchor, c.href); got != c.want {
			t.Errorf("%s: ResolveEmbedTileID(%q, %q) = %q, want %q",
				c.name, c.anchor, c.href, got, c.want)
		}
	}
}

// TestInsertMultibyteRuneBoundary: a byte offset that lands in the middle of a
// multibyte rune must not corrupt it. Before snapping, the splice split "é"
// (0xC3 0xA9) and the padding check read the continuation byte as non-space.
func TestInsertMultibyteRuneBoundary(t *testing.T) {
	src := "héllo" // h, é(2 bytes at [1,3)), l, l, o
	got := Insert(src, "[x]", 2)
	if !utf8.ValidString(got) {
		t.Fatalf("Insert at mid-rune produced invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "é") {
		t.Errorf("é was corrupted: %q", got)
	}
	if !strings.Contains(got, "[x]") {
		t.Errorf("link missing: %q", got)
	}
	// A boundary offset still inserts exactly where asked.
	if Insert("ab", "[x]", 1) != "a [x] b" {
		t.Errorf("ascii boundary insert wrong: %q", Insert("ab", "[x]", 1))
	}
}
