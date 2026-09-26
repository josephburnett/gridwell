package pane

import "testing"

// The table the two settle persisters are armed by. The layout persister
// writes the arrangement and the framing persister writes the view, so each
// row says which of them the change is a fact for. A fact missing from a
// column is a write that never happens; one wrongly in it is a write of
// something that did not change, and a pan wrongly in the layout column is a
// layout write per pan.
func TestWhatMovesEachPersistersFingerprint(t *testing.T) {
	build := func() *Tree {
		tr := NewTree()
		p := tr.FocusedPane()
		p.Stack = NewStack("g7abcde")
		p.Cx, p.Cy, p.Zoom = 1, 2, 3
		return tr
	}
	moves := []struct {
		name            string
		layout, framing bool
		do              func(*Tree)
	}{
		{"a pan", false, true, func(tr *Tree) { tr.FocusedPane().Cx += 0.5 }},
		{"a zoom", false, true, func(tr *Tree) { tr.FocusedPane().Zoom *= 2 }},
		{"a descent", true, true, func(tr *Tree) { tr.FocusedPane().Push(Frame{Door: "t7abcde", Zoom: 1}) }},
		{"a text scroll", false, true, func(tr *Tree) { tr.FocusedPane().TextScrollY = 40 }},
		{"a text mode toggle", false, true, func(tr *Tree) { tr.FocusedPane().TextMode = "rendered" }},
		{"a content zoom", false, true, func(tr *Tree) { tr.FocusedPane().TextZoom = 1.5 }},
		{"a split", true, true, func(tr *Tree) {
			if _, err := tr.Split(Vertical); err != nil {
				t.Fatal(err)
			}
		}},
		{"a divider drag", true, true, func(tr *Tree) {
			if _, err := tr.Split(Vertical); err != nil {
				t.Fatal(err)
			}
			tr.Root.Split.Ratio = 0.7
		}},
		{"a focus move", true, true, func(tr *Tree) {
			p, err := tr.Split(Vertical)
			if err != nil {
				t.Fatal(err)
			}
			tr.Focus = p.ID
		}},
		{"a zoom toggle", true, true, func(tr *Tree) { tr.ToggleZoom(tr.Focus) }},
		{"entering a level", true, true, func(tr *Tree) { tr.IDPrefix = "w7abcde/" }},
	}
	for _, c := range moves {
		tr := build()
		c.do(tr)
		if got := LayoutFingerprint(tr).Value() != LayoutFingerprint(build()).Value(); got != c.layout {
			t.Errorf("%s: moves the layout fingerprint = %v, want %v", c.name, got, c.layout)
		}
		if got := FramingFingerprint(tr).Value() != FramingFingerprint(build()).Value(); got != c.framing {
			t.Errorf("%s: moves the framing fingerprint = %v, want %v", c.name, got, c.framing)
		}
	}

	// A repaint is not a change. The same tree read twice is the same fact,
	// and that is what keeps a live tile's 250ms mirror pass from pushing the
	// settle out for as long as the tile is open.
	for name, fp := range map[string]func(*Tree) Fingerprint{
		"layout": LayoutFingerprint, "framing": FramingFingerprint,
	} {
		tr := build()
		first := fp(tr).Value()
		if second := fp(tr).Value(); second != first {
			t.Errorf("%s: two looks at one tree disagree", name)
		}
		if other := fp(build()).Value(); other != first {
			t.Errorf("%s: two equal trees have different fingerprints", name)
		}
		// A pan and back is the place the user started at, and writing it is
		// a no-op the flush already refuses.
		moved := build()
		moved.FocusedPane().Cx += 0.5
		moved.FocusedPane().Cx -= 0.5
		if fp(moved).Value() != fp(build()).Value() {
			t.Errorf("%s: a pan and back is not the place it started at", name)
		}
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
