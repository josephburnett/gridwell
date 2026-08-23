package providerhost_test

// The proc twin of the fs parity gate: the SAME fake process tree served
// by the legacy proc plugin and by the v2 stack must crawl to zero
// differences — including the non-authoritative sweep (a dead child is
// probed and swept; an @info tile is never swept; an unreadable pass
// sweeps nothing).

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/compose"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/layout"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/providerhost"
	"github.com/josephburnett/gridwell/internal/server"
	procplugin "github.com/josephburnett/gridwell/plugins/proc"
	procprovider "github.com/josephburnett/gridwell/plugins/proc/provider"
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

func legacyProcNode(t *testing.T, procRoot string) *rpc.Client {
	t.Helper()
	p, err := procplugin.Open(":memory:", procRoot, nopKiller{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.SetRootPID(100)
	client, closer, err := plugin.ServeInProcess(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register(procUUID, "proc", client, nil)
	srv := server.New(reg, server.Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
}

func providerProcNode(t *testing.T, procRoot string) *rpc.Client {
	t.Helper()
	mem, err := layout.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	cp, cpCloser, err := compose.ServeProviderInProcess(procprovider.New(procRoot, 100, nopKiller{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpCloser)
	client, closer, err := plugin.ServeInProcess(providerhost.New(cp, mem))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register(procUUID, "proc", client, nil)
	srv := server.New(reg, server.Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
}

func TestProcProviderMatchesLegacyOnTheWire(t *testing.T) {
	procRoot := fakeProc(t)
	legacy := legacyProcNode(t, procRoot)
	v2 := providerProcNode(t, procRoot)
	mustParity(t, legacy, v2)
}

func TestProcProviderSweepParity(t *testing.T) {
	procRoot := fakeProc(t)
	legacy := legacyProcNode(t, procRoot)
	v2 := providerProcNode(t, procRoot)
	mustParity(t, legacy, v2)

	// child-a (pid 200) dies: the next crawl probes and sweeps its well
	// on both sides — and its grid's @info tile survives on both (the
	// never-sweep-@info rule).
	if err := os.RemoveAll(filepath.Join(procRoot, "200")); err != nil {
		t.Fatal(err)
	}
	mustParity(t, legacy, v2)

	// The tile is really gone from both, not just still-matching.
	ctx := context.Background()
	pl, err := legacy.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g, err := legacy.GetGrid(ctx, pl.Plugins[0].RootGridID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range g.Tiles {
		if tile.AltText == "200" {
			t.Fatal("dead child still listed")
		}
	}
}

func TestProcProviderPlacementParity(t *testing.T) {
	procRoot := fakeProc(t)
	legacy := legacyProcNode(t, procRoot)
	v2 := providerProcNode(t, procRoot)
	mustParity(t, legacy, v2)

	ctx := context.Background()
	pl, err := legacy.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	g, err := legacy.GetGrid(ctx, rootGrid)
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
	for _, cl := range []*rpc.Client{legacy, v2} {
		if _, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{
			TileID: child.ID, Version: child.Version, GridID: rootGrid, X: 5, Y: 5, W: 1, H: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustParity(t, legacy, v2)
}
