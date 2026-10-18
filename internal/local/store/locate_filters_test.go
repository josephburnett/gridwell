package store

import (
	"context"
	"slices"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// filterWorld is a root holding a text and a well, the well holding a text and
// a nested well, the nested well holding a text: three depths, each text made
// an hour after the last.
type filterWorld struct {
	s                  *Store
	root               string
	outer, inner       *gridwellv1.Tile
	top, middle, deep  *gridwellv1.Tile
	t0, t1, t2, t3, t4 time.Time
}

func newFilterWorld(t *testing.T) *filterWorld {
	t.Helper()
	w := &filterWorld{s: newTestStore(t)}
	ctx := context.Background()
	w.root = rootID(t, w.s)
	w.t0 = time.Unix(1_800_000_000, 0)
	for i, at := range []*time.Time{&w.t1, &w.t2, &w.t3, &w.t4} {
		*at = w.t0.Add(time.Duration(i+1) * time.Hour)
	}
	clock := func(at time.Time) { w.s.SetClock(func() time.Time { return at }) }
	var err error
	clock(w.t0)
	if w.outer, err = w.s.CreateWell(ctx, w.root, 0, 0, 1, 1, "outer"); err != nil {
		t.Fatal(err)
	}
	if w.inner, err = w.s.CreateWell(ctx, w.outer.ChildGridId, 0, 0, 1, 1, "inner"); err != nil {
		t.Fatal(err)
	}
	clock(w.t1)
	if w.top, err = w.s.CreateText(ctx, w.root, 2, 0, 1, 1, []byte("gopher at the top")); err != nil {
		t.Fatal(err)
	}
	clock(w.t2)
	if w.middle, err = w.s.CreateText(ctx, w.outer.ChildGridId, 2, 0, 1, 1, []byte("gopher in the middle")); err != nil {
		t.Fatal(err)
	}
	clock(w.t3)
	if w.deep, err = w.s.CreateText(ctx, w.inner.ChildGridId, 2, 0, 1, 1, []byte("gopher down deep")); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *filterWorld) find(t *testing.T, req *gridwellv1.FindRequest) []string {
	t.Helper()
	res, err := w.s.Find(context.Background(), req)
	if err != nil {
		t.Fatalf("find %v: %v", req, err)
	}
	var ids []string
	for _, r := range res {
		ids = append(ids, r.Tile.Id)
	}
	return ids
}

func sameIDs(got []string, want ...string) bool {
	return slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want)))
}

// A grid keeps the hits on it; recurse adds the grids of the wells inside it,
// to any depth.
func TestFindGridScope(t *testing.T) {
	w := newFilterWorld(t)
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", GridId: w.outer.ChildGridId}); !sameIDs(got, w.middle.Id) {
		t.Errorf("grid alone = %v, want only the middle text", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", GridId: w.outer.ChildGridId, Recurse: true}); !sameIDs(got, w.middle.Id, w.deep.Id) {
		t.Errorf("grid recursive = %v, want middle and deep", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", GridId: w.root, Recurse: true}); !sameIDs(got, w.top.Id, w.middle.Id, w.deep.Id) {
		t.Errorf("root recursive = %v, want all three", got)
	}
}

// Each time range keeps the tiles whose row time falls in it, ends inclusive
// and 0 open; content_changed moves only on a content write, modified on any.
func TestFindTimeRanges(t *testing.T) {
	w := newFilterWorld(t)
	ctx := context.Background()
	at := func(tm time.Time) int64 { return tm.Unix() }
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", Created: &gridwellv1.TimeRange{From: at(w.t2)}}); !sameIDs(got, w.middle.Id, w.deep.Id) {
		t.Errorf("created from t2 = %v, want middle and deep", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", Created: &gridwellv1.TimeRange{To: at(w.t2)}}); !sameIDs(got, w.top.Id, w.middle.Id) {
		t.Errorf("created to t2 = %v, want top and middle", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", Created: &gridwellv1.TimeRange{From: at(w.t2), To: at(w.t2)}}); !sameIDs(got, w.middle.Id) {
		t.Errorf("created at t2 = %v, want middle", got)
	}

	// At t4 the top text moves and the middle one is typed into.
	w.s.SetClock(func() time.Time { return w.t4 })
	if _, err := w.s.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: w.top.Id, GridId: w.top.GridId,
		X: 5, Y: 5, W: 1, H: 1, Claim: placementOf(w.top)}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.WriteContent(ctx, w.middle.Id, w.middle.Version, []byte("gopher, edited")); err != nil {
		t.Fatal(err)
	}
	recent := &gridwellv1.TimeRange{From: at(w.t4)}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", Modified: recent}); !sameIDs(got, w.top.Id, w.middle.Id) {
		t.Errorf("modified since t4 = %v, want the moved and the typed", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", ContentChanged: recent}); !sameIDs(got, w.middle.Id) {
		t.Errorf("content changed since t4 = %v, want only the typed one", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", ContentChanged: &gridwellv1.TimeRange{To: at(w.t3)}}); !sameIDs(got, w.top.Id, w.deep.Id) {
		t.Errorf("content unchanged since t3 = %v, want top and deep (made then, never typed in)", got)
	}
}

// selected keeps the tiles in their grid's selection: with a grid, that
// grid's set; without one, every grid's.
func TestFindSelected(t *testing.T) {
	w := newFilterWorld(t)
	ctx := context.Background()
	if err := w.s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: w.root, Add: []string{w.top.Id}}); err != nil {
		t.Fatal(err)
	}
	if err := w.s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: w.inner.ChildGridId, Add: []string{w.deep.Id}}); err != nil {
		t.Fatal(err)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", Selected: true}); !sameIDs(got, w.top.Id, w.deep.Id) {
		t.Errorf("selected anywhere = %v, want top and deep", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", Selected: true, GridId: w.root}); !sameIDs(got, w.top.Id) {
		t.Errorf("selected on root = %v, want top", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Selected: true, GridId: w.outer.ChildGridId, Recurse: true}); !sameIDs(got, w.deep.Id) {
		t.Errorf("selected under outer = %v, want deep", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "id:" + w.middle.Id, Selected: true}); len(got) != 0 {
		t.Errorf("an unselected tile located by id under selected = %v, want none", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "id:" + w.deep.Id, Selected: true}); !sameIDs(got, w.deep.Id) {
		t.Errorf("a selected tile located by id = %v, want it", got)
	}
}

