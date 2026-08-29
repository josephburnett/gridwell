package pluginhost_test

// The v2 proc stack through a full server: a dead child is probed and
// swept while the survivors keep id and placement, and a retired key
// never re-mints on reads of an unchanged grid. (Until the 2026-08
// cutover these were pinned by crawling against the legacy proc plugin;
// the legacy twin is gone, so the assertions are direct now.)

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/compose"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
	procplugin "github.com/josephburnett/gridwell/plugins/proc/plugin"
)

const procUUID = "procuux"

type nopKiller struct{}

func (nopKiller) Kill(int64, syscall.Signal) error { return nil }

// fakeProc builds a /proc stand-in: pid 100 with children 200 and 300.
func fakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeProc(t, root, 100, 1, "parent")
	writeProc(t, root, 200, 100, "child-a")
	writeProc(t, root, 300, 100, "child-b")
	return root
}

func writeProc(t *testing.T, root string, pid, ppid int64, name string) {
	t.Helper()
	dir := filepath.Join(root, strconv.FormatInt(pid, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "Name:\t" + name + "\nPid:\t" + strconv.FormatInt(pid, 10) +
		"\nPPid:\t" + strconv.FormatInt(ppid, 10) + "\nState:\tS (sleeping)\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
}

func pluginProcNode(t *testing.T, procRoot string) *rpc.Client {
	t.Helper()
	return pluginProcNodeAt(t, procRoot, filepath.Join(t.TempDir(), "mem.db"))
}

func pluginProcNodeAt(t *testing.T, procRoot, memPath string) *rpc.Client {
	t.Helper()
	memStore, err := store.Open(memPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, cpCloser, err := compose.PluginInProcess(procplugin.New(procRoot, 100, nopKiller{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpCloser)
	client, closer, err := plugin.ServeInProcess(pluginhost.New(cp, memStore.Namespace("p1")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register(procUUID, "proc", client, nil)
	srv := servertest.New(t, reg, server.Config{})
	hs := servertest.Serve(t, srv)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
}

// TestProcPluginSweepAndPlacement: place a child, kill another; the
// next read sweeps only the dead one and the placement persists.
func TestProcPluginSweepAndPlacement(t *testing.T) {
	procRoot := fakeProc(t)
	v2 := pluginProcNode(t, procRoot)
	ctx := context.Background()
	pl, err := v2.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	g, err := v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	var child rpc.Tile
	for _, tile := range g.Tiles {
		if tile.AltText == "300" {
			child = tile
		}
	}
	if child.ID == "" {
		t.Fatal("child 300 not found")
	}
	if _, err := v2.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: child.ID, Version: child.Version, GridID: rootGrid, X: 5, Y: 5, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// child-a (pid 200) dies: the next read probes and sweeps it.
	if err := os.RemoveAll(filepath.Join(procRoot, "200")); err != nil {
		t.Fatal(err)
	}
	g, err = v2.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	var saw300 bool
	for _, tile := range g.Tiles {
		if tile.AltText == "200" {
			t.Fatal("dead child still listed")
		}
		if tile.AltText == "300" {
			saw300 = true
			if tile.ID != child.ID || tile.X != 5 || tile.Y != 5 {
				t.Fatalf("survivor drifted: %+v", tile)
			}
		}
	}
	if !saw300 {
		t.Fatal("living child swept")
	}
}

// TestRetiredKeyStaysRetiredWithoutIdBurn pins the "a retired key stays
// retired" tenet against the CACHE: a non-authoritative listing's cached
// union used to keep a swept key forever, so every later read re-minted a
// fresh id for it and immediately re-retired it — idmap/layout grew without
// bound and the AUTOINCREMENT sequence (the identity fact the v2
// converter existed to protect) advanced on every read of an unchanged
// grid. Reading never mutates.
func TestRetiredKeyStaysRetiredWithoutIdBurn(t *testing.T) {
	procRoot := fakeProc(t)
	memPath := filepath.Join(t.TempDir(), "mem.db")
	v2 := pluginProcNodeAt(t, procRoot, memPath)
	ctx := context.Background()

	pl, err := v2.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := pl.Plugins[0].RootGridID
	if _, err := v2.GetGrid(ctx, root); err != nil {
		t.Fatal(err) // pass 1: mint the live rows
	}
	if err := os.RemoveAll(filepath.Join(procRoot, "200")); err != nil {
		t.Fatal(err)
	}
	if _, err := v2.GetGrid(ctx, root); err != nil {
		t.Fatal(err) // pass 2: probe + sweep pid 200
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
