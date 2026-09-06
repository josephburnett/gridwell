package textedit

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"testing"
)

func TestDecideUnloadFlush(t *testing.T) {
	cases := []struct {
		name                      string
		rowKnown, editable, owner bool
		rowVersion, basis         int64
		haveBasis                 bool
		wantClaim                 int64
		want                      UnloadFlush
	}{
		{"cached editable row, basis", true, true, true, 7, 5, true, 5, UnloadBeacon},
		{"cached editable row, no basis", true, true, true, 7, 0, false, 7, UnloadBeacon},
		// A link row lends its version to nothing: it tracks its own
		// placement, not the target's bytes (SaveClaim's rule).
		{"cached link row, no basis", true, true, false, 7, 0, false, 0, UnloadBeacon},
		{"cached read-only or non-text row", true, false, true, 7, 5, true, 0, UnloadSkip},
		// The lost-edit case this rule exists for: a dirty edit whose
		// owner row was never cached (a leaf link's foreign target).
		{"uncached row, basis", false, false, false, 0, 5, true, 5, UnloadBeacon},
		{"uncached row, no basis", false, false, false, 0, 0, false, 0, UnloadAsync},
	}
	for _, c := range cases {
		claim, do := DecideUnloadFlush(c.rowKnown, c.editable, c.owner, c.rowVersion, c.basis, c.haveBasis)
		if claim != c.wantClaim || do != c.want {
			t.Errorf("%s: = (%d, %v), want (%d, %v)", c.name, claim, do, c.wantClaim, c.want)
		}
	}
}

func TestFramingChangedCountsEveryField(t *testing.T) {
	cur := FramingOf(rpc.Tile{TextX: 1, TextY: 2, TextW: 300, TextH: 400, TextMode: "text"})
	if FramingChanged(cur, cur) {
		t.Fatal("identical framing must not write")
	}
	for name, next := range map[string]Framing{
		"x":    {X: 9, Y: 2, W: 300, H: 400, Mode: "text"},
		"w":    {X: 1, Y: 2, W: 301, H: 400, Mode: "text"},
		"h":    {X: 1, Y: 2, W: 300, H: 401, Mode: "text"},
		"mode": {X: 1, Y: 2, W: 300, H: 400, Mode: "rendered"},
	} {
		if !FramingChanged(cur, next) {
			t.Errorf("%s change must write", name)
		}
	}
}

func TestDescentModeTable(t *testing.T) {
	cases := []struct {
		name string
		in   ModeInput
		want string
	}{
		// Which rows are documents is rpc.Tile.TextDocument's question
		// (pinned there); here it is one input, and everything that is not
		// one has no text mode at all.
		{"not a document (url, shell, page tile)", ModeInput{Cached: true, Stored: "text"}, ""},
		{"read-only is rendered even with a cursor url", ModeInput{TextDocument: true, ReadOnly: true, Cached: true, CursorURL: true, Stored: "text"}, rpc.TextModeRendered},
		{"cursor url forces text", ModeInput{TextDocument: true, Cached: true, CursorURL: true, Stored: rpc.TextModeRendered}, rpc.TextModeText},
		{"stored mode honored", ModeInput{TextDocument: true, Cached: true, Stored: rpc.TextModeRendered}, rpc.TextModeRendered},
		{"never opened defaults to text", ModeInput{TextDocument: true, Cached: true}, rpc.TextModeText},
		{"uncached restore defaults to text", ModeInput{TextDocument: true, Cached: false, Stored: rpc.TextModeRendered}, rpc.TextModeText},
	}
	for _, c := range cases {
		if got := DescentMode(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
