package interest

import (
	"reflect"
	"testing"
)

// The sequence a session runs: the first set is owed even when empty, since
// the node has heard nothing; the same set again is not; a change is; a
// re-open makes the current set owed again; a claimed set is not sent twice.
func TestTracker(t *testing.T) {
	var tr Tracker
	steps := []struct {
		name     string
		do       func() bool
		wantNext []string
		wantOwed bool
	}{
		{"first frame, nothing shown", func() bool { return tr.Show(nil) }, nil, true},
		{"the same again", func() bool { return tr.Show(nil) }, nil, false},
		{"a grid opens", func() bool { return tr.Show([]string{"p/1"}) }, []string{"p/1"}, true},
		{"it stays open", func() bool { return tr.Show([]string{"p/1"}) }, nil, false},
		{"the stream re-opens", func() bool { tr.Reopened(); return tr.Show([]string{"p/1"}) }, []string{"p/1"}, true},
		{"a second pane joins", func() bool { return tr.Show([]string{"p/1", "q/2"}) }, []string{"p/1", "q/2"}, true},
	}
	for _, s := range steps {
		if owed := s.do(); owed != s.wantOwed {
			t.Fatalf("%s: owed = %v, want %v", s.name, owed, s.wantOwed)
		}
		got, ok := tr.Next()
		if ok != s.wantOwed || !reflect.DeepEqual(got, s.wantNext) {
			t.Fatalf("%s: Next = %v, %v; want %v, %v", s.name, got, ok, s.wantNext, s.wantOwed)
		}
		if _, again := tr.Next(); again {
			t.Fatalf("%s: a claimed set was owed again", s.name)
		}
	}
}

// A set whose send failed is not owed again on its own: only a change or a
// re-open asks again, so a refusal is one call, not one per frame.
func TestFailedSendWaitsForAChangeOrReopen(t *testing.T) {
	var tr Tracker
	tr.Show([]string{"p/1"})
	if _, ok := tr.Next(); !ok {
		t.Fatal("the first set was not owed")
	}
	if tr.Show([]string{"p/1"}) {
		t.Error("the failed set is owed again on the next frame")
	}
	tr.Reopened()
	if !tr.Show([]string{"p/1"}) {
		t.Error("after a re-open the set is not owed")
	}
}
