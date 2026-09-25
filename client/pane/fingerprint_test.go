package pane

import "testing"

// The table the settle persisters are armed by: what moves the fingerprint is
// a fact one of them writes, and what does not move it is a repaint. A fact
// missing from the left column is a write that never happens; one wrongly in
// the right column is a settle a live tile's mirror pass can postpone forever.
func TestWhatMovesThePersistedFingerprint(t *testing.T) {
	build := func() *Tree {
		tr := NewTree()
		p := tr.FocusedPane()
		p.Stack = NewStack("g7abcde")
		p.Cx, p.Cy, p.Zoom = 1, 2, 3
		return tr
	}
	moves := []struct {
		name string
		do   func(*Tree)
	}{
		{"a pan", func(tr *Tree) { tr.FocusedPane().Cx += 0.5 }},
		{"a zoom", func(tr *Tree) { tr.FocusedPane().Zoom *= 2 }},
		{"a descent", func(tr *Tree) { tr.FocusedPane().Push(Frame{Door: "t7abcde", Zoom: 1}) }},
		{"a text scroll", func(tr *Tree) { tr.FocusedPane().TextScrollY = 40 }},
		{"a text mode toggle", func(tr *Tree) { tr.FocusedPane().TextMode = "rendered" }},
		{"a content zoom", func(tr *Tree) { tr.FocusedPane().TextZoom = 1.5 }},
		{"a split", func(tr *Tree) {
			if _, err := tr.Split(Vertical); err != nil {
				t.Fatal(err)
			}
		}},
		{"a divider drag", func(tr *Tree) {
			if _, err := tr.Split(Vertical); err != nil {
				t.Fatal(err)
			}
			tr.Root.Split.Ratio = 0.7
		}},
		{"a focus move", func(tr *Tree) {
			p, err := tr.Split(Vertical)
			if err != nil {
				t.Fatal(err)
			}
			tr.Focus = p.ID
		}},
		{"a zoom toggle", func(tr *Tree) { tr.ToggleZoom(tr.Focus) }},
		{"entering a level", func(tr *Tree) { tr.IDPrefix = "w7abcde/" }},
	}
	for _, c := range moves {
		before := PersistedFingerprint(build()).Value()
		tr := build()
		c.do(tr)
		if got := PersistedFingerprint(tr).Value(); got == before {
			t.Errorf("%s left the fingerprint alone", c.name)
		}
	}

	// A repaint is not a change. The same tree read twice is the same fact,
	// and that is what keeps a live tile's 250ms mirror pass from pushing the
	// settle out for as long as the tile is open.
	tr := build()
	first := PersistedFingerprint(tr).Value()
	if second := PersistedFingerprint(tr).Value(); second != first {
		t.Error("two looks at one tree disagree")
	}
	if other := PersistedFingerprint(build()).Value(); other != first {
		t.Error("two equal trees have different fingerprints")
	}
	// A pan and back is the arrangement the user started with, and writing it
	// is a no-op the flush already refuses.
	moved := build()
	moved.FocusedPane().Cx += 0.5
	moved.FocusedPane().Cx -= 0.5
	if PersistedFingerprint(moved).Value() != PersistedFingerprint(build()).Value() {
		t.Error("a pan and back is not the place it started at")
	}
}

// The hover-wheel's drift lives in a map, and the caller folds each entry in.
// Map order is not a fact about the drift, so folding the same entries in
// either order is the same fingerprint — otherwise every frame would look like
// a change and the settle would never come due.
func TestUnorderedFoldingIgnoresOrder(t *testing.T) {
	a := NewFingerprint().Str("t7abcde").Num(1)
	b := NewFingerprint().Str("w7abcde").Num(2)
	base := NewFingerprint().Str("tree")
	if base.MergeUnordered(a).MergeUnordered(b).Value() !=
		base.MergeUnordered(b).MergeUnordered(a).Value() {
		t.Error("the fold reads the iteration order as a fact")
	}
	if base.MergeUnordered(a).Value() == base.MergeUnordered(b).Value() {
		t.Error("two different members fold the same")
	}
	// A member leaving the set is a change: the flush that drained it has
	// something different to say next time.
	if base.MergeUnordered(a).Value() == base.Value() {
		t.Error("an empty set and a set of one fold the same")
	}
}

// Two strings run together must not read as the same fold as their halves
// swapped, or a pane id and a grid id could trade a character unnoticed.
func TestStringsAreTerminated(t *testing.T) {
	if NewFingerprint().Str("ab").Str("c").Value() == NewFingerprint().Str("a").Str("bc").Value() {
		t.Error("the fold runs strings together")
	}
}
