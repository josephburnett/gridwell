package server_test

// A move names one source's grid for another source's tile: home, the files
// and a far node each refuse it with the one sentence, before either side
// is asked, and nothing moves.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/files"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/server"
)

func wantMoveAcross(t *testing.T, what string, err error) {
	t.Helper()
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), rpc.MoveAcross) {
		t.Errorf("%s: %v, want InvalidArgument %q", what, err, rpc.MoveAcross)
	}
}

func TestAMoveBetweenHomeAndTheFilesIsRefusedEitherWay(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	writeFile(t, f, "a")
	n := newFilesNode(t, root)
	note, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.home,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 1, Y: 1, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = n.move(note, n.roots[0], 0, 0)
	wantMoveAcross(t, "home into a directory", err)
	if got, err := n.cl.GetTile(n.ctx, note.Id); err != nil || got.GridId != n.home {
		t.Errorf("the home tile after the refusal = %v, %v; want it on home's grid", got, err)
	}
	_, err = n.move(n.tile(n.roots[0], f), n.home, 0, 0)
	wantMoveAcross(t, "a file into home", err)
	if _, err := os.Stat(f); err != nil {
		t.Errorf("the file left its directory: %v", err)
	}
}

func TestAMoveBetweenThisNodeAndAFarOneIsRefusedEitherWay(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	writeFile(t, f, "a")
	h := buildTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil, nil,
		func(reg *server.Sources, st *store.Store) {
			src, err := files.New(st.Files(), []files.Root{{Path: root}}, byPath(func(string) error { return nil }))
			if err != nil {
				t.Fatal(err)
			}
			reg.Files = src
		})
	ctx := t.Context()
	lp, err := h.localCl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home, far := lp.HomeGridId, connectionRows(lp)[0].RootGridId
	x := int64(2)
	mk := func(grid string) *gridwellv1.Tile {
		x++
		tl, err := h.localCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: grid,
			Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: x, Y: 2, W: 1, H: 1}})
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	move := func(tl *gridwellv1.Tile, grid string) error {
		_, err := h.localCl.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: tl.Id, GridId: grid,
			X: 0, Y: 0, W: tl.W, H: tl.H, Claim: rpc.PlacementOf(tl)})
		return err
	}
	wantMoveAcross(t, "home onto a far grid", move(mk(home), far))
	wantMoveAcross(t, "a far tile onto home", move(mk(far), home))
	dir := rpc.QualifyID(localNodeID, rpc.KeyTileID(root))
	g, err := h.localCl.GetGrid(ctx, dir)
	if err != nil || len(g.Tiles) != 1 {
		t.Fatalf("the root lists %v, %v", g, err)
	}
	wantMoveAcross(t, "a file onto a far grid", move(g.Tiles[0], far))
	wantMoveAcross(t, "a far tile into a directory", move(mk(far), dir))
}
