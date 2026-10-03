package server

// The shipped fs plugin's symlinks through the whole stack: the spawned
// binary, the adapter, the router and the browser door. A symlink is a link
// to the one key of what it lands on; one that lands outside the root, or
// nowhere, is a dead link, and nothing outside the root is ever served.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

const fsSymlinkNS = "fsl"

func TestFsSymlinksAreLinksAndNothingOutsideTheRootServes(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "dir"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(root, "real.md"):        "# real",
		filepath.Join(outside, "secret.html"): "<p>secret</p>",
		filepath.Join(outside, "secret.md"):   "# secret",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, dest := range map[string]string{
		"file": "real.md", "dirlink": "dir", "broken": "missing.md",
		"out.md": "../outside/secret.md", "out.html": "../outside/secret.html",
	} {
		if err := os.Symlink(dest, filepath.Join(root, name)); err != nil {
			t.Skip("symlinks unsupported")
		}
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	registerPrimaryLocaldb(t, reg, st)
	reg.Register(fsSymlinkNS, "fs", newPluginClient(t, "fs", map[string]string{"root": root}), nil)
	hs := serveWeb(t, mustNew(t, reg, Config{}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx := context.Background()
	if _, err := cl.Handshake(ctx); err != nil {
		t.Fatal(err)
	}

	g, err := cl.GetGrid(ctx, rpc.QualifyID(fsSymlinkNS, rpc.EntryGridID(".")))
	if err != nil {
		t.Fatal(err)
	}
	tiles := map[string]*gridwellv1.Tile{}
	for _, tl := range g.Tiles {
		tiles[tl.AltText] = tl
	}
	entry := func(context, key string) string {
		return rpc.QualifyID(fsSymlinkNS, rpc.EntryTileID(context, key))
	}

	// A link to a file in the tree reads as a link with its target's face.
	file := tiles["file"]
	if file == nil || !file.Reference || file.LinkTargetId != entry(".", "real.md") {
		t.Fatalf("file = %+v, want a link to real.md", file)
	}
	if file.Kind != rpc.KindText {
		t.Errorf("file = kind %q, want real.md's text", file.Kind)
	}
	if target, err := cl.GetTile(ctx, file.LinkTargetId); err != nil || target.TextPresentation != rpc.TextPresentationBoth {
		t.Errorf("the link's target = %+v, %v; want real.md presenting both", target, err)
	}
	if body, _, _, err := cl.ReadContent(ctx, file.Id); err != nil || string(body) != "# real" {
		t.Errorf("ReadContent through the link = %q, %v; want real.md's body", body, err)
	}
	if _, err := cl.GetTilePreview(ctx, file.Id); err != nil {
		t.Errorf("GetTilePreview through the link: %v", err)
	}

	// A link to a directory is a well onto that directory's own grid.
	if d := tiles["dirlink"]; d == nil || d.Kind != rpc.KindWell || d.ChildGridId != rpc.QualifyID(fsSymlinkNS, rpc.EntryGridID("dir")) {
		t.Errorf("dirlink = %+v, want a well onto dir", d)
	}

	// A link out of the root, or to nothing, reads dead at every verb.
	for _, name := range []string{"out.md", "out.html", "broken"} {
		tl := tiles[name]
		if tl == nil || !tl.Reference {
			t.Fatalf("%s = %+v, want a link", name, tl)
		}
		if _, _, _, err := cl.ReadContent(ctx, tl.Id); !gwerr.IsDeadRef(err) {
			t.Errorf("ReadContent(%s) = %v, want dead", name, err)
		}
		if _, err := cl.GetTilePreview(ctx, tl.Id); !gwerr.IsDeadRef(err) {
			t.Errorf("GetTilePreview(%s) = %v, want dead", name, err)
		}
	}

	// And the page door serves nothing outside the root, by the link or by
	// the address it names.
	for _, id := range []string{tiles["out.html"].Id, entry("..", "../outside/secret.html"), entry(".", "out.html")} {
		local := strings.TrimPrefix(id, fsSymlinkNS+"/")
		res, body := get(t, hs.Client(), rpc.PageURL(hs.URL, ContentToken(testPassword), fsSymlinkNS+"/"+local), "")
		if res.StatusCode == http.StatusOK || strings.Contains(body, "secret") {
			t.Errorf("page %s = %d %q; want nothing outside the root served", id, res.StatusCode, body)
		}
	}
}
