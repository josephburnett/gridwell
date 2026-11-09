package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/files"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// A shell made on a directory's grid starts its tmux in that directory, read
// from the grid its row sits on when the tmux starts: through the web API,
// the router, home's OpenShell and the manager to the PTY backend. One on a
// home grid starts where the node does.
func TestAShellOnADirectoryStartsThere(t *testing.T) {
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	node, err := st.NodeUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fake := shellsvctest.New()
	fs, err := files.New(st.Files(), []files.Root{{Path: dir}}, func(*os.File, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fs.Close)
	hs := serveWeb(t, mustNew(t, &Sources{Home: local.New(st, shellsvc.NewManager(fake)), Files: fs}, Config{ID: node}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := &shellAPIFixture{hs: hs, cl: cl, fake: fake}
	for _, c := range []struct {
		grid, want string
	}{
		{rpc.QualifyID(node, rpc.KeyTileID(dir)), dir},
		{lp.HomeGridId, ""},
	} {
		tile, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: c.grid,
			Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 4, Y: 4, W: 1, H: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if rpc.IsFilesID(tile.Id) || tile.GridId != c.grid {
			t.Fatalf("the shell made on %s = %v, want home's row on it", c.grid, tile)
		}
		before := fake.AttachmentCount()
		cs := f.clientStack()
		cs.reg.Open(tile.Id, tile.Id, 80, 24)
		for deadline := time.Now().Add(3 * time.Second); fake.AttachmentCount() == before; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the shell on %s opened no PTY", c.grid)
			}
		}
		if att := fake.LastAttachment(); att.StartDir != c.want {
			t.Errorf("the shell on %s started in %q, want %q", c.grid, att.StartDir, c.want)
		}
		cs.reg.Close(tile.Id)
	}
}
