package server_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A symlink out of the root, or to nothing, reads dead at every verb through
// the web API, and the content API serves nothing outside the root, by the
// link or by the address it names.
func TestFilesSymlinksOutOfTheRootServeNothing(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	writeFile(t, filepath.Join(root, "real.md"), "# real")
	writeFile(t, filepath.Join(outside, "secret.pdf"), "%PDF secret")
	writeFile(t, filepath.Join(outside, "secret.md"), "# secret")
	for name, dest := range map[string]string{
		"file": "real.md", "broken": "missing.md",
		"out.md": "../outside/secret.md", "out.pdf": "../outside/secret.pdf",
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
	for _, name := range []string{"out.md", "out.pdf", "broken"} {
		tl := n.tile(n.roots[0], filepath.Join(root, name))
		if !tl.Link {
			t.Fatalf("%s = %+v, want a link", name, tl)
		}
		if _, _, _, err := n.cl.ReadContent(n.ctx, tl.Id); !gwerr.IsDeadLink(err) {
			t.Errorf("ReadContent(%s) = %v, want dead", name, err)
		}
		if _, err := n.cl.GetTilePreview(n.ctx, tl.Id); !gwerr.IsDeadLink(err) {
			t.Errorf("GetTilePreview(%s) = %v, want dead", name, err)
		}
	}
	lp, err := n.cl.Handshake(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{fileID(filepath.Join(root, "out.pdf")), fileID(filepath.Join(outside, "secret.pdf"))} {
		res, err := http.Get(rpc.PageURL(n.url, lp.ContentToken, id))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound || strings.Contains(string(body), "secret") {
			t.Errorf("page %s = %d %q; want dead, nothing outside the root served", id, res.StatusCode, body)
		}
	}
}
