// Package contentzoom owns the content-zoom policy: the chord keys and their
// steps, the range a zoom is clamped into, and what one press does to the
// tile a pane is descended into. The wasm shim is the hands.
package contentzoom

import "github.com/josephburnett/gridwell/api/rpc"

const (
	// Step is one press; Min and Max bound the result.
	Step = 1.1
	Min  = 0.5
	Max  = 3.0

	shellBaseFontPx = 13.0
)

// The chord's keys, "=" being the unshifted key "+" lives on. A live url view
// owns OS keyboard focus, so apps/desktop/src/main/viewutil.ts knows the set
// too and forwards the press; the drift lint in gesture-threshold.test.ts pins
// that copy to these.
const (
	KeyIn    = "+"
	KeyInAlt = "="
	KeyOut   = "-"
	KeyReset = "0"
)

// Of reads a tile's stored zoom. Zero is a row that was never zoomed.
func Of(stored float64) float64 {
	if stored > 0 {
		return stored
	}
	return 1
}

// Clamp holds a zoom inside [Min, Max].
func Clamp(z float64) float64 {
	if z < Min {
		return Min
	}
	if z > Max {
		return Max
	}
	return z
}

// ShellFontPx is the terminal font at zoom z.
func ShellFontPx(z float64) int { return int(shellBaseFontPx*z + 0.5) }

// Verdict is what one chord press does to a descended tile.
type Verdict struct {
	Next float64 // the zoom to show, when Apply
	// Consume: the chord is this tile's, so the key event stops here even
	// when nothing comes of it.
	Consume bool
	Apply   bool
	Persist bool
}

// Decide answers one chord press on the tile a pane is descended into; cur is
// that tile's current zoom, from Of.
func Decide(kind string, pageContent, possiblyEphemeral bool, key string, cur float64) Verdict {
	z, ok := next(key, cur)
	// The content-descent kind set has one owner, rpc.IsContentDescentKind.
	if !ok || !rpc.IsContentDescentKind(kind) {
		return Verdict{}
	}
	if pageContent {
		// A serves_page descent has no persisted content_zoom, because the
		// owning plugin stores no url state and a client-only zoom would
		// break the no-client-state rule. The chord is still this tile's.
		return Verdict{Consume: true}
	}
	// The zoom is live for the session either way; only the write is
	// conditional. An ephemeral visit's row dies on ascent, so persisting its
	// zoom would mark a row the user never asked to keep.
	return Verdict{Next: z, Consume: true, Apply: true, Persist: !possiblyEphemeral}
}

func next(key string, cur float64) (float64, bool) {
	switch key {
	case KeyIn, KeyInAlt:
		return Clamp(cur * Step), true
	case KeyOut:
		return Clamp(cur / Step), true
	case KeyReset:
		return 1, true
	}
	return 0, false
}
