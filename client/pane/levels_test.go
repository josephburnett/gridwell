package pane

import (
	"testing"
)

func TestStackPushPopRestoresOuterTrees(t *testing.T) {
	var s Levels
	if s.Depth() != 0 || s.Top() != nil {
		t.Fatal("fresh stack not empty")
	}
	outer1, outer2 := NewTree(), NewTree()
	s.Push(Level{OuterTree: outer1, OriginPane: "p1", TileID: "u/7", Name: "A"})
	s.Push(Level{OuterTree: outer2, OriginPane: "p3", TileID: "u/9", Name: "B"})
	if s.Depth() != 2 || s.Top().TileID != "u/9" {
		t.Fatalf("stack shape: depth=%d top=%+v", s.Depth(), s.Top())
	}
	if got := s.Names(); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("names = %v", got)
	}
	f, ok := s.Pop()
	if !ok || f.OuterTree != outer2 || f.OriginPane != "p3" {
		t.Fatalf("pop restored the wrong frame: %+v", f)
	}
	f, _ = s.Pop()
	if f.OuterTree != outer1 {
		t.Fatal("second pop lost the first outer tree")
	}
	if _, ok := s.Pop(); ok {
		t.Fatal("pop on empty stack reported ok")
	}
}

// Level k means inside level k, so everything deeper pops and the current
// boundary pops nothing; level 0 is the session.
func TestPopCountTo(t *testing.T) {
	var s Levels
	s.Push(Level{Name: "A"})
	s.Push(Level{Name: "B"})
	s.Push(Level{Name: "C"})
	cases := []struct{ level, want int }{
		{3, 0},  // the current boundary: already there (issue #245)
		{2, 1},  // be inside B: leave C
		{1, 2},  // be inside A: leave B+C
		{0, 3},  // the session outside every workspace
		{-1, 0}, // out of range
		{4, 0},  // out of range
	}
	for _, c := range cases {
		if got := s.PopCountTo(c.level); got != c.want {
			t.Errorf("PopCountTo(%d) = %d, want %d", c.level, got, c.want)
		}
	}
}

// Identical bytes never write, a change writes once and goes quiet after
// MarkSaved, and a read-only level never writes at all.
func TestShouldPersistDiffsAndReadOnly(t *testing.T) {
	f := &Level{}
	base := []byte(`{"v":1,"a":1}`)
	MarkSaved(f, base)
	if ShouldPersist(f, base) {
		t.Fatal("identical bytes must not persist (reading never mutates)")
	}
	edited := []byte(`{"v":1,"a":2}`)
	if !ShouldPersist(f, edited) {
		t.Fatal("changed bytes must persist")
	}
	MarkSaved(f, edited)
	if ShouldPersist(f, edited) {
		t.Fatal("saved bytes must go quiet")
	}
	ro := &Level{ReadOnly: true}
	if ShouldPersist(ro, edited) {
		t.Fatal("a read-only frame must NEVER write (it could not read the blob it would overwrite)")
	}
	if ShouldPersist(nil, edited) || ShouldPersist(f, nil) {
		t.Fatal("nil frame / empty bytes must not persist")
	}
}

// A capture pops the ephemeral content descents and nothing else: a leaf on a
// grid, a leaf in durable content and a leaf whose content the predicate keeps
// all survive untouched.
func TestPopEphemeralContent(t *testing.T) {
	tree := NewTree()
	onGrid := tree.FocusedPane()
	onGrid.Stack = StackAt("g1", nil, "")
	eph := &Pane{ID: "p2", Stack: StackAt("g1", []string{"d1"}, "scratch")}
	durable := &Pane{ID: "p3", Stack: StackAt("g1", nil, "keep")}
	unknown := &Pane{ID: "p4", Stack: StackAt("g1", nil, "unknown")}
	tree.Root = TreeNode{Split: &Split{Dir: Vertical, Ratio: 0.5,
		A: TreeNode{Split: &Split{Dir: Horizontal, Ratio: 0.5,
			A: TreeNode{Pane: onGrid}, B: TreeNode{Pane: eph}}},
		B: TreeNode{Split: &Split{Dir: Horizontal, Ratio: 0.5,
			A: TreeNode{Pane: durable}, B: TreeNode{Pane: unknown}}}}}

	var asked []string
	PopEphemeralContent(tree, func(p *Pane, contentID string) bool {
		asked = append(asked, p.ID+":"+contentID)
		return contentID == "scratch"
	})
	if len(asked) != 3 || asked[0] != "p2:scratch" || asked[1] != "p3:keep" || asked[2] != "p4:unknown" {
		t.Fatalf("asked = %v; a leaf on a grid must not be asked", asked)
	}
	if eph.ContentID() != "" || eph.Depth() != 2 || eph.Door != "d1" {
		t.Fatalf("ephemeral leaf not popped onto its doorway: %+v", eph.Frames())
	}
	if durable.ContentID() != "keep" || unknown.ContentID() != "unknown" {
		t.Fatalf("a kept content frame was popped: %q %q", durable.ContentID(), unknown.ContentID())
	}
	if onGrid.ContentID() != "" || onGrid.Depth() != 1 {
		t.Fatalf("a leaf on a grid was changed: %+v", onGrid.Frames())
	}
	PopEphemeralContent(nil, func(*Pane, string) bool { return true })
	PopEphemeralContent(tree, nil)
}
