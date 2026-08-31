package node

// The upgrade seam: a GENUINE pre-one-node home — the old server.yaml AND the
// old db/<home-id>/ layout — through the real boot entry, BuildConfig then
// Start. Both halves of "a pre-one-node home converts itself at first serve"
// are on this path and neither was crossed by a test before: the dev box's two
// live homes were hand-migrated during the fold, so no test ever fed a real
// pre-one-node server.yaml through serve. config.Load's strict decode refused
// the file at the first line, and node.Convert — unit-tested on its own, in
// convert_test.go, with a config value built in Go — was never reached.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// The ids are the real shapes a pre-one-node home carries: 32-hex, minted
// before the short-id decision, and immutable forever.
const (
	legacyNodeID = "8aed3340244e2053890889c4759cd373" // the deleted launcher grid's id
	legacyHomeID = "52f8374fa356402c66e41b8097341b09" // the HOME row: this is the node's id
	legacyFSID   = "fa21d5d19ab177018f1f11c7357d6ffc"
)

func TestAPreOneNodeHomeConvertsAndBoots(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bin := plugintest.Binary(t, "fs")

	// What the fs plugin will list once the node is up.
	fsRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(fsRoot, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fsRoot, "notes.md"), []byte("# notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. The old config, exactly as the fold left it on a real machine:
	// node_id, every namespace under plugins:, name:, provider:.
	cfgPath := filepath.Join(home, "server.yaml")
	legacyYAML := "node_id: " + legacyNodeID + `
plugins:
    - id: ` + legacyHomeID + `
      name: ""
      kind: home
    - id: ` + legacyFSID + `
      name: files
      kind: fs
      binary: ` + bin + `
      config:
        root: ` + fsRoot + `
      provider: true
retired_names:
    - eoifgyl
`
	if err := os.WriteFile(cfgPath, []byte(legacyYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. The old home store, with a tile of each shape that carries a
	// reference into the plugin.
	oldDir := config.LegacyDBDir(home, legacyHomeID)
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	homeDB := filepath.Join(oldDir, "store.db")
	if err := pluginmeta.Create(homeDB, legacyHomeID, "home"); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(homeDB)
	if err != nil {
		t.Fatal(err)
	}
	root, err := st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const noteBody = "# the note the user left\n\nit must come back byte for byte\n"
	text, err := st.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 3, Y: -2, W: 2, H: 2, Data: []byte(noteBody)})
	if err != nil {
		t.Fatal(err)
	}
	exit, err := st.CreateExitWell(ctx, root, 0, 0, 1, 1, legacyFSID+"/2", "docs", rpc.Framing{})
	if err != nil {
		t.Fatal(err)
	}
	link, err := st.CreateLeafLink(ctx, root, 1, 0, 1, 1, "text", legacyFSID+"/3", "notes")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	// 3. The old plugin memory: two contexts, three keys, a cached listing.
	memDir := config.LegacyDBDir(home, legacyFSID)
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
	must(`INSERT INTO contexts (grid_id, key, root_cx, root_cy, root_zoom) VALUES (1, 'root', 0, 0, 1), (2, 'root/docs', NULL, NULL, NULL)`)
	must(`INSERT INTO idmap (tile_id, grid_id, key, tombstoned) VALUES (1, 1, 'docs', 0), (2, 1, 'gone.md', 1), (3, 1, 'notes.md', 0)`)
	must(`INSERT INTO layout (tile_id, x, y, w, h, view_x, view_y, view_zoom, text_y, text_mode, content_zoom) VALUES (1, 4, 4, 1, 1, 0, 0, 1, 0, '', 0), (2, 0, 0, 1, 1, 0, 0, 1, 0, '', 0), (3, 5, 5, 1, 1, 0, 0, 1, 0, '', 0)`)
	listing, err := proto.Marshal(&pluginv1.ListResponse{Authoritative: true, Entries: []*pluginv1.Entry{
		{Key: "docs", Kind: "well", Label: "docs", ChildContext: "root/docs"},
		{Key: "notes.md", Kind: "text", Label: "notes.md"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	must(`INSERT INTO cache_listings (grid_id, entries, authoritative) VALUES (1, ?, 1)`, listing)
	mem.Close()

	// 4. The real boot entry. Before the config converter this failed here,
	// on "field node_id not found in type config.ServerConfig", and the
	// database converter below it never ran.
	cfg, err := BuildConfig(home, cfgPath)
	if err != nil {
		t.Fatalf("BuildConfig on a pre-one-node home: %v", err)
	}
	if cfg.ID != legacyHomeID {
		t.Fatalf("id = %q, want the home row's id %q (node_id named the deleted launcher grid)", cfg.ID, legacyHomeID)
	}
	if len(cfg.Plugins) != 1 || cfg.Plugins[0].ID != legacyFSID || cfg.Plugins[0].Label != "files" {
		t.Fatalf("plugins = %+v, want the fs row alone, name become label", cfg.Plugins)
	}
	if _, err := os.Stat(cfgPath + ".pre-one-node"); err != nil {
		t.Errorf("the original config must be set aside: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "db.pre-one-node", legacyHomeID, "store.db")); err != nil {
		t.Errorf("the old db/ must be set aside: %v", err)
	}
	if _, err := os.Stat(config.DBFile(home)); err != nil {
		t.Errorf("the one database must exist: %v", err)
	}

	// 5. The node comes up on the converted home, plugin subprocess and all.
	cfg.Web.Bind = "127.0.0.1:0"
	n, err := Start(Options{Home: home, Cfg: cfg})
	if err != nil {
		t.Fatalf("Start on the converted home: %v", err)
	}
	defer n.Close()
	n.ServeBackground()

	ns, ok := n.Reg.Get(cfg.ID)
	if !ok {
		t.Fatal("home is not registered under the converted id")
	}
	grid, err := ns.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatalf("read the home root back: %v", err)
	}
	byID := map[string]*gridwellv1.Tile{}
	for _, tl := range grid.Tiles {
		byID[tl.Id] = tl
	}
	if len(byID) != 3 {
		t.Fatalf("home root has %d tiles, want the 3 the user left", len(byID))
	}
	// Home keeps every id and every placement: nothing the user did not touch
	// moved.
	tx := byID[text.ID]
	if tx == nil || tx.Kind != "text" || tx.X != 3 || tx.Y != -2 || tx.W != 2 || tx.H != 2 {
		t.Fatalf("the text tile did not survive: %+v", tx)
	}
	var body []byte
	if err := ns.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: text.ID},
		func(c *gridwellv1.ContentChunk) error { body = append(body, c.Data...); return nil }); err != nil {
		t.Fatalf("read the note back: %v", err)
	}
	if string(body) != noteBody {
		t.Fatalf("the note read back as %q, want it byte for byte", body)
	}
	// The references into the plugin resolve to the re-minted rows: read
	// through the RUNNING node's own store handle, not a second open.
	p := n.st.Namespace(legacyFSID)
	rootGID, err := p.ContextID("root")
	if err != nil {
		t.Fatal(err)
	}
	docsGID, err := p.ContextID("root/docs")
	if err != nil {
		t.Fatal(err)
	}
	e, err := n.st.GetTile(ctx, exit.ID)
	if err != nil || e.ChildGridID != legacyFSID+"/"+strconv.FormatInt(docsGID, 10) {
		t.Fatalf("exit well child = %+v (%v), want the re-minted docs grid %d", e, err, docsGID)
	}
	tiles, err := p.Merge(rootGID, []store.Entry{
		{Key: "docs", Kind: "well", Label: "docs", ChildContext: "root/docs"},
		{Key: "notes.md", Kind: "text", Label: "notes.md"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	var notesID int64
	for _, tl := range tiles {
		if tl.Key == "notes.md" {
			notesID = tl.ID
		}
	}
	l, err := n.st.GetTile(ctx, link.ID)
	if err != nil || l.LinkTargetID != legacyFSID+"/"+strconv.FormatInt(notesID, 10) {
		t.Fatalf("leaf link target = %+v (%v), want the re-minted notes tile %d", l, err, notesID)
	}
}
