package store

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestFileWellChildGridDistinctFromWell guards the reported bug where a
// freshly dropped file-well showed a grid-well's preview. A file-well must
// point at the FS singleton grid for its path, never at a grid-well's child
// grid.
func TestFileWellChildGridDistinctFromWell(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	gw, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("create well: %v", err)
	}
	fw, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 2, Y: 0, W: 1, H: 1, FSPath: "/",
	})
	if err != nil {
		t.Fatalf("create file-well: %v", err)
	}

	if fw.Kind != rpc.KindFileWell {
		t.Fatalf("file-well kind = %q", fw.Kind)
	}
	if fw.ChildGridID == 0 {
		t.Fatal("file-well has no child grid")
	}
	if fw.ChildGridID == gw.ChildGridID {
		t.Fatalf("file-well child grid %d == grid-well child grid %d (cross-wired)",
			fw.ChildGridID, gw.ChildGridID)
	}

	// The file-well's child grid must be an FS source grid for "/". Source
	// grids live in the attached cache database (id >= cacheIDBase).
	if !isCacheID(fw.ChildGridID) {
		t.Fatalf("file-well child grid %d is not a cache id (>= %d)", fw.ChildGridID, int64(cacheIDBase))
	}
	var srcKind, srcID string
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(source_kind,''), COALESCE(source_id,'') FROM `+schemaOf(fw.ChildGridID)+`grids WHERE id = ?`,
		fw.ChildGridID).Scan(&srcKind, &srcID)
	if err != nil {
		t.Fatalf("query child grid: %v", err)
	}
	if srcKind != rpc.GridSourceFS || srcID != "/" {
		t.Fatalf("file-well child grid source = (%q,%q), want (fs,/)", srcKind, srcID)
	}
}

// TestGridIDNotReusedAfterDelete is the root-cause guard for the file-well
// cross-wiring: a deleted grid's id must never be handed to a new grid, or
// the client (which caches grids by id) would render the deleted grid's
// stale tiles under the new one. AUTOINCREMENT on grids.id enforces this.
func TestGridIDNotReusedAfterDelete(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	a, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("create well A: %v", err)
	}
	deletedChild := a.ChildGridID

	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: a.ID, Version: a.Version,
	}); err != nil {
		t.Fatalf("delete well A: %v", err)
	}

	// The deleted well's child grid must actually be gone (refcount hit 0),
	// otherwise there's no freed id to reuse and this test proves nothing.
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM grids WHERE id = ?`, deletedChild).Scan(&n); err != nil {
		t.Fatalf("query deleted grid: %v", err)
	}
	if n != 0 {
		t.Fatalf("child grid %d not deleted (refcount > 0); test wouldn't exercise id reuse", deletedChild)
	}

	b, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("create well B: %v", err)
	}
	if b.ChildGridID == deletedChild {
		t.Fatalf("new grid reused deleted grid id %d", deletedChild)
	}
}
