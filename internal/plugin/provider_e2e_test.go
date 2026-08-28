package plugin_test

// The v2 provider spawn path, end to end (docs/v2-design.md §4): a
// separately-compiled gridwell-plugin-fs binary spawned through
// go-plugin, serving plugin.v1; the loader opens the NODE-owned
// memory DB, wraps the adapter, and the registry client sees an ordinary
// Gridwell plugin — placement persists in the node's file, and the
// provider process holds no state at all.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// buildProviderBinary compiles a shipped provider binary into a temp
// file. A build failure FAILS the test (never skips): a skip-on-failure
// once masked a stale build path for weeks while the subprocess
// transport went unexercised.
func buildProviderBinary(t *testing.T, kind string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "gridwell-plugin-"+kind)
	cmd := exec.Command("go", "build", "-o", out,
		"github.com/josephburnett/gridwell/plugins/"+kind+"/cmd/gridwell-plugin-"+kind)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build provider %s: %v\n%s", kind, err, b)
	}
	return out
}

func TestSubprocessProvider_FS(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a provider binary; skipped under -short")
	}
	bin := buildProviderBinary(t, "fs")

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	memPath := filepath.Join(t.TempDir(), "mem.db")

	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{
		ID: "pfsuuid", Name: "files", Kind: "fs", Binary: bin,
		Config: map[string]string{"root": root, "db_file": memPath},
	}}}
	reg, err := plugin.LoadAll(cfg, nil, nil)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	defer reg.Close()

	client, ok := reg.Get("pfsuuid")
	if !ok {
		t.Fatal("provider not registered")
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
	// The memory DB is the node's file; the provider process wrote
	// nothing anywhere (its config carries no db path at all).
	if _, err := os.Stat(memPath); err != nil {
		t.Fatalf("node memory DB missing: %v", err)
	}
}
