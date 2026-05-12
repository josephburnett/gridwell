package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestPropertyRefcountAndOverlap exercises a long random sequence of
// mutations and asserts:
//   - Refcounts on grids and blobs always match the actual reference count.
//   - No two tiles in the same grid overlap.
//   - The tile count in any grid equals the SQL count of tiles in it.
//
// The test does not aim for coverage of every code path; it stress-tests the
// invariants that the CoW logic is responsible for. The seed is fixed so
// failures reproduce, but it can be flipped to a wall-clock seed locally for
// fuzz-style runs.
func TestPropertyRefcountAndOverlap(t *testing.T) {
	const iters = 300
	rng := rand.New(rand.NewPCG(0xa5cea5ce, 0x42))

	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()

	// Seed a 1×1 well so the user has somewhere to descend.
	type liveNode struct {
		id          int64
		typ         string
		gridID      int64
		path        rpc.Path
		w, h        int64
		x, y        int64
		childGridID int64
	}
	var nodes []liveNode
	addNode := func(n *rpc.Tile, path rpc.Path) {
		nodes = append(nodes, liveNode{
			id: n.ID, typ: n.Type, gridID: n.GridID, path: path,
			w: n.W, h: n.H, x: n.X, y: n.Y, childGridID: n.ChildGridID,
		})
	}

	w0, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	addNode(w0, rpc.Path{})

	for i := range iters {
		// Pick a random op.
		op := rng.IntN(6)
		switch op {
		case 0:
			// Create a well at a random spot in some live well's child grid
			// or root.
			parentPath := rpc.Path{}
			gridID := u.RootGridID
			// Sometimes descend.
			if len(nodes) > 0 && rng.IntN(2) == 0 {
				ln := nodes[rng.IntN(len(nodes))]
				if ln.typ == "well" && ln.childGridID != 0 {
					parentPath = rpc.Path{WellIDs: append([]int64{}, ln.path.WellIDs...)}
					parentPath.WellIDs = append(parentPath.WellIDs, ln.id)
					gridID = ln.childGridID
				}
			}
			x := int64(rng.IntN(20)) * 2
			y := int64(rng.IntN(20)) * 2
			w := int64(1 + rng.IntN(2))
			h := int64(1 + rng.IntN(2))
			n, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
				Path: parentPath, ViewRect: largeView(), GridID: gridID, X: x, Y: y, W: w, H: h,
			})
			if err != nil {
				if !isBenignPropError(err) {
					t.Fatalf("iter %d create: %v", i, err)
				}
				continue
			}
			addNode(n, parentPath)
		case 1:
			// Clone a random node into root.
			if len(nodes) == 0 {
				continue
			}
			src := nodes[rng.IntN(len(nodes))]
			x := int64(rng.IntN(20))*2 + 100 // bias to "open" area
			y := int64(rng.IntN(20)) * 2
			n, err := s.CloneTile(ctx, u.ID, &rpc.CloneTileRequest{
				Path: src.path, ViewRect: largeView(), TileID: src.id,
				DestGridID: u.RootGridID, DestPath: rpc.Path{}, DestViewRect: largeView(),
				X: x, Y: y,
			})
			if err != nil {
				if !isBenignPropError(err) {
					t.Fatalf("iter %d clone: %v", i, err)
				}
				continue
			}
			addNode(n, rpc.Path{})
		case 2:
			// Resize a random node.
			if len(nodes) == 0 {
				continue
			}
			pickIdx := rng.IntN(len(nodes))
			pick := nodes[pickIdx]
			w := int64(1 + rng.IntN(3))
			h := int64(1 + rng.IntN(3))
			n, err := s.ResizeTile(ctx, u.ID, &rpc.ResizeTileRequest{
				Path: pick.path, ViewRect: largeView(), TileID: pick.id,
				X: pick.x, Y: pick.y, W: w, H: h,
			})
			if err != nil {
				if !isBenignPropError(err) {
					t.Fatalf("iter %d resize: %v", i, err)
				}
				continue
			}
			// CoW may have given us a new id.
			nodes[pickIdx].id = n.ID
			nodes[pickIdx].gridID = n.GridID
			nodes[pickIdx].w = n.W
			nodes[pickIdx].h = n.H
		case 3:
			// Cap a well.
			if len(nodes) == 0 {
				continue
			}
			pickIdx := rng.IntN(len(nodes))
			pick := nodes[pickIdx]
			if pick.typ != "well" {
				continue
			}
			n, err := s.CapWell(ctx, u.ID, &rpc.CapWellRequest{
				Path: pick.path, ViewRect: largeView(), TileID: pick.id,
			})
			if err != nil && !isBenignPropError(err) && !errors.Is(err, ErrCapped) {
				t.Fatalf("iter %d cap: %v", i, err)
			}
			if err == nil {
				nodes[pickIdx].id = n.ID
			}
		case 4:
			// Redig a well.
			if len(nodes) == 0 {
				continue
			}
			pickIdx := rng.IntN(len(nodes))
			pick := nodes[pickIdx]
			if pick.typ != "well" {
				continue
			}
			n, err := s.RedigWell(ctx, u.ID, &rpc.RedigWellRequest{
				Path: pick.path, ViewRect: largeView(), TileID: pick.id,
			})
			if err != nil && !isBenignPropError(err) && !errors.Is(err, ErrNotCapped) {
				t.Fatalf("iter %d redig: %v", i, err)
			}
			if err == nil {
				nodes[pickIdx].id = n.ID
			}
		case 5:
			// Set viewport.
			if len(nodes) == 0 {
				continue
			}
			pickIdx := rng.IntN(len(nodes))
			pick := nodes[pickIdx]
			n, err := s.SetTileViewport(ctx, u.ID, &rpc.SetTileViewportRequest{
				Path: pick.path, ViewRect: largeView(), TileID: pick.id,
				ViewX: int64(rng.IntN(50)), ViewY: int64(rng.IntN(50)),
			})
			if err != nil && !isBenignPropError(err) {
				t.Fatalf("iter %d set viewport: %v", i, err)
			}
			if err == nil {
				nodes[pickIdx].id = n.ID
			}
		}

		if i%30 == 29 {
			verifyRefcounts(t, s)
			verifyNoOverlap(t, s)
		}
	}
	verifyRefcounts(t, s)
	verifyNoOverlap(t, s)
}

