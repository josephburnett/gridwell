package pane

import "math"

// The settle persisters are armed once per frame from draw(), and a frame is
// not a change: a live url or shell tile repaints on the mirror's cadence for
// as long as it is open, an ascent trace fades for two seconds, a transition
// runs for its duration. A settle keyed on frames stopping never comes due
// while any of those lasts. A Fingerprint is what the arm is keyed on instead:
// the facts a persister takes from the tree, folded into one comparable
// number, so two draws that would persist the same thing arm nothing.
//
// Over-arming is cheap — both flushes diff against what they last wrote, so a
// fingerprint that moves for something its flush does not write costs one
// no-op pass. Under-arming is a lost write, so every fact a flush reads
// belongs in its fingerprint.
type Fingerprint struct{ h uint64 }

// FNV-1a, for one number a comparison can be made on. Nothing here is a
// checksum of anything stored, so the choice binds nothing.
const (
	fnvOffset uint64 = 14695981039346656037
	fnvPrime  uint64 = 1099511628211
)

func NewFingerprint() Fingerprint { return Fingerprint{h: fnvOffset} }

// Value is the number the caller compares; two Fingerprints are the same fact
// when it matches.
func (f Fingerprint) Value() uint64 { return f.h }

func (f Fingerprint) byte(b byte) Fingerprint {
	f.h = (f.h ^ uint64(b)) * fnvPrime
	return f
}

// Str folds a string and a terminator, so "ab" then "c" is not "a" then "bc".
func (f Fingerprint) Str(s string) Fingerprint {
	for i := 0; i < len(s); i++ {
		f = f.byte(s[i])
	}
	return f.byte(0)
}

func (f Fingerprint) Num(v float64) Fingerprint {
	bits := math.Float64bits(v)
	for i := 0; i < 8; i++ {
		f = f.byte(byte(bits >> (8 * i)))
	}
	return f
}

func (f Fingerprint) Flag(b bool) Fingerprint {
	if b {
		return f.byte(1)
	}
	return f.byte(2)
}

// MergeUnordered folds one member of a set in commutatively, for a caller
// whose facts live in a map: iteration order is not one of the facts.
func (f Fingerprint) MergeUnordered(other Fingerprint) Fingerprint {
	f.h ^= other.h
	return f
}

// LayoutFingerprint is what the layout blob encodes: the shape, the focus and
// each pane's chain of doorways, but no view.
func LayoutFingerprint(t *Tree) Fingerprint { return treeFingerprint(t, false) }

// FramingFingerprint is what the framing writeback reads: the arrangement,
// which decides rects and writers, plus each pane's view.
func FramingFingerprint(t *Tree) Fingerprint { return treeFingerprint(t, true) }

func treeFingerprint(t *Tree, views bool) Fingerprint {
	f := NewFingerprint()
	if t == nil {
		return f
	}
	// The prefix is per level, so two levels whose trees are otherwise equal
	// are two different blobs.
	f = f.Str(t.IDPrefix).Str(t.Focus).Str(t.Zoomed)
	return fingerprintNode(f, t.Root, views)
}

func fingerprintNode(f Fingerprint, n TreeNode, views bool) Fingerprint {
	if n.IsLeaf() {
		return fingerprintPane(f, n.Pane, views)
	}
	if n.Split == nil {
		return f.Str("empty")
	}
	f = f.Str(string(n.Split.Dir)).Num(n.Split.Ratio)
	f = fingerprintNode(f, n.Split.A, views)
	return fingerprintNode(f, n.Split.B, views)
}

func fingerprintPane(f Fingerprint, p *Pane, views bool) Fingerprint {
	f = f.Str(p.ID)
	for _, fr := range p.Frames() {
		f = f.Str(fr.GridID).Str(fr.Door).Flag(fr.Content)
	}
	if !views {
		return f
	}
	return f.Num(p.Cx).Num(p.Cy).Num(p.Zoom).Flag(p.ViewPending).
		Str(p.TextMode).Num(p.TextScrollX).Num(p.TextScrollY).Num(p.TextZoom)
}
