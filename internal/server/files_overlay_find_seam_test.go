package server_test

// Find reaches the Gridwell tiles over a directory on their path.

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A Gridwell tile over a directory is found on its path: the directories
// from the root, then the Gridwell doors; a recursive find from the root
// reaches one inside a Gridwell door on a subdirectory.
func TestAGridwellTileOverADirectoryIsFoundOnItsPath(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "a", "b")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	note := n.overText(fileID(b), 1, 1, "Plan")
	door, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: fileID(b),
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 2, Y: 2, W: 1, H: 1, AltText: "Notes"}})
	if err != nil {
		t.Fatal(err)
	}
	inner := n.overText(door.ChildGridId, 0, 0, "Inner")
	pathOf := func(r *gridwellv1.FindResult) []string {
		var ids []string
		for _, d := range r.Path {
			ids = append(ids, d.Id)
		}
		return ids
	}
	for _, c := range []struct {
		req  *gridwellv1.FindRequest
		id   string
		path []string
	}{
		{&gridwellv1.FindRequest{Query: "id:" + note.Id}, note.Id, []string{fileID(a), fileID(b)}},
		{&gridwellv1.FindRequest{Query: "Plan"}, note.Id, []string{fileID(a), fileID(b)}},
		{&gridwellv1.FindRequest{GridId: n.roots[0], Kind: rpc.KindText, Recurse: true}, inner.Id, []string{fileID(a), fileID(b), door.Id}},
	} {
		resp, err := n.raw.Find(n.ctx, connect.NewRequest(c.req))
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(resp.Msg.Results, func(r *gridwellv1.FindResult) bool { return r.Tile.GetId() == c.id })
		if i < 0 {
			t.Errorf("find %v missed %s: %v", c.req, c.id, resp.Msg.Results)
			continue
		}
		if got := pathOf(resp.Msg.Results[i]); !slices.Equal(got, c.path) {
			t.Errorf("find %v: %s on path %v, want %v", c.req, c.id, got, c.path)
		}
	}
}
