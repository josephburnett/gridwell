package store

import (
	"context"
	"errors"
	"slices"
	"testing"

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

func claimOf(ids ...string) *gridwellv1.SelectionClaim {
	return &gridwellv1.SelectionClaim{TileIds: ids}
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
	if err := s.SetSelection(ctx, root, nil, []string{b.Id, a.Id}); err != nil {
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

// A write claims the set it saw. A stale claim is refused with the version
// conflict and the row keeps what it held; a write landing where the row
// already is writes nothing and tells no one.
func TestSelectionClaimsTheSetItSaw(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	a, b, c := threeTexts(t, s, root)
	if err := s.SetSelection(ctx, root, claimOf(), []string{a.Id}); err != nil {
		t.Fatal(err)
	}
	err := s.SetSelection(ctx, root, claimOf(), []string{b.Id})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale claim: err = %v, want ErrVersionConflict", err)
	}
	if got := selectionOn(t, s, root); !slices.Equal(got, []string{a.Id}) {
		t.Errorf("after a refused write the selection = %v, want [%s]", got, a.Id)
	}

	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if err := s.SetSelection(ctx, root, claimOf(c.Id), []string{a.Id}); err != nil {
		t.Errorf("a write landing where the row is: %v, want nil", err)
	}
	if evs := drainEvents(t, ch); len(evs) != 0 {
		t.Errorf("a write that changed nothing told %d events", len(evs))
	}

	if err := s.SetSelection(ctx, root, claimOf(a.Id), []string{c.Id, a.Id}); err != nil {
		t.Fatal(err)
	}
	sc := selectionEvents(drainEvents(t, ch))
	if len(sc) != 1 || sc[0].GridId != root || !slices.Equal(sc[0].TileIds, []string{a.Id, c.Id}) {
		t.Errorf("SelectionChanged = %v, want one for %s carrying [%s %s]", sc, root, a.Id, c.Id)
	}
	if err := s.SetSelection(ctx, root, claimOf(c.Id, a.Id), nil); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if got := selectionOn(t, s, root); len(got) != 0 {
		t.Errorf("after clearing the selection = %v", got)
	}
}

// The set names only tiles on its grid: a write naming another grid's tile is
// refused, and nothing is stored.
func TestSelectionRefusesATileOffTheGrid(t *testing.T) {
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
	if err := s.SetSelection(ctx, root, nil, []string{inner.Id}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("selecting another grid's tile: err = %v, want ErrInvalidArgument", err)
	}
	if got := selectionOn(t, s, root); len(got) != 0 {
		t.Errorf("selection = %v after a refused write", got)
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
	if err := s.SetSelection(ctx, root, nil, []string{a.Id, b.Id, c.Id}); err != nil {
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
	if err := s.SetSelection(ctx, scratch, nil, []string{d.Id}); err != nil {
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
	if _, err := n.SetSelection(gid, []int64{}, []int64{b, a}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.SetSelection(gid, []int64{b}, []int64{a}); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale claim: err = %v, want ErrVersionConflict", err)
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
	if _, err := n.SetSelection(gid, []int64{b}, []int64{a}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("selecting a retired row: err = %v, want ErrInvalidArgument", err)
	}
	// Home's grid of the same number is not this one.
	if _, err := s.Namespace("").SetSelection(gid, nil, []int64{b}); !errors.Is(err, ErrNotFound) {
		t.Errorf("another namespace's grid: err = %v, want ErrNotFound", err)
	}
}
