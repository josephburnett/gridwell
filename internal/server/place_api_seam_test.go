package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/shellpaste"
	"github.com/josephburnett/gridwell/internal/files"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/mcpapi"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// A reference read with the password, as a bearer or the cookie, answers
// the content its place names; without either it is refused; and a
// browser's navigation of it still gets the client.
func TestAReferenceAnswersItsContent(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "plain words")
	writeFile(t, filepath.Join(root, "page.pdf"), "%PDF-1.4 pretend")
	reg := &server.Sources{Home: local.New(st, nil)}
	src, err := files.New(st.Files(), []files.Root{{Path: root}}, byPath(os.RemoveAll))
	if err != nil {
		t.Fatal(err)
	}
	reg.Files = src
	static := fstest.MapFS{"index.html": {Data: []byte("<html>client</html>")}}
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID, StaticFS: static}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home := lp.HomeGridId
	mk := func(tl *gridwellv1.Tile, body string) *gridwellv1.Tile {
		t.Helper()
		made, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: home, Tile: tl}, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return made
	}
	text := mk(&gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}, "# Notes\nbody")
	site := mk(&gridwellv1.Tile{Kind: rpc.KindURL, X: 1, Y: 0, W: 1, H: 1, UrlString: "https://example.com/a"}, "")
	door := mk(&gridwellv1.Tile{Kind: rpc.KindDoor, X: 2, Y: 0, W: 1, H: 1, AltText: "Hall"}, "")
	desk := mk(&gridwellv1.Tile{Kind: rpc.KindWorkspace, X: 3, Y: 0, W: 1, H: 1, AltText: "Desk"},
		`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`)
	if _, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: door.ChildGridId,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("Inside")); err != nil {
		t.Fatal(err)
	}
	fileRow := func(name string) *gridwellv1.Tile {
		row, err := cl.GetTile(ctx, rpc.QualifyID(localNodeID, rpc.KeyTileID(filepath.Join(root, name))))
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	notes, page := fileRow("notes.txt"), fileRow("page.pdf")

	bearer := func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+servertest.Password) }
	get := func(c *http.Client, ref string, set ...func(*http.Request)) (int, string, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range set {
			s(req)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	ref := func(row *gridwellv1.Tile) string { return shellpaste.Reference(hs.URL, row) }
	curl := &http.Client{}

	for _, c := range []struct {
		name, ref, typ, body string
	}{
		{"a text's body", ref(text), "text/markdown", "# Notes\nbody"},
		{"a url's address", ref(site), "text/uri-list", "https://example.com/a\r\n"},
		{"a plain file's bytes", ref(notes), "text/plain", "plain words"},
		{"a page's bytes", ref(page), "application/pdf", "%PDF-1.4 pretend"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, typ, body := get(curl, c.ref, bearer)
			if code != http.StatusOK || !strings.HasPrefix(typ, c.typ) || body != c.body {
				t.Errorf("GET %s with a bearer = %d %q %q, want 200 %q %q", c.ref, code, typ, body, c.typ, c.body)
			}
		})
	}
	t.Run("a door is its grid as read_grid answers", func(t *testing.T) {
		code, typ, body := get(curl, ref(door), bearer)
		var g mcpapi.GridOut
		if code != http.StatusOK || typ != "application/json" || json.Unmarshal([]byte(body), &g) != nil ||
			g.Grid.ID != door.ChildGridId || len(g.Tiles) != 1 || g.Tiles[0].Name != "Inside" {
			t.Errorf("GET the door = %d %q %s, want its grid with one tile", code, typ, body)
		}
	})
	t.Run("a workspace is its layout as read_tiles answers", func(t *testing.T) {
		code, _, body := get(curl, ref(desk), bearer)
		var out mcpapi.TileOut
		if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil || out.Tile.ID != desk.Id || out.Layout == nil {
			t.Errorf("GET the workspace = %d %s, want its row and layout", code, body)
		}
	})
	t.Run("the root path is home's grid", func(t *testing.T) {
		code, _, body := get(curl, hs.URL+"/", bearer)
		var g mcpapi.GridOut
		if code != http.StatusOK || json.Unmarshal([]byte(body), &g) != nil || g.Grid.ID != home {
			t.Errorf("GET / = %d %s, want home's grid", code, body)
		}
	})
	t.Run("the cookie reads it too", func(t *testing.T) {
		if code, _, body := get(hs.Client(), ref(text)); code != http.StatusOK || body != "# Notes\nbody" {
			t.Errorf("GET with the cookie = %d %q", code, body)
		}
	})
	t.Run("nothing else does", func(t *testing.T) {
		if code, _, body := get(curl, ref(text)); code != http.StatusUnauthorized || strings.Contains(body, "Notes") {
			t.Errorf("GET with no password = %d %q, want 401 and no body", code, body)
		}
		wrong := func(req *http.Request) { req.Header.Set("Authorization", "Bearer nope") }
		if code, _, _ := get(curl, ref(text), wrong); code != http.StatusUnauthorized {
			t.Errorf("GET with a wrong bearer = %d, want 401", code)
		}
	})
	t.Run("a browser's navigation gets the client", func(t *testing.T) {
		html := func(req *http.Request) { req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8") }
		if code, _, body := get(hs.Client(), ref(text), html); code != http.StatusOK || body != "<html>client</html>" {
			t.Errorf("a navigation = %d %q, want the client", code, body)
		}
	})
}
