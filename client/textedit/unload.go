package textedit

import "github.com/josephburnett/gridwell/api/rpc"

// UnloadFlush is one dirty content entry's unload decision.
type UnloadFlush int

const (
	// UnloadSkip: the entry must not write (a non-text or read-only row).
	UnloadSkip UnloadFlush = iota
	// UnloadBeacon: beacon now with the returned version claim.
	UnloadBeacon
	// UnloadAsync: no claim exists to beacon with — the async path (which
	// resolves the owner row) is the only door left.
	UnloadAsync
)

// DecideUnloadFlush is the ONE rule for what a dying page does with a
// dirty text body (audit #8). The subtle case is rowKnown=false: the
// owner row living in no cached grid is NOT a dead end (audit #6 — a
// leaf link's target in a never-fetched foreign grid), because the
// SaveBasis alone is the claim, and only editable text ever becomes
// dirty; the server issues the verdict either way. The unload path used
// to skip that case silently — the one flush with no next sweep behind
// it, so the edit was guaranteed lost.
func DecideUnloadFlush(rowKnown, rowEditableText bool, rowVersion int64, basis int64, haveBasis bool) (claim int64, do UnloadFlush) {
	if rowKnown && !rowEditableText {
		return 0, UnloadSkip
	}
	if haveBasis {
		return basis, UnloadBeacon
	}
	if rowKnown {
		return rowVersion, UnloadBeacon
	}
	return 0, UnloadAsync
}

// Framing is a text tile's persisted window: scroll, size, and mode —
// the SetTextView payload. FramingOf reads it off a tile; Changed is the
// ONE rule both framing writers (the settle-persist and the ascent save)
// gate on, so a pure descend-and-ascent, or a resize that only changed
// the window size, writes exactly when something differs (2026-08-27: the
// settle path ignored W/H and the ascent path wrote unconditionally —
// reading mutated updated_at and broadcast an event).
type Framing struct {
	X, Y, W, H int64
	Mode       string
}

// FramingOf is the tile's stored framing.
func FramingOf(t rpc.Tile) Framing {
	return Framing{X: t.TextX, Y: t.TextY, W: t.TextW, H: t.TextH, Mode: t.TextMode}
}

// FramingChanged reports whether next differs from cur in any field.
func FramingChanged(cur, next Framing) bool { return cur != next }

// ModeInput is everything the descent-mode decision reads.
type ModeInput struct {
	Kind       string
	ServesPage bool
	ReadOnly   bool
	// Cached: the tile row is known (an uncached restore has no stored
	// mode to honor).
	Cached bool
	// CursorURL: the address encodes a text cursor — the user was
	// editing; text mode, unless the tile is read-only.
	CursorURL bool
	Stored    string
}

// DescentMode is the ONE owner of which mode a text descent shows. URL
// and serves_page tiles have no text/rendered mode ("": the textarea
// overlay never shows). A READ-ONLY text tile always shows RENDERED —
// never a caret over content the user can't change, and rendered is the
// selectable DOM surface (#268). A cursor URL forces text. Otherwise the
// stored mode, defaulting to raw text for a never-opened or uncached
// tile. Every path that decides a text mode — descent, session restore —
// reads this, never re-derives it (2026-08-27: the restore path carried
// two extra arms of its own).
func DescentMode(in ModeInput) string {
	if in.Kind != rpc.KindText || in.ServesPage {
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
