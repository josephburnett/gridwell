package plugin_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/pluginmeta"
	_ "modernc.org/sqlite" // register the sqlite driver for pluginmeta.Ensure
)

// buildPluginBinary compiles cmd/plugin/<kind> into a temp file and returns its
// path. Skips the test on build failure (e.g. a constrained CI without a Go
// toolchain) rather than failing spuriously.
func buildPluginBinary(t *testing.T, kind string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "gridwell-"+kind)
	cmd := exec.Command("go", "build", "-o", out, "github.com/josephburnett/gridwell/cmd/plugin/"+kind)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("go build %s: %v\n%s", kind, err, b)
	}
	return out
}

// TestSubprocessPlugin_LocalDB exercises the real production transport: a
// separately-compiled go-plugin binary, spawned by the host, configured through
// the environment (no Attach), reporting its root via Info, and round-tripping
// a CreateTile through gRPC. It also confirms the uuid was persisted into the
// plugin's own DB (pluginmeta).
func TestSubprocessPlugin_LocalDB(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary; skipped under -short")
	}
	bin := buildPluginBinary(t, "localdb")
	dbPath := filepath.Join(t.TempDir(), "test.gwdb")

	client, closer, err := plugin.LoadPlugin(bin, map[string]string{
		"db_file": dbPath,
		"uuid":    "sub-uuid-1",
		"kind":    "localdb",
	})
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	defer closer()

	ctx := context.Background()
	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Kind != "localdb" || info.RootGridId == "" {
		t.Fatalf("Info = %+v, want kind=localdb and a root grid id", info)
	}

	created, err := client.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: info.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2},
		Data:   []byte("# over the wire"),
	})
	if err != nil {
		t.Fatalf("CreateTile: %v", err)
	}
	body, err := client.GetTileContent(ctx, &gridwellv1.GetTileContentRequest{TileId: created.Tile.Id})
	if err != nil {
		t.Fatalf("GetTileContent: %v", err)
	}
	if string(body.Data) != "# over the wire" {
		t.Errorf("content = %q, want %q", body.Data, "# over the wire")
	}

	// The plugin persisted its durable identity (id + kind) into its own DB.
	stored, err := pluginmeta.Ensure(dbPath, "", "")
	if err != nil {
		t.Fatalf("pluginmeta.Ensure(read): %v", err)
	}
	if stored.ID != "sub-uuid-1" || stored.Kind != "localdb" {
		t.Errorf("persisted identity = %+v, want {sub-uuid-1 localdb}", stored)
	}
}

// TestSubprocessPlugin_IDMismatchRejected proves the strict identity check
// fires across a restart: a DB that already carries one id refuses to be opened
// by a plugin configured with a different id. The plugin exits non-zero during
// startup, so LoadPlugin (the handshake) fails.
func TestSubprocessPlugin_IDMismatchRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary; skipped under -short")
	}
	bin := buildPluginBinary(t, "localdb")
	dbPath := filepath.Join(t.TempDir(), "test.gwdb")

	// Seed the DB's durable identity directly.
	if _, err := pluginmeta.Ensure(dbPath, "id-A", "localdb"); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	// Spawning the plugin against that DB with a different id must fail.
	client, closer, err := plugin.LoadPlugin(bin, map[string]string{
		"db_file": dbPath,
		"uuid":    "id-B",
		"kind":    "localdb",
	})
	if err == nil {
		closer()
		_ = client
		t.Fatal("LoadPlugin should fail when the configured id diverges from the DB")
	}
}
