package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"strconv"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// selectionOn is a grid's selection as GetGrid answers it.
func selectionOn(t *testing.T, s *Store, gridID string) []string {
	t.Helper()
	resp, err := s.GetGrid(context.Background(), gridID)
	if err != nil {
		t.Fatalf("GetGrid %s: %v", gridID, err)
	}
	return resp.GetGrid().GetSelection()
}

// pick is a select of home grid grid's tiles.
func pick(ctx context.Context, s *Store, grid string, add, remove []string) error {
	return s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: grid, Add: add, Remove: remove})
}

// selectionEvents is every SelectionChanged among evs.
func selectionEvents(evs []*gridwellv1.Event) []*gridwellv1.SelectionChanged {
	var out []*gridwellv1.SelectionChanged
	for _, ev := range evs {
		if sc := ev.GetSelectionChanged(); sc != nil {
			out = append(out, sc)
		}
	}
	return out
}

func threeTexts(t *testing.T, s *Store, grid string) (a, b, c *gridwellv1.Tile) {
	t.Helper()
	ctx := context.Background()
	var out []*gridwellv1.Tile
	for x := int64(0); x < 3; x++ {
		n, err := s.CreateText(ctx, grid, x*2, 0, 1, 1, []byte("t"))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out[0], out[1], out[2]
}

// The selection is a node fact on the grid row: it survives a reopen, and
// writing it moves no version, the grid's or any tile's.
func TestSelectionPersistsAndBumpsNoVersion(t *testing.T) {
	s, path := newTestStoreFile(t)
	ctx := context.Background()
	root := rootID(t, s)
	a, b, _ := threeTexts(t, s, root)
	before, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := pick(ctx, s, root, []string{b.Id, a.Id}, nil); err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	after, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if after.Grid.Version != before.Grid.Version {
		t.Errorf("grid version %d -> %d; a selection is not content", before.Grid.Version, after.Grid.Version)
	}
	for i := range before.Tiles {
		if !proto.Equal(before.Tiles[i], after.Tiles[i]) {
			t.Errorf("tile %s changed under a selection write", before.Tiles[i].Id)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got, want := selectionOn(t, s2, root), []string{a.Id, b.Id}; !slices.Equal(got, want) {
		t.Errorf("selection after reopen = %v, want %v", got, want)
	}
}

// A select names what it adds and what it removes and claims nothing: two
// selects of different tiles merge whichever lands first, two of the same
// tile are the last one's, and a select landing where the row already is
// writes nothing and tells no one.
func TestSelectionAddsAndRemovesWithoutAClaim(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	a, b, c := threeTexts(t, s, root)
	if err := pick(ctx, s, root, []string{a.Id}, nil); err != nil {
		t.Fatal(err)
	}
	// Another writer, who never saw a selected, adds b: both stand.
	if err := pick(ctx, s, root, []string{b.Id}, nil); err != nil {
		t.Fatalf("a second writer's select: %v", err)
	}
	if got := selectionOn(t, s, root); !slices.Equal(got, []string{a.Id, b.Id}) {
		t.Errorf("two selects merged to %v, want [%s %s]", got, a.Id, b.Id)
	}
	// One takes a out while the other puts it back in: the last one wins.
	if err := pick(ctx, s, root, nil, []string{a.Id}); err != nil {
		t.Fatal(err)
	}
	if err := pick(ctx, s, root, []string{a.Id}, nil); err != nil {
		t.Fatal(err)
	}
	if got := selectionOn(t, s, root); !slices.Equal(got, []string{a.Id, b.Id}) {
		t.Errorf("same tile, last select: %v, want [%s %s]", got, a.Id, b.Id)
	}

	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if err := pick(ctx, s, root, []string{a.Id}, []string{c.Id}); err != nil {
		t.Errorf("a select landing where the row is: %v, want nil", err)
	}
	if evs := drainEvents(t, ch); len(evs) != 0 {
		t.Errorf("a select that changed nothing told %d events", len(evs))
	}

	if err := pick(ctx, s, root, []string{c.Id}, []string{b.Id}); err != nil {
		t.Fatal(err)
	}
	sc := selectionEvents(drainEvents(t, ch))
	if len(sc) != 1 || sc[0].GridId != root || !slices.Equal(sc[0].TileIds, []string{a.Id, c.Id}) {
		t.Errorf("SelectionChanged = %v, want one for %s carrying the whole set [%s %s]", sc, root, a.Id, c.Id)
	}
	if err := pick(ctx, s, root, nil, []string{c.Id, a.Id}); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if got := selectionOn(t, s, root); len(got) != 0 {
		t.Errorf("after clearing the selection = %v", got)
	}
}

// The set names only tiles on its grid: an added tile of another grid is
// ignored, not refused, and the rest of the select still lands.
func TestSelectionIgnoresATileOffTheGrid(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	w, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateText(ctx, w.ChildGridId, 0, 0, 1, 1, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pick(ctx, s, root, []string{inner.Id, w.Id}, nil); err != nil {
		t.Errorf("selecting another grid's tile beside one of this grid's: %v, want nil", err)
	}
	if got := selectionOn(t, s, root); !slices.Equal(got, []string{w.Id}) {
		t.Errorf("selection = %v, want [%s] alone", got, w.Id)
	}
}

// A tile that leaves its grid leaves the selection with it, whichever way it
// goes, and the change is told with the new set. A tile that comes back
// lands unselected.
func TestATileLeavingItsGridLeavesTheSelection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	a, b, c := threeTexts(t, s, root)
	w, err := s.CreateWell(ctx, root, 0, 4, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := pick(ctx, s, root, []string{a.Id, b.Id, c.Id}, nil); err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	// Delete: into the trash.
	if err := s.DeleteTile(ctx, rpc.DeleteOf(a)); err != nil {
		t.Fatal(err)
	}
	if got := selectionOn(t, s, root); !slices.Equal(got, []string{b.Id, c.Id}) {
		t.Errorf("after delete: selection = %v, want [%s %s]", got, b.Id, c.Id)
	}
	sc := selectionEvents(drainEvents(t, ch))
	if len(sc) != 1 || !slices.Equal(sc[0].TileIds, []string{b.Id, c.Id}) {
		t.Errorf("after delete: SelectionChanged = %v", sc)
	}

	// Move: into another grid, and back.
	moved, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: b.Id, GridId: w.ChildGridId, X: 0, Y: 0, W: 1, H: 1, Claim: rpc.PlacementOf(b),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := selectionOn(t, s, root); !slices.Equal(got, []string{c.Id}) {
		t.Errorf("after move: selection = %v, want [%s]", got, c.Id)
	}
	if _, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: b.Id, GridId: root, X: b.X, Y: b.Y, W: 1, H: 1, Claim: rpc.PlacementOf(moved),
	}); err != nil {
		t.Fatal(err)
	}
	if got := selectionOn(t, s, root); !slices.Equal(got, []string{c.Id}) {
		t.Errorf("a tile moved back came back selected: %v", got)
	}

	// Destroy: a scratch-grid tile deletes for real.
	scratch, err := s.ScratchGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateText(ctx, scratch, 0, 0, 1, 1, []byte("d"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pick(ctx, s, scratch, []string{d.Id}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTile(ctx, rpc.DeleteOf(d)); err != nil {
		t.Fatal(err)
	}
	if got := selectionOn(t, s, scratch); len(got) != 0 {
		t.Errorf("after destroy: selection = %v", got)
	}
}

// A plugin's grid keeps its selection on the node's row for it, like its
// framing, and a retired row leaves the set.
func TestPluginGridSelection(t *testing.T) {
	s := newTestStore(t)
	n := s.Namespace("plug1")
	gid, err := n.ContextID("inbox")
	if err != nil {
		t.Fatal(err)
	}
	a, err := n.Mint(gid, &pluginv1.Entry{Key: "a", Kind: "text", Label: "a"}, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := n.Mint(gid, &pluginv1.Entry{Key: "b", Kind: "text", Label: "b"}, 0, 2, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.SetSelection(gid, SelectOp{Add: []int64{b, a}}); err != nil {
		t.Fatal(err)
	}
	if err := n.Retire(a); err != nil {
		t.Fatal(err)
	}
	got, err := n.Selection(gid)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int64{b}) {
		t.Errorf("after retiring %d: selection = %v, want [%d]", a, got, b)
	}
	if changed, err := n.SetSelection(gid, SelectOp{Add: []int64{a}}); err != nil || len(changed) != 0 {
		t.Errorf("selecting a retired row: changed %v, err %v; want it ignored", changed, err)
	}
	// Home's grid of the same number is not this one.
	if _, err := s.Namespace("").SetSelection(gid, SelectOp{Add: []int64{b}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another namespace's grid: err = %v, want ErrNotFound", err)
	}
}

// nest is root holding well w1 holding well w2, each grid with one text tile
// selected.
func nest(t *testing.T, s *Store, root string) (g1, g2 string) {
	t.Helper()
	ctx := context.Background()
	w1, err := s.CreateWell(ctx, root, 0, 6, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	w2, err := s.CreateWell(ctx, w1.ChildGridId, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{root, w1.ChildGridId, w2.ChildGridId} {
		x, err := s.CreateText(ctx, g, 3, 7, 1, 1, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pick(ctx, s, g, []string{x.Id}, nil); err != nil {
			t.Fatal(err)
		}
	}
	return w1.ChildGridId, w2.ChildGridId
}

// clear empties the named grid's set without naming it, and alone touches no
// other grid; with add it clears first, so the set becomes what add names.
func TestSelectionClearEmptiesTheNamedGrid(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	g1, _ := nest(t, s, root)
	a, _, _ := threeTexts(t, s, root)
	if err := s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: root, Clear: true, Add: []string{a.Id}}); err != nil {
		t.Fatal(err)
	}
	if got := selectionOn(t, s, root); !slices.Equal(got, []string{a.Id}) {
		t.Errorf("clear then add: %v, want [%s]", got, a.Id)
	}
	if got := selectionOn(t, s, g1); len(got) != 1 {
		t.Errorf("a clear without recurse reached a nested grid: %v", got)
	}
}

// clear with recurse empties every grid nested under the named grid's wells,
// to any depth, in one write, telling each grid's set; recurse alone is
// refused and changes nothing.
func TestSelectionClearRecursesThroughWells(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	g1, g2 := nest(t, s, root)
	err := s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: g1, Recurse: true})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("recurse without clear: err = %v, want ErrInvalidArgument", err)
	}
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if err := s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: g1, Clear: true, Recurse: true}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{g1, g2} {
		if got := selectionOn(t, s, g); len(got) != 0 {
			t.Errorf("grid %s kept %v", g, got)
		}
	}
	if got := selectionOn(t, s, root); len(got) != 1 {
		t.Errorf("the grid above was cleared: %v", got)
	}
	told := map[string]bool{}
	for _, sc := range selectionEvents(drainEvents(t, ch)) {
		if len(sc.TileIds) != 0 {
			t.Errorf("SelectionChanged for %s carries %v, want none", sc.GridId, sc.TileIds)
		}
		told[sc.GridId] = true
	}
	if len(told) != 2 || !told[g1] || !told[g2] {
		t.Errorf("told %v, want %s and %s", told, g1, g2)
	}
	// Again: nothing changed, nothing told.
	if err := s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: g1, Clear: true, Recurse: true}); err != nil {
		t.Fatal(err)
	}
	if evs := drainEvents(t, ch); len(evs) != 0 {
		t.Errorf("a clear of empty sets told %d events", len(evs))
	}
}

// The walk stays in the grid's namespace: an exit well to another namespace's
// grid is not followed.
func TestSelectionClearRecurseStaysInItsNamespace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	n := s.Namespace("plug1")
	gid, err := n.ContextID("inbox")
	if err != nil {
		t.Fatal(err)
	}
	e, err := n.Mint(gid, &pluginv1.Entry{Key: "e", Kind: "text", Label: "e"}, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.SetSelection(gid, SelectOp{Add: []int64{e}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateExitWell(ctx, root, 5, 5, 1, 1, "plug1/"+strconv.FormatInt(gid, 10), "", rpc.View{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: root, Clear: true, Recurse: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := n.Selection(gid); err != nil || len(got) != 1 {
		t.Errorf("another namespace's grid behind an exit well: %v, %v; want it kept", got, err)
	}
	// From the plugin's side the walk stays in its own rows too.
	changed, err := n.SetSelection(gid, SelectOp{Clear: true, Recurse: true})
	if err != nil || !slices.Equal(changed, []int64{gid}) {
		t.Errorf("a plugin grid's recursive clear changed %v, %v; want [%d]", changed, err, gid)
	}
}

// The all-clear empties every grid's set in one namespace, home's or a
// plugin's, and leaves the others' sets alone; it names nothing else.
func TestSelectionClearAllIsOneNamespace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	g1, g2 := nest(t, s, root)
	n := s.Namespace("plug1")
	gid, err := n.ContextID("inbox")
	if err != nil {
		t.Fatal(err)
	}
	e, err := n.Mint(gid, &pluginv1.Entry{Key: "e", Kind: "text", Label: "e"}, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.SetSelection(gid, SelectOp{Add: []int64{e}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*gridwellv1.SetSelectionRequest{
		{All: true},
		{All: true, Clear: true, GridId: root},
		{All: true, Clear: true, Add: []string{"1"}},
	} {
		if err := s.SetSelection(ctx, bad); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%v: err = %v, want ErrInvalidArgument", bad, err)
		}
	}

	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if err := s.SetSelection(ctx, &gridwellv1.SetSelectionRequest{Clear: true, All: true}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{root, g1, g2} {
		if got := selectionOn(t, s, g); len(got) != 0 {
			t.Errorf("home grid %s kept %v", g, got)
		}
	}
	if got := len(selectionEvents(drainEvents(t, ch))); got != 3 {
		t.Errorf("told %d grids, want 3", got)
	}
	if got, _ := n.Selection(gid); len(got) != 1 {
		t.Errorf("home's all-clear reached the plugin's grid: %v", got)
	}
	changed, err := n.ClearAllSelections()
	if err != nil || !slices.Equal(changed, []int64{gid}) {
		t.Errorf("the plugin's all-clear changed %v, %v; want [%d]", changed, err, gid)
	}
	if changed, err := n.ClearAllSelections(); err != nil || len(changed) != 0 {
		t.Errorf("a second all-clear changed %v, %v", changed, err)
	}
}
