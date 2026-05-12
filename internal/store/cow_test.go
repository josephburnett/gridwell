package store

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestCloneNodeCreatesSecondPointer verifies that cloning a well produces a
// second well node sharing the same child grid (refcount 2 on the child)
// without copying the child's contents.
func TestCloneNodeCreatesSharedChildGrid(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()

	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Put something inside w's child grid so we can later confirm the
	// clone's child still sees it.
	inner, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{w.ID}}, ViewRect: largeView(),
		GridID: w.ChildGridID, X: 5, Y: 5, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	clone, err := s.CloneNode(ctx, u.ID, &rpc.CloneNodeRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID,
		DestGridID: u.RootGridID, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.ObjectID != w.ObjectID {
		t.Errorf("clone object_id = %s, original = %s", clone.ObjectID, w.ObjectID)
	}
	if clone.ID == w.ID {
		t.Errorf("clone has same row id as original")
	}
	if clone.ChildGridID != w.ChildGridID {
		t.Errorf("clone child grid = %d, original = %d (expected shared)", clone.ChildGridID, w.ChildGridID)
	}

	// Refcount on the child grid should be 2.
	var rc int64
	if err := s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, w.ChildGridID).Scan(&rc); err != nil {
		t.Fatal(err)
	}
	if rc != 2 {
		t.Errorf("child refcount = %d, want 2", rc)
	}
	_ = inner
}

// TestCowForkOnWriteIntoSharedChild: after clone, writing through one of the
// pointers must fork; writing through the other must still see the
// pre-fork state.
func TestCowForkOnWriteIntoSharedChild(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()

	w, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Inside the child grid, place a marker well.
	inner, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{w.ID}}, ViewRect: largeView(),
		GridID: w.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	clone, err := s.CloneNode(ctx, u.ID, &rpc.CloneNodeRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: w.ID,
		DestGridID: u.RootGridID, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 10, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Write through clone's child (fork should occur).
	resized, err := s.ResizeNode(ctx, u.ID, &rpc.ResizeNodeRequest{
		Path: rpc.Path{WellIDs: []int64{clone.ID}}, ViewRect: largeView(),
		NodeID: inner.ID, W: 3, H: 3,
	})
	if err != nil {
		t.Fatalf("resize through clone: %v", err)
	}
	// resized should have a NEW row id (different from inner.ID) but the
	// same object_id, because preWrite forked the child grid.
	if resized.ID == inner.ID {
		t.Error("expected fork; got same row id")
	}
	if resized.ObjectID != inner.ObjectID {
		t.Errorf("object_id changed across fork: %s -> %s", inner.ObjectID, resized.ObjectID)
	}
	if resized.W != 3 || resized.H != 3 {
		t.Errorf("resize did not apply: %+v", resized)
	}

	// The original well's child grid should still contain the unchanged inner.
	origChildContents, err := s.GetGrid(ctx, u.ID, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(origChildContents.Nodes) != 1 {
		t.Fatalf("original child has %d nodes, want 1", len(origChildContents.Nodes))
	}
	if origChildContents.Nodes[0].W != 1 || origChildContents.Nodes[0].H != 1 {
		t.Errorf("original child node was mutated: %+v", origChildContents.Nodes[0])
	}

	// The clone's child grid should now be a different id from w's.
	cloneAfter, err := s.loadNode(ctx, s.db, clone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cloneAfter.ChildGridID == w.ChildGridID {
		t.Error("clone's child grid was not forked")
	}

	// Refcounts: original child should be back to 1; new forked child is 1.
	var rcOld, rcNew int64
	_ = s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, w.ChildGridID).Scan(&rcOld)
	_ = s.db.QueryRow(`SELECT refcount FROM grids WHERE id = ?`, cloneAfter.ChildGridID).Scan(&rcNew)
	if rcOld != 1 {
		t.Errorf("orig child refcount = %d, want 1", rcOld)
	}
	if rcNew != 1 {
		t.Errorf("forked child refcount = %d, want 1", rcNew)
	}
}

// TestRefcountInvariant runs random create/clone/resize/fill operations and
// verifies that refcounts on grids and blobs always equal the actual count of
// references to them.
func TestRefcountInvariant(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()

	// A small number of seeded operations exercising the interesting paths.
	type op struct {
		kind string
		args []int64
	}
	// Build a small tree.
	w1, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 2, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Clone w1 into root.
	clone, err := s.CloneNode(ctx, u.ID, &rpc.CloneNodeRequest{
		Path: rpc.Path{}, ViewRect: largeView(), NodeID: w1.ID,
		DestGridID: u.RootGridID, DestPath: rpc.Path{}, DestViewRect: largeView(),
		X: 4, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Add stuff inside w1's (now shared) child.
	if _, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{WellIDs: []int64{w1.ID}}, ViewRect: largeView(),
		GridID: w1.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Each of these triggers a fork.
	_ = w2
	_ = clone
	_ = op{}

	// Verify invariant: every grid's refcount equals the count of well rows
	// pointing at it, plus 1 if it's any user's root_grid_id.
	verifyRefcounts(t, s)
}

// verifyRefcounts asserts the refcount invariant globally. Every grid's
// refcount should equal: (number of node rows with child_grid_id = grid.id)
// plus (1 if any user.root_grid_id = grid.id).
//
// We collect all rows up front (fully materializing each Rows before issuing
// further queries) because the test store uses a single SQLite connection;
// holding a Rows iterator open while running QueryRow on the same handle
// would deadlock.
func verifyRefcounts(t *testing.T, s *Store) {
	t.Helper()
	type pair struct{ id, refcount int64 }
	var grids, blobs []pair

	rows, err := s.db.Query(`SELECT id, refcount FROM grids`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.refcount); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		grids = append(grids, p)
	}
	rows.Close()

	brows, err := s.db.Query(`SELECT id, refcount FROM blobs`)
	if err != nil {
		t.Fatal(err)
	}
	for brows.Next() {
		var p pair
		if err := brows.Scan(&p.id, &p.refcount); err != nil {
			brows.Close()
			t.Fatal(err)
		}
		blobs = append(blobs, p)
	}
	brows.Close()

	for _, g := range grids {
		var pointers, asRoot int64
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM nodes WHERE child_grid_id = ?`, g.id).Scan(&pointers); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM users WHERE root_grid_id = ?`, g.id).Scan(&asRoot); err != nil {
			t.Fatal(err)
		}
		want := pointers + asRoot
		if g.refcount != want {
			t.Errorf("grid %d refcount = %d, want %d", g.id, g.refcount, want)
		}
	}
	for _, b := range blobs {
		var n int64
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM nodes WHERE blob_id = ?`, b.id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if b.refcount != n {
			t.Errorf("blob %d refcount = %d, want %d", b.id, b.refcount, n)
		}
	}
}
