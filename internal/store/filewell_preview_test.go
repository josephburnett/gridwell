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

	// The file-well's child grid must be an FS source grid for "/".
	var srcKind, srcID string
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(source_kind,''), COALESCE(source_id,'') FROM grids WHERE id = ?`,
		fw.ChildGridID).Scan(&srcKind, &srcID)
	if err != nil {
		t.Fatalf("query child grid: %v", err)
	}
	if srcKind != rpc.GridSourceFS || srcID != "/" {
		t.Fatalf("file-well child grid source = (%q,%q), want (fs,/)", srcKind, srcID)
	}
}
