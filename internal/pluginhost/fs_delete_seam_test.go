package pluginhost_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// A delete the real fs binary cannot carry out, because the directory holding
// the file has gone unreadable, reaches the caller as an error naming the
// reason. The row the user placed stays, and so does the file.
func TestFsDeleteInAnUnreadableDirectorySurfacesAndKeepsTheRow(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permissions do not bind here")
	}
	root := seedTree(t)
	cl, st := pluginNodeAt(t, root, filepath.Join(t.TempDir(), "mem.db"))
	ctx := context.Background()
	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := plugintest.LandingOf(t, pl.Plugins[0])
	g, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	notes := tileNamed(g.Tiles, "notes.md")
	if _, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: notes.Id, GridId: rootGrid, X: 7, Y: 3, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}
	row := rowIDOf(t, st.Namespace("p1"), ".", "notes.md")

	lighten := darken(t, root)
	err = cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: notes.Id})
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable || !strings.Contains(ce.Message(), "permission denied") {
		t.Fatalf("DeleteTile in an unreadable directory = %v, want Unavailable naming the reason", err)
	}
	lighten()

	if got := rowIDOf(t, st.Namespace("p1"), ".", "notes.md"); got != row {
		t.Errorf("the row is %s after the refused delete, want %s still", got, row)
	}
	if _, err := os.Stat(filepath.Join(root, "notes.md")); err != nil {
		t.Errorf("the file went with a refused delete: %v", err)
	}
}
