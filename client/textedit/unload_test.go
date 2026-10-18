package textedit

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
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
		// A link row's version tracks its placement, not the bytes.
		{"cached link row, no basis", true, true, false, 7, 0, false, 0, UnloadBeacon},
		{"cached read-only or non-text row", true, false, true, 7, 5, true, 0, UnloadSkip},
		// A dirty edit whose owner row was never cached.
		{"uncached row, basis", false, false, false, 0, 5, true, 5, UnloadBeacon},
		{"uncached row, no basis", false, false, false, 0, 0, false, 0, UnloadAsync},
	}
	for _, c := range cases {
		claim, do := DecideUnloadFlush(c.rowKnown, c.editable, c.owner, v(c.rowVersion), v(c.basis), c.haveBasis)
		if claim != v(c.wantClaim) || do != c.want {
			t.Errorf("%s: = (%+v, %v), want (%d, %v)", c.name, claim, do, c.wantClaim, c.want)
		}
	}
}

func TestScrollChangedCountsEveryField(t *testing.T) {
	cur := ScrollOf(&gridwellv1.Tile{TextX: 1, TextY: 2, TextMode: "text"})
	if ScrollChanged(cur, cur) {
		t.Fatal("identical framing must not write")
	}
	for name, next := range map[string]Scroll{
		"x":    {X: 9, Y: 2, Mode: "text"},
		"y":    {X: 1, Y: 3, Mode: "text"},
		"mode": {X: 1, Y: 2, Mode: "rendered"},
	} {
		if !ScrollChanged(cur, next) {
			t.Errorf("%s change must write", name)
		}
	}
}

// A never-framed row is not a framing but the absence of one: no scroll and no
// mode, and what the tile shows in its place is the top of the doc at the mode
// a descent picks with nothing stored. Both framing writers must diff against
// that, or the first ascent out of a document nobody has opened stamps it with
// a scroll and a mode the user never chose.
func TestLookingAtADocumentNeverStampsAScrollOnIt(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		mode := DescentMode(ModeInput{TextDocument: true, ReadOnly: readOnly, Cached: true})
		// What an ascent measures after a pure look: the top of the doc, the
		// mode the descent installed.
		if ScrollMoved(Scroll{}, Scroll{Mode: mode}, readOnly) {
			t.Errorf("read-only %v: a look stamped a scroll", readOnly)
		}
		other := rpc.TextModeRendered
		if mode == rpc.TextModeRendered {
			other = rpc.TextModeText
		}
		for name, next := range map[string]Scroll{
			"a scroll":   {Y: 120, Mode: mode},
			"a retoggle": {Mode: other},
		} {
			if !ScrollMoved(Scroll{}, next, readOnly) {
				t.Errorf("read-only %v: %s must still be written", readOnly, name)
			}
		}
	}
	// A framed row is its own framing, so every reframe of one is written.
	stored := Scroll{X: 1, Y: 2, Mode: rpc.TextModeText}
	if got := ShownScroll(stored, false); got != stored {
		t.Errorf("a framed row shows %+v, want the stored %+v", got, stored)
	}
}

func TestDescentModeTable(t *testing.T) {
	cases := []struct {
		name string
		in   ModeInput
		want string
	}{
		// Which rows are documents is rpc.TextDocument's question.
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

func TestShownModeIsDescentModeAtDisplayTime(t *testing.T) {
	cases := []struct {
		name     string
		paneMode string
		readOnly bool
		want     string
	}{
		{"text stays text", rpc.TextModeText, false, rpc.TextModeText},
		{"rendered stays rendered", rpc.TextModeRendered, false, rpc.TextModeRendered},
		{"no mode is rendered", "", false, rpc.TextModeRendered},
		{"read-only renders a stale text mode", rpc.TextModeText, true, rpc.TextModeRendered},
		{"read-only renders no mode", "", true, rpc.TextModeRendered},
	}
	for _, c := range cases {
		if got := ShownMode(c.paneMode, c.readOnly); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	// A descent's installed mode is shown as installed: the two rules agree
	// on every editable document.
	for _, stored := range []string{"", rpc.TextModeText, rpc.TextModeRendered} {
		in := ModeInput{TextDocument: true, Cached: true, Stored: stored}
		if got := ShownMode(DescentMode(in), false); got != DescentMode(in) {
			t.Errorf("stored %q: descent installs %q, display shows %q", stored, DescentMode(in), got)
		}
	}
}

// The pane's box is the viewer's geometry, not a fact about the tile: two
// devices open one tile in panes of different sizes, and neither may write the
// row for that alone, or each focus overwrites the other's scroll.
func TestAPaneSizeIsNotAReframe(t *testing.T) {
	stored := ScrollOf(&gridwellv1.Tile{TextX: 1, TextY: 2, TextW: 300, TextH: 400, TextMode: rpc.TextModeText})
	if ScrollMoved(stored, Scroll{X: 1, Y: 2, Mode: rpc.TextModeText}, false) {
		t.Error("a pane of another size reframed a row nobody scrolled")
	}
}

// Inside a workspace tile the layout owns the scroll, so a settle there takes only
// the mode to the row: a scroll writes nothing to it, a toggle does, and a
// never-framed row looked at is still left alone. Leaving is different: the
// row's scroll is where the tile was left, so an ascent writes it whole.
func TestInsideAWorkspaceASettleTakesTheModeAndALeaveTheScroll(t *testing.T) {
	stored := Scroll{X: 0, Y: 240, Mode: rpc.TextModeRendered}
	scrolled := Scroll{Y: 900, Mode: rpc.TextModeRendered}
	if ScrollMoved(stored, RowScroll(stored, scrolled, true, false), false) {
		t.Error("a settle inside a workspace tile reframed the row's scroll")
	}
	if got := RowScroll(stored, scrolled, true, true); got != scrolled {
		t.Errorf("leaving inside a workspace tile writes %+v, want the scroll left %+v", got, scrolled)
	}
	for _, leaving := range []bool{false, true} {
		if got := RowScroll(stored, scrolled, false, leaving); got != scrolled {
			t.Errorf("outside a workspace tile (leaving %v) writes %+v, want %+v", leaving, got, scrolled)
		}
	}
	toggled := Scroll{Y: 900, Mode: rpc.TextModeText}
	if got := RowScroll(stored, toggled, true, false); got != (Scroll{Y: 240, Mode: rpc.TextModeText}) {
		t.Errorf("a toggle settled inside a workspace tile writes %+v, want the row's scroll and the new mode", got)
	}
	shown := DescentMode(ModeInput{TextDocument: true, Cached: true})
	if ScrollMoved(Scroll{}, RowScroll(Scroll{}, Scroll{Y: 300, Mode: shown}, true, false), false) {
		t.Error("a settle inside a workspace tile stamped a never-framed row")
	}
	if ScrollMoved(Scroll{}, RowScroll(Scroll{}, Scroll{Mode: shown}, true, true), false) {
		t.Error("leaving a never-framed row unscrolled stamped it")
	}
}
