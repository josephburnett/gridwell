package plugin_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/compose"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/pluginmeta"
	_ "modernc.org/sqlite" // register the sqlite driver for pluginmeta.Ensure
)

// buildPluginBinary compiles the test-only guest binary
// (internal/plugin/guesttest) into a temp file and returns its path. A
// build failure FAILS the test: the old skip-on-failure masked the
// build path going stale for weeks — these tests silently skipped on
// every run while the subprocess transport went unexercised.
func buildPluginBinary(t *testing.T, kind string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "gridwell-"+kind)
	cmd := exec.Command("go", "build", "-o", out, "github.com/josephburnett/gridwell/internal/plugin/guesttest")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build guesttest: %v\n%s", err, b)
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
	bin := buildPluginBinary(t, "local")
	dbPath := filepath.Join(t.TempDir(), "test.gwdb")

	// The plugin verifies (never creates) its identity, so the DB must be
	// initialized first — exactly what `gridwell init` does.
	if err := pluginmeta.Create(dbPath, "sub-uuid-1", "local"); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	client, closer, err := compose.LoadPlugin(bin, map[string]string{
		"db_file": dbPath,
		"uuid":    "sub-uuid-1",
		"kind":    "local",
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
	if info.Kind != "local" || info.RootGridId == "" {
		t.Fatalf("Info = %+v, want kind=localdb and a root grid id", info)
	}

	created, err := client.CreateTile(ctx, &gridwellv1.CreateTileRequest{
		GridId: info.RootGridId,
		Tile:   &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2},
	})
	if err != nil {
		t.Fatalf("CreateTile: %v", err)
	}
	if got := writeThenRead(t, ctx, client, created.Tile.Id, created.Tile.Version, []byte("# over the wire")); string(got) != "# over the wire" {
		t.Errorf("content = %q, want %q", got, "# over the wire")
	}

	// The plugin's durable identity (id + kind) is intact in its own DB.
	stored, err := pluginmeta.Verify(dbPath, "", "")
	if err != nil {
		t.Fatalf("pluginmeta.Verify(read): %v", err)
	}
	if stored.ID != "sub-uuid-1" || stored.Kind != "local" {
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
	bin := buildPluginBinary(t, "local")
	dbPath := filepath.Join(t.TempDir(), "test.gwdb")

	// Seed the DB's durable identity directly.
	if err := pluginmeta.Create(dbPath, "id-A", "local"); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	// Spawning the plugin against that DB with a different id must fail.
	client, closer, err := compose.LoadPlugin(bin, map[string]string{
		"db_file": dbPath,
		"uuid":    "id-B",
		"kind":    "local",
	})
	if err == nil {
		closer()
		_ = client
		t.Fatal("LoadPlugin should fail when the configured id diverges from the DB")
	}
}

// writeThenRead drives the contracted content surface: WriteContent commits
// the bytes (version claimed from the created row), ReadContent streams them
// back. The test helper for what CreateTile.data used to carry.
func writeThenRead(t *testing.T, ctx context.Context, c gridwellv1.GridwellClient, tileID string, version int64, data []byte) []byte {
	t.Helper()
	w, err := c.WriteContent(ctx)
	if err != nil {
		t.Fatalf("WriteContent open: %v", err)
	}
	if err := w.Send(&gridwellv1.WriteContentRequest{TileId: tileID, Version: version, Data: data}); err != nil {
		t.Fatalf("WriteContent send: %v", err)
	}
	if _, err := w.CloseAndRecv(); err != nil {
		t.Fatalf("WriteContent close: %v", err)
	}
	r, err := c.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: tileID})
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	var out []byte
	for {
		chunk, err := r.Recv()
		if err != nil {
			break
		}
		out = append(out, chunk.Data...)
	}
	return out
}
