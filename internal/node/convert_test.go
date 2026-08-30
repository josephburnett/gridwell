package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
	"github.com/josephburnett/gridwell/internal/remote"
)

// legacyMemoryDDL is the retired per-plugin memory-DB schema, as the
// converter reads it.
const legacyMemoryDDL = `
CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE contexts (grid_id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL UNIQUE, root_cx REAL, root_cy REAL, root_zoom REAL);
CREATE TABLE idmap (tile_id INTEGER PRIMARY KEY AUTOINCREMENT, grid_id INTEGER NOT NULL, key TEXT NOT NULL, tombstoned INTEGER NOT NULL DEFAULT 0);
CREATE TABLE cache_listings (grid_id INTEGER PRIMARY KEY, entries BLOB NOT NULL, authoritative INTEGER NOT NULL DEFAULT 0);
CREATE TABLE layout (tile_id INTEGER PRIMARY KEY, x INTEGER NOT NULL DEFAULT 0, y INTEGER NOT NULL DEFAULT 0, w INTEGER NOT NULL DEFAULT 1, h INTEGER NOT NULL DEFAULT 1,
  view_x INTEGER NOT NULL DEFAULT 0, view_y INTEGER NOT NULL DEFAULT 0, view_zoom REAL NOT NULL DEFAULT 1.0,
  text_x INTEGER NOT NULL DEFAULT 0, text_y INTEGER NOT NULL DEFAULT 0, text_w INTEGER NOT NULL DEFAULT 0, text_h INTEGER NOT NULL DEFAULT 0,
  text_mode TEXT NOT NULL DEFAULT '', content_zoom REAL NOT NULL DEFAULT 0);`

