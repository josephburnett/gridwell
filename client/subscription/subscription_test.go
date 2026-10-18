package subscription

import (
	"testing"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

func shown(grids ...string) *gridwellv1.SetViewRequest {
	return &gridwellv1.SetViewRequest{Label: "desktop", ShownGridIds: grids}
}

// focused is a view of one pane at grid, focused or not.
func focused(grid string, on bool) *gridwellv1.SetViewRequest {
	v := shown(grid)
	v.Panes = []*gridwellv1.ViewPane{{Id: "p0", Anchor: grid, GridId: grid, Focused: on}}
	return v
}

// The sequence a session runs: the first view is owed even when empty, since
// the node has heard nothing; the same view again is not; a change to any of
// it is, the panes' focus as much as the grids shown; a re-open makes the
// current view owed again; a claimed view is not sent twice.
func TestTracker(t *testing.T) {
	var tr Tracker
	steps := []struct {
		name     string
		do       func() bool
		wantNext *gridwellv1.SetViewRequest
		wantOwed bool
	}{
		{"first draw, nothing shown", func() bool { return tr.Show(shown()) }, shown(), true},
		{"the same again", func() bool { return tr.Show(shown()) }, nil, false},
		{"a grid opens", func() bool { return tr.Show(shown("p/1")) }, shown("p/1"), true},
		{"it stays open", func() bool { return tr.Show(shown("p/1")) }, nil, false},
		{"the stream re-opens", func() bool { tr.Reopened(); return tr.Show(shown("p/1")) }, shown("p/1"), true},
		{"a second pane joins", func() bool { return tr.Show(shown("p/1", "q/2")) }, shown("p/1", "q/2"), true},
		{"a pane is laid out", func() bool { return tr.Show(focused("p/1", false)) }, focused("p/1", false), true},
		{"focus moves, the grids stay", func() bool { return tr.Show(focused("p/1", true)) }, focused("p/1", true), true},
		{"nothing moves", func() bool { return tr.Show(focused("p/1", true)) }, nil, false},
	}
	for _, s := range steps {
		if owed := s.do(); owed != s.wantOwed {
			t.Fatalf("%s: owed = %v, want %v", s.name, owed, s.wantOwed)
		}
		got, ok := tr.Next()
		if ok != s.wantOwed || !proto.Equal(got, s.wantNext) {
			t.Fatalf("%s: Next = %v, %v; want %v, %v", s.name, got, ok, s.wantNext, s.wantOwed)
		}
		if _, again := tr.Next(); again {
			t.Fatalf("%s: a claimed view was owed again", s.name)
		}
	}
}

// A view whose send failed is not owed again on its own: only a change or a
// re-open asks again, so a refusal is one call, not one per draw.
func TestFailedSendWaitsForAChangeOrReopen(t *testing.T) {
	var tr Tracker
	tr.Show(shown("p/1"))
	if _, ok := tr.Next(); !ok {
		t.Fatal("the first view was not owed")
	}
	if tr.Show(shown("p/1")) {
		t.Error("the failed view is owed again on the next draw")
	}
	tr.Reopened()
	if !tr.Show(shown("p/1")) {
		t.Error("after a re-open the view is not owed")
	}
}

// The view the tracker holds is its own: a caller changing the one it showed
// does not change what is owed.
func TestShowCopies(t *testing.T) {
	var tr Tracker
	v := shown("p/1")
	tr.Show(v)
	v.ShownGridIds[0] = "q/9"
	got, _ := tr.Next()
	if !proto.Equal(got, shown("p/1")) {
		t.Errorf("Next = %v, want the view as shown", got)
	}
}
