package server

// The shipped fs plugin's text tiles, spawned as the loader spawns it: what a
// listing declares a body to be, and what the body then is, must agree, or
// the client renders a tile as something it is not.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

const fsPresentationNS = "up2"

// Every text tile declares a presentation and no state, and its body is the
// media type the declaration names: plain is text/plain, both is markdown.
// Past the plugin's body cap a file still answers in its declaration's form.
func TestFsTextTilesDeclareWhatTheirBodiesAre(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"notes.md":  "# notes\n",
		"small.log": "one line\n",
		"blob.bin":  "\x00\x01\x02",
		"README":    "read me\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"big.log", "big.md"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(filepath.Join(root, name), 4<<20+1); err != nil {
			t.Fatal(err)
		}
	}

	cl := newPluginClient(t, "fs", map[string]string{"root": root})
	ctx := t.Context()
	info, err := cl.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: plugintest.Landing(t, info)})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tl := range grid.Tiles {
		if tl.Kind != rpc.KindText {
			continue
		}
		seen++
		if tl.StatusDetail != "" {
			t.Errorf("%s carries status_detail %q; a file has no state worth a word", tl.AltText, tl.StatusDetail)
		}
		want := map[string]string{rpc.TextPresentationPlain: "text/plain", rpc.TextPresentationBoth: "text/markdown"}[tl.TextPresentation]
		if want == "" {
			t.Errorf("%s declares text_presentation %q; every text entry declares plain or both", tl.AltText, tl.TextPresentation)
			continue
		}
		var mediaType string
		err := cl.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: tl.Id}, func(ch *gridwellv1.ContentChunk) error {
			if mediaType == "" {
				mediaType = ch.GetMediaType()
			}
			return nil
		})
		if err != nil {
			t.Errorf("read %s: %v", tl.AltText, err)
			continue
		}
		if mediaType != want {
			t.Errorf("%s declares %q and its body is %q; want %q", tl.AltText, tl.TextPresentation, mediaType, want)
		}
	}
	if seen != len(files)+2 {
		t.Fatalf("%d text tiles, want %d", seen, len(files)+2)
	}
}

// A file the plugin may not read is refused with that reason at the door, not
// answered as absent.
func TestFsUnreadablePageIsForbiddenNotMissing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked.html")
	if err := os.WriteFile(locked, []byte("<p>secret</p>"), 0o000); err != nil {
		t.Fatal(err)
	}
	cl := newPluginClient(t, "fs", map[string]string{"root": root})
	reg := plugin.NewRegistry()
	reg.Register(fsPresentationNS, "fs", cl, nil)
	hs := webDoorTest(t, mustNew(t, reg, Config{}).WebHandler())

	ctx := t.Context()
	info, err := cl.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	grid, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: plugintest.Landing(t, info)})
	if err != nil {
		t.Fatal(err)
	}
	tl := tileByLabel(t, grid, "locked.html")
	res, body := get(t, hs.Client(), rpc.PageURL(hs.URL, ContentToken(testPassword), fsPresentationNS+"/"+tl.Id), "")
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("unreadable page = %d %q, want 403", res.StatusCode, body)
	}
}