// An empty query with a filter lists every tile passing it, the newest first
// by the time filtered on; with no filter it is still no find.
func TestFindEmptyQueryLists(t *testing.T) {
	w := newFilterWorld(t)
	got := w.find(t, &gridwellv1.FindRequest{GridId: w.root, Recurse: true})
	want := []string{w.deep.Id, w.middle.Id, w.top.Id, w.inner.Id, w.outer.Id}
	// The two wells share t0; the later id is the newer.
	if !slices.Equal(got, want) {
		t.Errorf("everything under root = %v, want newest first %v", got, want)
	}
	got = w.find(t, &gridwellv1.FindRequest{Created: &gridwellv1.TimeRange{From: w.t1.Unix()}, Limit: 2})
	if !slices.Equal(got, []string{w.deep.Id, w.middle.Id}) {
		t.Errorf("made since t1, two = %v, want deep then middle", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{}); len(got) != 0 {
		t.Errorf("no query and no filter = %v, want nothing", got)
	}
}

// Filters AND together and with the query.
func TestFindFiltersCombine(t *testing.T) {
	w := newFilterWorld(t)
	ctx := context.Background()
	if err := w.s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: w.outer.ChildGridId, Add: []string{w.middle.Id, w.inner.Id}}); err != nil {
		t.Fatal(err)
	}
	req := &gridwellv1.FindRequest{Query: "gopher", GridId: w.root, Recurse: true, Selected: true,
		Created: &gridwellv1.TimeRange{From: w.t2.Unix()}}
	if got := w.find(t, req); !sameIDs(got, w.middle.Id) {
		t.Errorf("gopher, under root, selected, made since t2 = %v, want middle", got)
	}
	req.Query = "inner"
	if got := w.find(t, req); len(got) != 0 {
		t.Errorf("the selected well made at t0 passed a from-t2 filter: %v", got)
	}
}

// kind keeps the tiles of that kind, with a query or alone, newest first.
func TestFindKind(t *testing.T) {
	w := newFilterWorld(t)
	if got := w.find(t, &gridwellv1.FindRequest{Kind: "well"}); !slices.Equal(got, []string{w.inner.Id, w.outer.Id}) {
		t.Errorf("every well = %v, want inner then outer", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", Kind: "text", GridId: w.outer.ChildGridId}); !sameIDs(got, w.middle.Id) {
		t.Errorf("gopher texts on outer's grid = %v, want middle", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "gopher", Kind: "pane"}); len(got) != 0 {
		t.Errorf("gopher panes = %v, want none", got)
	}
	if got := w.find(t, &gridwellv1.FindRequest{Query: "id:" + w.top.Id, Kind: "well"}); len(got) != 0 {
		t.Errorf("a text located by id under kind well = %v, want none", got)
	}
}
