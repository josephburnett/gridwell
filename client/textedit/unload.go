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
	// resolves the owner row, is the only door left.
	UnloadAsync
)

// DecideUnloadFlush decides whether a dying page writes a dirty content entry;
// what it claims is SaveClaim's. An unknown row still writes, because unload
// is the one flush with no next sweep behind it.
func DecideUnloadFlush(rowKnown, rowTakesBytes, rowOwnsContent bool, rowVersion, basis int64, haveBasis bool) (claim int64, do UnloadFlush) {
	if rowKnown && !rowTakesBytes {
		return 0, UnloadSkip
	}
	if !haveBasis && !rowKnown {
		return 0, UnloadAsync
	}
	return SaveClaim(rowKnown && rowOwnsContent, rowVersion, basis, haveBasis), UnloadBeacon
}

// Framing is a text tile's persisted window, the SetTextView payload. Both
// framing writers gate on Reframes, because writing unconditionally
// would mutate updated_at and broadcast an event for a read.
type Framing struct {
	X, Y, W, H int64
	Mode       string
}

// FramingOf is the tile's stored framing.
func FramingOf(t *gridwellv1.Tile) Framing {
	return Framing{X: t.TextX, Y: t.TextY, W: t.TextW, H: t.TextH, Mode: t.TextMode}
}

// FramingChanged reports whether next differs from cur in any field.
func FramingChanged(cur, next Framing) bool { return cur != next }

// Box is the window a text tile is shown in: the descended pane's inner box,
// in doc px, which is the rectangle a framing writer measures.
type Box struct{ W, H int64 }

// ShownFraming is the framing a text row is already showing: the stored one,
// or for a never-framed row the top of the doc in its box at DescentMode's
// mode. Both framing writers diff against this, so a look stamps nothing.
func ShownFraming(stored Framing, box Box, readOnly bool) Framing {
	if stored != (Framing{}) {
		return stored
	}
	return Framing{W: box.W, H: box.H,
		Mode: DescentMode(ModeInput{TextDocument: true, ReadOnly: readOnly, Cached: true})}
}

// Reframes is the one test both text framing writers make: whether next
// differs from ShownFraming.
func Reframes(stored, next Framing, readOnly bool) bool {
	return FramingChanged(ShownFraming(stored, Box{W: next.W, H: next.H}, readOnly), next)
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
