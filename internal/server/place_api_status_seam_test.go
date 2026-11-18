package server_test

// A reference that cannot be read answers one status per cause, whichever
// arm of the content GET reads it.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

func TestAReferenceThatCannotBeReadAnswersOneStatusPerCause(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory its mode refuses")
	}
	root := t.TempDir()
	locked, gone := filepath.Join(root, "locked"), filepath.Join(root, "gone")
	secret := filepath.Join(root, "secret.txt")
	for _, d := range []string{locked, gone} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, secret, "words")
	n := newFilesNode(t, root)
	door := func(dir string, x int64) string {
		t.Helper()
		row := n.tile(n.roots[0], dir)
		link, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: n.home, Tile: &gridwellv1.Tile{
			Kind: rpc.KindDoor, X: x, Y: 0, W: 1, H: 1, ChildGridId: row.ChildGridId, AltText: row.AltText}})
		if err != nil {
			t.Fatal(err)
		}
		return rpc.LocalOf(link.Id)
	}
	toLocked, toGone := door(locked, 0), door(gone, 1)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secret, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	undeclared := localNodeID + "/nope/9"

	for _, c := range []struct {
		cause, arm string
		at         pane.URLState
		want       int
	}{
		{"a missing grid", "grid", pane.URLState{Anchor: localNodeID + "/99999"}, http.StatusNotFound},
		{"a missing tile", "tile", pane.URLState{Anchor: n.home, TileIDs: []string{"99998"}}, http.StatusNotFound},
		{"a dead link", "grid", pane.URLState{Anchor: undeclared}, http.StatusNotFound},
		{"a dead link", "tile", pane.URLState{Anchor: undeclared, TileIDs: []string{"7"}}, http.StatusNotFound},
		{"a dead link", "door", pane.URLState{Anchor: n.home, TileIDs: []string{toGone}}, http.StatusNotFound},
		{"a refused permission", "grid", pane.URLState{Anchor: fileID(locked)}, http.StatusForbidden},
		{"a refused permission", "door", pane.URLState{Anchor: n.home, TileIDs: []string{toLocked}}, http.StatusForbidden},
		{"a refused permission", "content", pane.URLState{Anchor: n.roots[0], TileIDs: []string{rpc.KeyTileID(secret)}}, http.StatusForbidden},
	} {
		t.Run(c.cause+" through the "+c.arm, func(t *testing.T) {
			req, err := http.NewRequestWithContext(n.ctx, http.MethodGet, n.url+pane.EncodeURL(c.at), nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+servertest.Password)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("GET %s = %d, want %d", pane.EncodeURL(c.at), resp.StatusCode, c.want)
			}
		})
	}
}
