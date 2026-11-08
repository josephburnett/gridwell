package server_test

// The files' text tiles through the web API: what a listing declares a body
// to be, and what the body then is, must agree, or the client renders a tile
// as something it is not.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Every text tile declares a presentation, and its body is the
// media type the declaration names: plain is text/plain, both is markdown.
// Past the body cap a file still answers in its declaration's form.
func TestFilesTextTilesDeclareWhatTheirBodiesAre(t *testing.T) {
	root := t.TempDir()
	bodies := map[string]string{
		"notes.md":  "# notes\n",
		"small.log": "one line\n",
		"blob.bin":  "\x00\x01\x02",
		"README":    "read me\n",
	}
	for name, body := range bodies {
		writeFile(t, filepath.Join(root, name), body)
	}
	for _, name := range []string{"big.log", "big.md"} {
		writeFile(t, filepath.Join(root, name), "")
		if err := os.Truncate(filepath.Join(root, name), 4<<20+1); err != nil {
			t.Fatal(err)
		}
	}
	n := newFilesNode(t, root)
	seen := 0
	for _, tl := range n.grid(n.roots[0]).Tiles {
		if tl.Kind != rpc.KindText {
			continue
		}
		seen++
		want := map[string]string{rpc.TextPresentationPlain: "text/plain", rpc.TextPresentationBoth: "text/markdown"}[tl.TextPresentation]
		if want == "" {
			t.Errorf("%s declares text_presentation %q; every text tile declares plain or both", tl.AltText, tl.TextPresentation)
			continue
		}
		_, mediaType, _, err := n.cl.ReadContent(n.ctx, tl.Id)
		if err != nil {
			t.Errorf("read %s: %v", tl.AltText, err)
			continue
		}
		if mediaType != want {
			t.Errorf("%s declares %q and its body is %q; want %q", tl.AltText, tl.TextPresentation, mediaType, want)
		}
	}
	if seen != len(bodies)+2 {
		t.Fatalf("%d text tiles, want %d", seen, len(bodies)+2)
	}
}

// A page file the node may not read is refused as forbidden at the content
// API: a permission is a verdict, never an absence or an outage.
func TestFilesUnreadablePageIsRefusedNotMissing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked.pdf")
	if err := os.WriteFile(locked, []byte("%PDF-1.4\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	tl := n.tile(n.roots[0], locked)
	lp, err := n.cl.Handshake(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(rpc.PageURL(n.url, lp.ContentToken, tl.Id))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("unreadable page = %d, want 403", res.StatusCode)
	}
}