// verifyNoOverlap asserts that within every grid, no two tiles overlap.
func verifyNoOverlap(t *testing.T, s *Store) {
	t.Helper()
	rows, err := s.db.Query(`SELECT id, grid_id, x, y, w, h FROM tiles`)
	if err != nil {
		t.Fatal(err)
	}
	type rec struct {
		id, grid, x, y, w, h int64
	}
	var all []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.grid, &r.x, &r.y, &r.w, &r.h); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		all = append(all, r)
	}
	rows.Close()

	// O(n^2) per grid, but n is small in these tests.
	byGrid := map[int64][]rec{}
	for _, r := range all {
		byGrid[r.grid] = append(byGrid[r.grid], r)
	}
	for g, rs := range byGrid {
		for i := range rs {
			for j := i + 1; j < len(rs); j++ {
				if overlap(rs[i], rs[j]) {
					t.Fatalf("grid %d: nodes %d and %d overlap", g,
						rs[i].id, rs[j].id)
				}
			}
		}
	}
}

func overlap(a, b struct {
	id, grid, x, y, w, h int64
}) bool {
	if a.x >= b.x+b.w || b.x >= a.x+a.w {
		return false
	}
	if a.y >= b.y+b.h || b.y >= a.y+a.h {
		return false
	}
	return true
}

// silence the unused-import in stress builds
var _ = fmt.Sprintf

// isBenignPropError reports whether err is one the property test should skip
// over without aborting. CoW forks rewrite well row ids, so paths cached in
// the test harness can become stale (ErrInvalidPath) or refer to deleted
// rows (ErrNotFound). Overlap and permission denials are also legitimate
// outcomes of randomly-generated requests.
func isBenignPropError(err error) bool {
	return errors.Is(err, ErrInvalidPath) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrOverlap) ||
		errors.Is(err, ErrPermissionDenied)
}
