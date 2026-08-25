package providerhost_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/josephburnett/gridwell/api/compose"
	cpv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/layout"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/providerhost"
	"github.com/josephburnett/gridwell/internal/server"
	fsprovider "github.com/josephburnett/gridwell/plugins/fs/provider"
)

// darkableCP forwards to a live provider until dark is set — then every
// unary call answers Unavailable, exactly what a crashed provider
// SUBPROCESS looks like to the adapter (distinct from the dark-SOURCE
// case, where the process answers and only the directory read fails).
type darkableCP struct {
	cpv1.ContentProviderClient
	dark atomic.Bool
}

func (d *darkableCP) Info(ctx context.Context, req *cpv1.InfoRequest, opts ...grpc.CallOption) (*cpv1.InfoResponse, error) {
	if d.dark.Load() {
		return nil, status.Error(codes.Unavailable, "provider process dark")
	}
	return d.ContentProviderClient.Info(ctx, req, opts...)
}

func (d *darkableCP) List(ctx context.Context, req *cpv1.ListRequest, opts ...grpc.CallOption) (*cpv1.ListResponse, error) {
	if d.dark.Load() {
		return nil, status.Error(codes.Unavailable, "provider process dark")
	}
	return d.ContentProviderClient.List(ctx, req, opts...)
}

func (d *darkableCP) Probe(ctx context.Context, req *cpv1.ProbeRequest, opts ...grpc.CallOption) (*cpv1.ProbeResponse, error) {
	if d.dark.Load() {
		return nil, status.Error(codes.Unavailable, "provider process dark")
	}
	return d.ContentProviderClient.Probe(ctx, req, opts...)
}

// A crashed provider process must degrade exactly like a dark source:
// the remembered listing, stamped stale. Before the fix, grid() served
// the cache from List but then failed the whole read on the trailing
// Info call — the tenet-6 promise never fired for the process-dark case.
func TestProviderProcessDarkServesRememberedListing(t *testing.T) {
	root := seedTree(t)
	mem, err := layout.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	cp, cpCloser, err := compose.ServeProviderInProcess(fsprovider.New(root, osRemoveHost{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cpCloser)
	dc := &darkableCP{ContentProviderClient: cp}
	client, closer, err := plugin.ServeInProcess(providerhost.New(dc, mem))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register(fsUUID, "fs", client, nil)
	srv := server.New(reg, server.Config{})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx := context.Background()
	pl, err := cl.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID
	before, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Tiles) == 0 {
		t.Fatal("empty first read")
	}

	dc.dark.Store(true)
	after, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatalf("process-dark provider surfaced as an error instead of the remembered answer: %v", err)
	}
	if !after.Grid.Stale {
		t.Fatal("remembered answer not stamped stale")
	}
	if len(after.Tiles) != len(before.Tiles) {
		t.Fatalf("dark process changed the tile set: %d != %d", len(after.Tiles), len(before.Tiles))
	}
	if after.Grid.SourceKind != before.Grid.SourceKind {
		t.Fatalf("source kind drifted dark: %q != %q", after.Grid.SourceKind, before.Grid.SourceKind)
	}

	dc.dark.Store(false)
	healed, err := cl.GetGrid(ctx, rootGrid)
	if err != nil {
		t.Fatal(err)
	}
	if healed.Grid.Stale {
		t.Fatal("healed provider still stamped stale")
	}
}
