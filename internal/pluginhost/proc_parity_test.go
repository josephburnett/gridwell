package pluginhost_test

// The proc stack through a full server: a dead child is probed and swept
// while the survivors keep their id and placement, and a retired key never
// re-mints on reads of an unchanged grid.
//
// The plugin is the REAL gridwell-plugin-proc binary over the REAL /proc,
// rooted at this test process: a plugin lives in another repository now, so
// there is no in-process stand-in to point at a fake tree, and there is no
// need for one — the test owns real children, and killing one is exactly the
// disappearance the sweep arbitrates.

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

const procUUID = "procuux"

// sleeper starts a real child of this test process and returns its pid and a
// reaper. The reaper kills AND waits: an unreaped child stays in /proc as a
// zombie, so the plugin would still see it and the sweep would be right not
// to remove it.
func sleeper(t *testing.T) (pid string, reap func()) {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a child process: %v", err)
	}
	done := false
	reap = func() {
		if done {
			return
		}
		done = true
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	t.Cleanup(reap)
	return strconv.Itoa(cmd.Process.Pid), reap
}

func pluginProcNode(t *testing.T) *rpc.Client {
	t.Helper()
	return pluginProcNodeAt(t, filepath.Join(t.TempDir(), "mem.db"))
}

// pluginProcNodeAt stands the shipped proc binary up over an existing store
// path, rooted at this test process, configured exactly as a server.yaml
// plugins: entry would be.
func pluginProcNodeAt(t *testing.T, memPath string) *rpc.Client {
	t.Helper()
	memStore, err := store.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp := plugintest.Spawn(t, "proc", map[string]string{"pid": strconv.Itoa(os.Getpid())})
	client := pluginhost.New(cp, memStore.Namespace("p1"))
	reg := plugin.NewRegistry()
	reg.Register(procUUID, "proc", client, nil)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
}

// tileNamed returns the tile whose label is the given pid, or the zero tile.
func tileNamed(tiles []rpc.Tile, label string) rpc.Tile {
	for _, tile := range tiles {
		if tile.AltText == label {
			return tile
		}
	}
	return rpc.Tile{}
}

// TestProcPluginSweepAndPlacement: place a child, kill another; the
// next read sweeps only the dead one and the placement persists.
func TestProcPluginSweepAndPlacement(t *testing.T) {
	dying, reapDying := sleeper(t)
	surviving, _ := sleeper(t)
	v2 := pluginProcNode(t)
	ctx := context.Background()
	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	g, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	child := tileNamed(g.Tiles, surviving)
	if child.ID == "" {
		t.Fatalf("child %s not found among %v", surviving, g.Tiles)
	}
	if tileNamed(g.Tiles, dying).ID == "" {
		t.Fatalf("child %s not found among %v", dying, g.Tiles)
	}
	if _, err := v2.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: child.ID, GridID: rootGrid, X: 5, Y: 5, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// The other child dies: the next read probes and sweeps it.
	reapDying()
	g, err = v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if tileNamed(g.Tiles, dying).ID != "" {
		t.Fatal("dead child still listed")
	}
	back := tileNamed(g.Tiles, surviving)
	if back.ID == "" {
		t.Fatal("living child swept")
	}
	if back.ID != child.ID || back.X != 5 || back.Y != 5 {
		t.Fatalf("survivor drifted: %+v", back)
	}
}

// TestRetiredKeyStaysRetiredWithoutIdBurn pins "a retired key stays retired"
// against the cache. If a non-authoritative listing's cached union kept a
// swept key, every later read would re-mint a fresh id for it and immediately
// re-retire it: the rows would grow without bound and the AUTOINCREMENT
// sequence would advance on every read of an unchanged grid. Reading never
// mutates.
func TestRetiredKeyStaysRetiredWithoutIdBurn(t *testing.T) {
	_, reap := sleeper(t)
	memPath := filepath.Join(t.TempDir(), "mem.db")
	v2 := pluginProcNodeAt(t, memPath)
	ctx := context.Background()

	pl, err := v2.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := pl.Plugins[0].RootGridID
	if _, err := v2.GetGrid(ctx, root); err != nil {
		t.Fatal(err) // pass 1: mint the live rows
	}
	reap()
	if _, err := v2.GetGrid(ctx, root); err != nil {
		t.Fatal(err) // pass 2: probe + sweep the dead child
	}

	count := func() int {
		t.Helper()
		db, err := sql.Open("sqlite", memPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM tiles WHERE ns = 'p1'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	for i := 0; i < 3; i++ {
		if _, err := v2.GetGrid(ctx, root); err != nil {
			t.Fatal(err)
		}
	}
	if after := count(); after != before {
		t.Fatalf("idmap grew %d → %d across reads of an UNCHANGED grid: the cached union resurrects the retired key and every read mints-and-retires a fresh id", before, after)
	}
}
