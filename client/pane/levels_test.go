package pane

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
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

// A write that did not land is sent again by the next flush, unless newer
// bytes were sent after it.
func TestForgetSavedOnlyUndoesItsOwnMark(t *testing.T) {
	f := &Level{}
	a := []byte(`{"v":1,"a":1}`)
	b := []byte(`{"v":1,"a":2}`)
	MarkSaved(f, a)
	ForgetSaved(f, a)
	if !ShouldPersist(f, a) {
		t.Fatal("a layout whose write failed must be sent again")
	}
	MarkSaved(f, a)
	MarkSaved(f, b)
	ForgetSaved(f, a)
	if ShouldPersist(f, b) {
		t.Fatal("an older write failing must not undo the newer mark")
	}
}

func TestLevelsClaimAndLanded(t *testing.T) {
	var s Levels
	if got := s.Claim("pt", 9); got != 9 {
		t.Fatalf("no level open: claim %d, want the fallback", got)
	}
	s.Push(Level{TileID: "pt", Basis: 4})
	if got := s.Claim("pt", 9); got != 4 {
		t.Fatalf("claim %d, want the level's basis", got)
	}
	s.Landed("pt", 4, 5)
	if got := s.Claim("pt", 9); got != 5 {
		t.Fatalf("own write landed: claim %d, want 5", got)
	}
	// A write that claimed another basis was not this tree's.
	s.Landed("pt", 3, 8)
	if got := s.Claim("pt", 9); got != 5 {
		t.Fatalf("claim %d after a foreign landing, want 5 kept", got)
	}
}

// Every pane of every parked tree is a holder, outermost first; a level
// entered from boot parks none.
func TestLevelsParked(t *testing.T) {
	outer := TreeAtPlace("", "g1", []string{"d1"}, rpc.View{})
	inner := TreeAtPlace("w1:", "g2", nil, rpc.View{})
	var s Levels
	s.Push(Level{TileID: "boot"})
	s.Push(Level{TileID: "ws1", OuterTree: outer})
	s.Push(Level{TileID: "ws2", OuterTree: inner})
	got := s.Parked()
	if len(got) != 2 || got[0].Anchor() != "g1" || got[1].Anchor() != "g2" {
		t.Fatalf("Parked = %+v, want the two parked trees' panes, outermost first", got)
	}
	if _, ok := got[0].LevelOf("d1"); !ok {
		t.Fatalf("the parked place lost its door: %+v", got[0])
	}
	got[0].Push(Frame{GridID: "x"})
	if outer.FocusedPane().Depth() != 2 {
		t.Fatal("Parked handed out the live stack, not a copy")
	}
}