// TestConvertFoldsALegacyHome builds a home in the per-namespace layout — a
// home store with an exit well into a plugin grid, a leaf link into a plugin
// tile, and a pane layout anchored in the plugin; the plugin's memory DB with
// two contexts, three keys, one of them retired, and a cached listing; and a
// transport store with one learned root — converts it, and checks that home
// keeps its ids, the plugin's rows are re-minted under their namespace with
// their facts and framing, every reference into the plugin is rewritten, the
// connections are remembered, and the old files are set aside.
func TestConvertFoldsALegacyHome(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	const nodeID, pid = "n0de1", "plug1"
	cfg := &config.ServerConfig{ID: nodeID, Plugins: []config.PluginConfig{{ID: pid, Kind: "fs"}}}

	// The old home store.
	oldDir := config.LegacyDBDir(home, nodeID)
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	homeDB := filepath.Join(oldDir, "store.db")
	if err := pluginmeta.Create(homeDB, nodeID, "home"); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(homeDB)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := st.RootGridID(ctx)
	// Old plugin ids: grid 1 (root), grid 2 (a dir), tiles 1..3.
	exit, err := st.CreateExitWell(ctx, root, 0, 0, 1, 1, pid+"/2", "docs", rpc.Framing{})
	if err != nil {
		t.Fatal(err)
	}
	link, err := st.CreateLeafLink(ctx, root, 1, 0, 1, 1, "text", pid+"/3", "notes")
	if err != nil {
		t.Fatal(err)
	}
	layout, _ := json.Marshal(map[string]any{"v": 1, "root": map[string]any{"pane": map[string]any{
		"id": "a", "anchor": pid + "/1", "path": []string{pid + "/3"},
		"up": []map[string]any{{"anchor": nodeID + "/" + root, "path": []string{}}},
	}}})
	paneTile, err := st.CreatePane(ctx, root, 2, 0, 1, 1, "ws", layout)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	// The old plugin memory.
	memDir := config.LegacyDBDir(home, pid)
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mem, err := sql.Open("sqlite", filepath.Join(memDir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := mem.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(legacyMemoryDDL)
	must(`INSERT INTO contexts (grid_id, key, root_cx, root_cy, root_zoom) VALUES (1, 'root', 2.5, -1, 0.75), (2, 'root/docs', NULL, NULL, NULL)`)
	must(`INSERT INTO idmap (tile_id, grid_id, key, tombstoned) VALUES (1, 1, 'docs', 0), (2, 1, 'gone.md', 1), (3, 1, 'notes.md', 0)`)
	must(`INSERT INTO layout (tile_id, x, y, w, h, view_x, view_y, view_zoom, text_y, text_mode, content_zoom) VALUES (1, 4, 4, 2, 2, 7, 8, 1.5, 0, '', 0), (2, 0, 0, 1, 1, 0, 0, 1, 0, '', 0), (3, 5, 5, 1, 1, 0, 0, 1, 120, 'rendered', 1.25)`)
	listing, _ := proto.Marshal(&pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
		{Key: "docs", Kind: "well", Label: "docs", ChildContext: "root/docs"},
		{Key: "notes.md", Kind: "text", Label: "notes.md"},
	}})
	must(`INSERT INTO cache_listings (grid_id, entries, authoritative) VALUES (1, ?, 1)`, listing)
	mem.Close()

	// The old transport store.
	rsql, err := sql.Open("sqlite", filepath.Join(oldDir, "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	rdb, err := remote.NewDB(rsql)
	if err != nil {
		t.Fatal(err)
	}
	_ = rdb.Ensure(ctx, "geneva")
	_ = rdb.SetRemoteRoot(ctx, "geneva", "rn/1")
	_ = rdb.Tombstone(ctx, "olddead")
	rsql.Close()
	if err := os.MkdirAll(filepath.Join(home, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := Convert(home, cfg); err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// The one database, identity stamped.
	if _, err := pluginmeta.Verify(config.DBFile(home), nodeID, "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "db")); !os.IsNotExist(err) {
		t.Error("the old db/ must be set aside")
	}
	if _, err := os.Stat(filepath.Join(home, "db.pre-one-node", nodeID, "store.db")); err != nil {
		t.Error("the old files must survive beside, not vanish")
	}
	if _, err := os.Stat(filepath.Join(home, "cache")); !os.IsNotExist(err) {
		t.Error("the source cache is disposable and must go")
	}
	ns, err := store.Open(config.DBFile(home))
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Close()

	// Home kept its ids and content.
	if _, err := ns.GetTile(ctx, exit.ID); err != nil {
		t.Fatalf("home exit well lost: %v", err)
	}
	// The plugin's rows, re-minted under its namespace: the root context
	// keeps its view, the well keeps its framing and child, the text keeps
	// its window, and the retired key stays retired.
	p := ns.Namespace(pid)
	rootGID, err := p.ContextID("root")
	if err != nil {
		t.Fatal(err)
	}
	// The old file stored a root as the origin of the 1x1 synthetic doorway,
	// (2.5, -1); the center the client showed, and the one this shape keeps,
	// is + 0.5.
	if f, ok, _ := p.RootFraming(rootGID); !ok || f.Cx != 3 || f.Cy != -0.5 || f.Zoom != 0.75 {
		t.Fatalf("root framing lost: %+v ok=%v", f, ok)
	}
	// The retired key stayed retired across the conversion: a
	// non-authoritative listing that says nothing, which is the shape a dark
	// source takes, answers the two live rows only.
	if live, lerr := p.Merge(rootGID, nil, false); lerr != nil || len(live) != 2 {
		t.Fatalf("retired key came back live: %+v err=%v", live, lerr)
	}
	tiles, err := p.Merge(rootGID, []store.Entry{{Key: "docs", Kind: "well", Label: "docs", ChildContext: "root/docs"}, {Key: "notes.md", Kind: "text", Label: "notes.md"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiles) != 2 {
		t.Fatalf("plugin root = %+v, want docs + notes", tiles)
	}
	var docs, notes store.ExtTile
	for _, tl := range tiles {
		switch tl.Key {
		case "docs":
			docs = tl
		case "notes.md":
			notes = tl
		}
	}
	docsGID, _ := p.ContextID("root/docs")
	// view_x 7 on a 2x2 footprint is the center 8, the same arithmetic the
	// v11 migration applies to a home file.
	if docs.X != 4 || docs.W != 2 || docs.ViewCx != 8 || docs.ViewCy != 9 || docs.ViewZoom != 1.5 || docs.ChildGridID != docsGID {
		t.Fatalf("well framing/child lost: %+v (child %d)", docs, docsGID)
	}
	if notes.TextY != 120 || notes.TextMode != "rendered" || notes.ContentZoom != 1.25 {
		t.Fatalf("text window lost: %+v", notes)
	}
	// Every reference into the plugin points at the re-minted rows.
	e, _ := ns.GetTile(ctx, exit.ID)
	if e.ChildGridID != pid+"/"+strconv.FormatInt(docsGID, 10) {
		t.Fatalf("exit well child = %q, want the re-minted docs grid %d", e.ChildGridID, docsGID)
	}
	l, _ := ns.GetTile(ctx, link.ID)
	if l.LinkTargetID != pid+"/"+strconv.FormatInt(notes.ID, 10) {
		t.Fatalf("leaf link target = %q, want the re-minted notes tile %d", l.LinkTargetID, notes.ID)
	}
	blob, _, _, err := ns.ReadContent(ctx, paneTile.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantAnchor := `"anchor":"` + pid + "/" + strconv.FormatInt(rootGID, 10) + `"`
	wantPath := `"path":["` + pid + "/" + strconv.FormatInt(notes.ID, 10) + `"]`
	if !strings.Contains(string(blob), wantAnchor) || !strings.Contains(string(blob), wantPath) {
		t.Fatalf("pane layout not rewritten: %s (want %s and %s)", blob, wantAnchor, wantPath)
	}
	if !strings.Contains(string(blob), `"anchor":"`+nodeID+"/"+root+`"`) {
		t.Fatalf("a home anchor must stay as it was: %s", blob)
	}
	// The connections came along.
	cdb, err := remote.NewDB(ns.SQL())
	if err != nil {
		t.Fatal(err)
	}
	if r, err := cdb.Get(ctx, "geneva"); err != nil || r.RemoteRoot != "rn/1" || r.Deleted {
		t.Fatalf("connection lost: %+v (%v)", r, err)
	}
	if r, err := cdb.Get(ctx, "olddead"); err != nil || !r.Deleted {
		t.Fatalf("retired connection lost: %+v (%v)", r, err)
	}
	// And the node comes up on it, converting nothing twice.
	if err := ensureStore(home, cfg); err != nil {
		t.Fatalf("second boot: %v", err)
	}
}
