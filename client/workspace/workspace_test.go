package workspace

import (
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

func TestStackPushPopRestoresOuterTrees(t *testing.T) {
	var s Stack
	if s.Depth() != 0 || s.Top() != nil {
		t.Fatal("fresh stack not empty")
	}
	outer1, outer2 := pane.NewTree(), pane.NewTree()
	s.Push(Frame{OuterTree: outer1, OriginPane: "p1", TileID: "u/7", Name: "A"})
	s.Push(Frame{OuterTree: outer2, OriginPane: "p3", TileID: "u/9", Name: "B"})
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

// TestPopCountForCrumb pins the bar semantics: clicking crumb k LEAVES
// workspace k (and everything deeper) — consistent whether k is the current
// workspace (rightmost: leave one) or an ancestor (leave several). The same
// verb everywhere, so the bar never means two different things.
func TestPopCountForCrumb(t *testing.T) {
	var s Stack
	s.Push(Frame{Name: "A"})
	s.Push(Frame{Name: "B"})
	s.Push(Frame{Name: "C"})
	cases := []struct{ level, want int }{
		{3, 1}, // rightmost: leave C, land in B
		{2, 2}, // leave B+C, land in A
		{1, 3}, // leave everything, land in the session
		{0, 0}, {4, 0},
	}
	for _, c := range cases {
		if got := s.PopCountForCrumb(c.level); got != c.want {
			t.Errorf("PopCountForCrumb(%d) = %d, want %d", c.level, got, c.want)
		}
	}
}

// TestShouldPersistDiffsAndReadOnly: the single write decision — identical
// bytes never write (a pure visit never mutates), a change writes once and
// goes quiet after MarkSaved, and a read-only frame (undecodable blob) never
// writes no matter what.
func TestShouldPersistDiffsAndReadOnly(t *testing.T) {
	f := &Frame{}
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
	ro := &Frame{ReadOnly: true}
	if ShouldPersist(ro, edited) {
		t.Fatal("a read-only frame must NEVER write (it could not read the blob it would overwrite)")
	}
	if ShouldPersist(nil, edited) || ShouldPersist(f, nil) {
		t.Fatal("nil frame / empty bytes must not persist")
	}
}
