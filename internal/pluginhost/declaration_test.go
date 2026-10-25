package pluginhost

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// countingPlugin counts its Info calls and refuses while refusing is set.
type countingPlugin struct {
	oneEntryPlugin
	asked    atomic.Int32
	refusing atomic.Bool
}

func (p *countingPlugin) Info(ctx context.Context, req *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	p.asked.Add(1)
	if p.refusing.Load() {
		return nil, status.Error(codes.FailedPrecondition, "no source")
	}
	return p.oneEntryPlugin.Info(ctx, req)
}

// Every reader of the declaration reads the one answer held for the process,
// a refusal as much as a grid, and a process that comes up is asked afresh.
func TestTheDeclarationIsAskedOncePerProcess(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	p := &countingPlugin{}
	p.refusing.Store(true)
	cp, closer, err := plugintest.Loopback(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	sup := &fakeSup{healthy: true, fns: map[int]func(bool, string){}}
	a := New(cp, memStore.Namespace("p1"), sup)
	ctx := context.Background()

	for range 3 {
		if _, err := a.Info(ctx, &gridwellv1.InfoRequest{}); status.Convert(err).Message() != "no source" {
			t.Fatalf("Info = %v, want the held refusal", err)
		}
	}
	if _, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: rpc.EntryGridID("all")}); err == nil {
		t.Fatal("a grid read past a refused declaration")
	}
	if n := p.asked.Load(); n != 1 {
		t.Fatalf("the plugin was asked Info %d times, want once for the process", n)
	}

	p.refusing.Store(false)
	sup.set(false)
	sup.set(true)
	for range 3 {
		info, err := a.Info(ctx, &gridwellv1.InfoRequest{})
		if err != nil || len(info.PluginGrids) != 1 {
			t.Fatalf("after a respawn Info = %v, %v; want the new process's one grid", info, err)
		}
	}
	if _, err := a.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: rpc.EntryGridID("all")}); err != nil {
		t.Fatal(err)
	}
	if n := p.asked.Load(); n != 2 {
		t.Fatalf("the plugin was asked Info %d times, want once more for the new process", n)
	}
}

// presentingPlugin declares the site addresses it presents and no grids.
type presentingPlugin struct{ oneEntryPlugin }

func (presentingPlugin) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "presenter", Presents: []string{"https://example.test/*"}}, nil
}

// The addresses a plugin presents ride its held declaration to the node,
// with no grid to carry them.
func TestInfoCarriesWhatAPluginPresents(t *testing.T) {
	memStore, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	cp, closer, err := plugintest.Loopback(presentingPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	info, err := New(cp, memStore.Namespace("p1"), nil).Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.GetPresents(); len(got) != 1 || got[0] != "https://example.test/*" || len(info.GetPluginGrids()) != 0 {
		t.Errorf("Info = presents %q, %d grids; want the declared pattern and none", got, len(info.GetPluginGrids()))
	}
}
