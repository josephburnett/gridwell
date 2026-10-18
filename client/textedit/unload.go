package textedit

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// UnloadFlush is one dirty content entry's unload decision.
type UnloadFlush int

const (
	UnloadSkip   UnloadFlush = iota // a row that takes no content write must not write
	UnloadBeacon                    // beacon now with the returned claim
	// UnloadAsync: no claim exists to beacon with, so the async path, which
	// resolves the owner row, is the only way left.
	UnloadAsync
)

// DecideUnloadFlush decides whether a dying page writes a dirty content entry;
// what it claims is SaveClaim's. An unknown row still writes, because unload
// is the one flush with no next sweep behind it.
func DecideUnloadFlush(rowKnown, rowTakesBytes, rowOwnsContent bool, row, basis rpc.ContentBasis, haveBasis bool) (claim rpc.ContentBasis, do UnloadFlush) {
	if rowKnown && !rowTakesBytes {
		return rpc.ContentBasis{}, UnloadSkip
	}
	if !haveBasis && !rowKnown {
		return rpc.ContentBasis{}, UnloadAsync
	}
	return SaveClaim(rowKnown && rowOwnsContent, row, basis, haveBasis), UnloadBeacon
}

// Scroll is a text tile's persisted scroll, the SetTextScroll payload: where
// the doc is scrolled and the mode it shows. The box it is shown in is the
// viewer's geometry, so it is not here: two devices with different pane sizes
// would otherwise each rewrite the row on every look. Both scroll writers
// gate on ScrollMoved, because writing unconditionally would mutate updated_at
// and broadcast an event for a read.
type Scroll struct {
	X, Y int64
	Mode string
}

// ScrollOf is the tile's stored scroll.
func ScrollOf(t *gridwellv1.Tile) Scroll {
	return Scroll{X: t.TextX, Y: t.TextY, Mode: t.TextMode}
}

// ScrollChanged reports whether next differs from cur in any field.
func ScrollChanged(cur, next Scroll) bool { return cur != next }

// ShownScroll is the scroll a text row is already showing: the stored one,
// or for a never-framed row the top of the doc at DescentMode's mode. Both
// scroll writers diff against this, so a look stamps nothing.
func ShownScroll(stored Scroll, readOnly bool) Scroll {
	if stored != (Scroll{}) {
		return stored
	}
	return Scroll{Mode: DescentMode(ModeInput{TextDocument: true, ReadOnly: readOnly, Cached: true})}
}

// ScrollMoved is the one test both text scroll writers make: whether next
// differs from ShownScroll.
func ScrollMoved(stored, next Scroll, readOnly bool) bool {
	return ScrollChanged(ShownScroll(stored, readOnly), next)
}

// RowScroll is what a text pane writes to its tile row from the scroll it
// shows. The row's scroll is where the tile was left: the grid preview crops
// there and a fresh descent opens there. So leaving writes it whole, and so
// does a settle outside a pane tile; a settle inside one, whose layout owns
// the live scroll (pane.FramingOwner), takes only the mode, so a look or a
// focus tap on another device moves no one's scroll.
func RowScroll(stored, shown Scroll, layoutOwnsScroll, leaving bool) Scroll {
	if leaving || !layoutOwnsScroll {
		return shown
	}
	return Scroll{X: stored.X, Y: stored.Y, Mode: shown.Mode}
}

// ModeInput is everything the descent-mode decision reads.
type ModeInput struct {
	TextDocument bool // rpc.TextDocument: nothing else has a text mode
	ReadOnly     bool
	Cached       bool // the tile row is known, so Stored can be honored
	CursorURL    bool // the address encodes a text cursor
	Stored       string
}

// DescentMode is the one owner of which mode a text descent shows; descent
// and session restore both read it. A row that is not a text document has no
// mode, so "". A read-only tile is always rendered, never a caret over
// content the user cannot change.
func DescentMode(in ModeInput) string {
	if !in.TextDocument {
		return ""
	}
	if in.ReadOnly {
		return rpc.TextModeRendered
	}
	if in.CursorURL || !in.Cached || in.Stored == "" {
		return rpc.TextModeText
	}
	return in.Stored
}

// ShownMode is DescentMode's rule read at display time: a read-only row or no
// mode renders, so a restored "text" cannot open a textarea over content the
// user cannot change.
func ShownMode(paneMode string, readOnly bool) string {
	if readOnly || paneMode == "" {
		return rpc.TextModeRendered
	}
	return paneMode
}
