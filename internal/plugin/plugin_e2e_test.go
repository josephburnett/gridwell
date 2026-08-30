package plugin_test

// The plugin spawn path, end to end: a separately-compiled
// gridwell-plugin-fs binary spawned through go-plugin, serving plugin.v1. The
// loader opens the node-owned store, wraps the adapter, and the registry
// client sees an ordinary Gridwell namespace. Placement persists in the node's
// file and the plugin process holds no state at all.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

func TestSubprocessPlugin_FS(t *testing.T) {
	bin := plugintest.Binary(t, "fs")

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "gridwell.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{
		ID: "pfsuuid", Label: "files", Kind: "fs", Binary: bin,
		Config: map[string]string{"root": root},
	}}}
	reg := plugin.NewRegistry()
	if err := plugin.LoadInto(reg, cfg, st, nil); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	defer reg.Close()

	client, ok := reg.Get("pfsuuid")
	if !ok {
		t.Fatal("plugin not registered")
	}
	ctx := context.Background()
	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info through the adapter: %v", err)
	}
	if info.Kind != "fs" || info.RootGridId == "" {
		t.Fatalf("bad Info: %+v", info)
	}
	g, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	if len(g.Tiles) != 2 {
		t.Fatalf("want 2 tiles, got %+v", g.Tiles)
	}
	var hello *gridwellv1.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "hello.md" {
			hello = tile
		}
	}
	if hello == nil {
		t.Fatal("hello.md not projected")
	}
	if _, err := client.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: hello.Id, GridId: info.RootGridId, X: 4, Y: 1, W: 2, H: 2,
	}); err != nil {
		t.Fatalf("PlaceTile: %v", err)
	}
	got, err := client.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: hello.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Tile.X != 4 || got.Tile.W != 2 {
		t.Fatalf("placement not persisted through the subprocess seam: %+v", got.Tile)
	}
	// The plugin's memory is the node's one database, and the plugin process
	// wrote nothing anywhere: its config carries no db path at all.
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("node database missing: %v", err)
	}
}

// TestLoadIntoFailsOnARefusedHandshake crosses the whole refusal path in the
// one shape that ships: a real gridwell-plugin-proc spawned with a pid the
// plugin's FromConfig refuses. guest.Main serves the refusal as an Info that
// answers FailedPrecondition, and LoadInto must stop the launch carrying that
// reason and naming the plugin — never come up as an empty grid. The two
// tests this replaces staged the refusal through the deleted in-process
// factory door, so neither ever spawned anything.
func TestLoadIntoFailsOnARefusedHandshake(t *testing.T) {
	bin := plugintest.Binary(t, "proc")

	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{
		ID: "pr1234a", Label: "procs", Kind: "proc", Binary: bin,
		Config: map[string]string{"pid": "abc"},
	}}}
	err = plugin.LoadInto(plugin.NewRegistry(), cfg, st, nil)
	if err == nil || !strings.Contains(err.Error(), `pid "abc"`) || !strings.Contains(err.Error(), "pr1234a") {
		t.Fatalf("LoadInto = %v, want the plugin's own reason, naming it", err)
	}
}
