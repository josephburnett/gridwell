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
//
// Where mutations need a tile version, we reload the tile right before the
// call so we don't have to track versions through the random walk.
func TestPropertyRefcountAndOverlap(t *testing.T) {
	const iters = 300
	rng := rand.New(rand.NewPCG(0xa5cea5ce, 0x42))

	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	type liveTile struct {
		id          int64
		kind        string
		gridID      int64
		path        rpc.Path
		w, h        int64
		x, y        int64
		childGridID string
	}
	var tiles []liveTile
	addTile := func(n *rpc.Tile, path rpc.Path) {
		tiles = append(tiles, liveTile{
			id: n.ID, kind: n.Kind, gridID: n.GridID, path: path,
			w: n.W, h: n.H, x: n.X, y: n.Y, childGridID: n.ChildGridID,
		})
	}

	w0, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	addTile(w0, rpc.Path{})

	// liveVersion reads the current version of a tile id, returning 0 and
	// reporting not-found if the row is gone.
	liveVersion := func(id int64) (int64, error) {
		t, err := s.loadTile(ctx, s.db, id)
		if err != nil {
			return 0, err
		}
		return t.Version, nil
	}

	for i := range iters {
		op := rng.IntN(6)
		switch op {
		case 0:
			// Create a tile at a random spot in some live well's child grid
			// or root. Kind is drawn from well/text/url/shell so clone, fork,
			// and delete get exercised across every refcounted reference a
			// tile can hold: child grid, text blob, and url/shell preview
			// blob. (Before this, the walk only made wells, which is exactly
			// why the preview-blob refcount leaks went uncaught.)
			parentPath := rpc.Path{}
			gridID := root
			if len(tiles) > 0 && rng.IntN(2) == 0 {
				ln := tiles[rng.IntN(len(tiles))]
				if ln.kind == rpc.KindWell && ln.childGridID != "" {
					parentPath = rpc.Path{WellIDs: append([]int64{}, ln.path.WellIDs...)}
					parentPath.WellIDs = append(parentPath.WellIDs, ln.id)
					gridID = parseID(ln.childGridID)
				}
			}
			x := int64(rng.IntN(20)) * 2
			y := int64(rng.IntN(20)) * 2
			w := int64(1 + rng.IntN(2))
			h := int64(1 + rng.IntN(2))
			var (
				n   *rpc.Tile
				err error
			)
			switch rng.IntN(4) {
			case 0:
				n, err = s.CreateWell(ctx, &rpc.CreateWellRequest{
					Path: parentPath, GridID: gridID, X: x, Y: y, W: w, H: h,
				})
			case 1:
				n, err = s.CreateText(ctx, &rpc.CreateTextRequest{
					Path: parentPath, GridID: gridID, X: x, Y: y, W: w, H: h,
					Data: []byte(fmt.Sprintf("# tile %d", i)),
				})
			case 2:
				n, err = s.CreateURL(ctx, &rpc.CreateURLRequest{
					Path: parentPath, GridID: gridID, X: x, Y: y, W: w, H: h,
					URL: fmt.Sprintf("https://example.com/%d", i),
				})
			case 3:
				n, err = s.CreateShell(ctx, &rpc.CreateShellRequest{
					Path: parentPath, GridID: gridID, X: x, Y: y, W: w, H: h,
				})
			}
			if err != nil {
				if !isBenignPropError(err) {
					t.Fatalf("iter %d create: %v", i, err)
				}
				continue
			}
			addTile(n, parentPath)
		case 1:
			// Clone a random tile into root.
			if len(tiles) == 0 {
				continue
			}
			src := tiles[rng.IntN(len(tiles))]
			ver, err := liveVersion(src.id)
			if err != nil {
				continue
			}
			x := int64(rng.IntN(20))*2 + 100
			y := int64(rng.IntN(20)) * 2
			n, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
				Path: src.path, TileID: src.id, Version: ver,
				DestGridID: root, DestPath: rpc.Path{},
				X: x, Y: y,
			})
			if err != nil {
				if !isBenignPropError(err) {
					t.Fatalf("iter %d clone: %v", i, err)
				}
				continue
			}
			addTile(n, rpc.Path{})
		case 2:
			// Resize a random tile.
			if len(tiles) == 0 {
				continue
			}
			pickIdx := rng.IntN(len(tiles))
			pick := tiles[pickIdx]
			ver, err := liveVersion(pick.id)
			if err != nil {
				continue
			}
			w := int64(1 + rng.IntN(3))
			h := int64(1 + rng.IntN(3))
			n, err := s.ResizeTile(ctx, &rpc.ResizeTileRequest{
				Path: pick.path, TileID: pick.id, Version: ver,
				X: pick.x, Y: pick.y, W: w, H: h,
			})
			if err != nil {
				if !isBenignPropError(err) {
					t.Fatalf("iter %d resize: %v", i, err)
				}
				continue
			}
			tiles[pickIdx].id = n.ID
			tiles[pickIdx].gridID = n.GridID
			tiles[pickIdx].w = n.W
			tiles[pickIdx].h = n.H
		case 3:
			// Delete a random tile (cascades through wells).
			if len(tiles) == 0 {
				continue
			}
			pickIdx := rng.IntN(len(tiles))
			pick := tiles[pickIdx]
			ver, err := liveVersion(pick.id)
			if err != nil {
				// gone; drop from harness
				tiles = append(tiles[:pickIdx], tiles[pickIdx+1:]...)
				continue
			}
			err = s.DeleteTile(ctx, &rpc.DeleteTileRequest{
				Path: pick.path, TileID: pick.id, Version: ver,
			})
			if err != nil && !isBenignPropError(err) {
				t.Fatalf("iter %d delete: %v", i, err)
			}
			if err == nil {
				deletedGrids := map[int64]bool{}
				if pick.kind == rpc.KindWell && pick.childGridID != "" {
					deletedGrids[parseID(pick.childGridID)] = true
				}
				next := tiles[:0]
				for _, n := range tiles {
					if n.id == pick.id {
						continue
					}
					if deletedGrids[n.gridID] {
						continue
					}
					next = append(next, n)
				}
				tiles = next
			}
		case 4:
			// Set viewport (well-only in the kind/version model).
			if len(tiles) == 0 {
				continue
			}
			pickIdx := rng.IntN(len(tiles))
			pick := tiles[pickIdx]
			if pick.kind != rpc.KindWell {
				continue
			}
			ver, err := liveVersion(pick.id)
			if err != nil {
				continue
			}
			n, err := s.SetWellView(ctx, &rpc.SetWellViewRequest{
				Path: pick.path, TileID: pick.id, Version: ver,
				ViewX: int64(rng.IntN(50)), ViewY: int64(rng.IntN(50)),
				ViewZoom: 1.0,
			})
			if err != nil && !isBenignPropError(err) {
				t.Fatalf("iter %d set well view: %v", i, err)
			}
			if err == nil {
				tiles[pickIdx].id = n.ID
			}
		case 5:
			// Freeze a preview onto a shell tile so preview_blob_id
			// refcounting gets exercised when that tile is later cloned,
			// forked, or deleted. A tiny fixed byte alphabet makes previews
			// dedupe across tiles, so a shared preview blob reaches
			// refcount > 1 — the case fork/clone used to mishandle.
			if len(tiles) == 0 {
				continue
			}
			pickIdx := rng.IntN(len(tiles))
			pick := tiles[pickIdx]
			if pick.kind != rpc.KindShell {
				continue
			}
			ver, err := liveVersion(pick.id)
			if err != nil {
				continue
			}
			n, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
				Path: pick.path, TileID: pick.id, Version: ver,
				JPEG: []byte{byte('a' + rng.IntN(3))},
			})
			if err != nil && !isBenignPropError(err) {
				t.Fatalf("iter %d set shell preview: %v", i, err)
			}
			if err == nil {
				tiles[pickIdx].id = n.ID
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

	byGrid := map[int64][]rec{}
	for _, r := range all {
		byGrid[r.grid] = append(byGrid[r.grid], r)
	}
	for g, rs := range byGrid {
		for i := range rs {
			for j := i + 1; j < len(rs); j++ {
				if overlap(rs[i], rs[j]) {
					t.Fatalf("grid %d: tiles %d and %d overlap", g,
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

func isBenignPropError(err error) bool {
	return errors.Is(err, ErrInvalidPath) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrOverlap) ||
		errors.Is(err, ErrVersionConflict)
}
