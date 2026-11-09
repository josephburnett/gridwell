package server_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A symlink out of the root is followed through the web API: it reads its
// target's bytes, and the content API serves the target's page by the link
// or by the address it names. One to nothing reads dead.
func TestFilesSymlinksOutOfTheRootAreFollowed(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	writeFile(t, filepath.Join(root, "real.md"), "# real")
	writeFile(t, filepath.Join(outside, "far.pdf"), "%PDF far")
	writeFile(t, filepath.Join(outside, "far.md"), "# far")
	for name, dest := range map[string]string{
		"file": "real.md", "broken": "missing.md",
		"out.md": "../outside/far.md", "out.pdf": "../outside/far.pdf",
	} {
		if err := os.Symlink(dest, filepath.Join(root, name)); err != nil {
			t.Skip("symlinks unsupported")
		}
	}
	n := newFilesNode(t, root)
	file := n.tile(n.roots[0], filepath.Join(root, "file"))
	if _, err := n.cl.GetTilePreview(n.ctx, file.Id); err != nil {
		t.Errorf("GetTilePreview through a link in the root: %v", err)
	}
	out := n.tile(n.roots[0], filepath.Join(root, "out.md"))
	if body, _, _, err := n.cl.ReadContent(n.ctx, out.Id); err != nil || string(body) != "# far" {
		t.Errorf("ReadContent(out.md) = %q, %v; want the target's bytes", body, err)
	}
	broken := n.tile(n.roots[0], filepath.Join(root, "broken"))
	if !broken.Link {
		t.Fatalf("broken = %+v, want a link", broken)
	}
	if _, _, _, err := n.cl.ReadContent(n.ctx, broken.Id); !gwerr.IsDeadLink(err) {
		t.Errorf("ReadContent(broken) = %v, want dead", err)
	}
	lp, err := n.cl.Handshake(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{fileID(filepath.Join(root, "out.pdf")), fileID(filepath.Join(outside, "far.pdf"))} {
		res, err := http.Get(rpc.PageURL(n.url, lp.ContentToken, id))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || string(body) != "%PDF far" {
			t.Errorf("page %s = %d %q; want the target's page", id, res.StatusCode, body)
		}
	}
}
