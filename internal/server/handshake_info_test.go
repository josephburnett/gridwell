package server

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/namespace/namespacetest"
)

// infoNode is the node these tests' fakes are the files of.
const infoNode = "lnode1"

// onFiles registers ns as infoNode's files, the second namespace a node
// declares beside home, and serves the node.
func onFiles(t *testing.T, reg *Sources, ns namespace.Namespace) *Server {
	t.Helper()
	reg.Files = ns
	return mustNew(t, reg, Config{ID: infoNode})
}

// countingInfo is a namespace that counts Info calls and can fail the first
// one.
type countingInfo struct {
	namespacetest.Unimplemented
	calls     atomic.Int32
	failFirst bool
}

func (p *countingInfo) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	n := p.calls.Add(1)
	if p.failFirst && n == 1 {
		return nil, errors.New("transient info failure")
	}
	return &pb.InfoResponse{DisplayName: "T", SourceGrids: []*pb.SourceGrid{{Id: "/r", Label: "r", GridId: rpc.KeyTileID("/r")}}}, nil
}

// TestHandshakeRetriesFailedInfo: a failed Info lists a degraded row, and
// the next Handshake asks the namespace again and lists it whole.
func TestHandshakeRetriesFailedInfo(t *testing.T) {
	fake := &countingInfo{failFirst: true}
	h := newConnectHandler(newRouter(onFiles(t, &Sources{}, fake)))

	r1, err := h.Handshake(context.Background(), connect.NewRequest(&pb.HandshakeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	// Degraded entry: listed, with its failure and no door (info was nil).
	if row := r1.Msg.Sources[0]; row.InfoError == "" || len(row.SourceGrids) != 0 {
		t.Errorf("failed Info should list a degraded entry, got %v", row)
	}
	r2, err := h.Handshake(context.Background(), connect.NewRequest(&pb.HandshakeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if row := r2.Msg.Sources[0]; row.InfoError != "" || len(row.SourceGrids) != 1 || row.SourceGrids[0].GridId != rpc.QualifyID(infoNode, rpc.KeyTileID("/r")) {
		t.Errorf("retry after a failed Info should recover, got %v", row)
	}
	if got := fake.calls.Load(); got != 2 {
		t.Errorf("Info called %d times, want 2 (fail, then the answer)", got)
	}
}

// hungInfo answers Info only when its caller gives up.
type hungInfo struct{ namespacetest.Unimplemented }

func (hungInfo) Info(ctx context.Context, _ *pb.InfoRequest) (*pb.InfoResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A namespace whose Info never answers lists broken once pluginInfoTimeout
// runs out, and the rest of the menu is not held behind it.
func TestAHungInfoListsBrokenAfterTheTimeout(t *testing.T) {
	h := newConnectHandler(newRouter(onFiles(t, &Sources{}, hungInfo{})))
	start := time.Now()
	r, err := h.Handshake(context.Background(), connect.NewRequest(&pb.HandshakeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < pluginInfoTimeout || took > pluginInfoTimeout+2*time.Second {
		t.Errorf("Handshake took %v, want it bounded by pluginInfoTimeout (%v)", took, pluginInfoTimeout)
	}
	if r.Msg.Sources[0].InfoError == "" {
		t.Error("a hung Info lists no InfoError")
	}
}

// alwaysFailInfo never succeeds its Info handshake: a namespace persistently
// broken.
type alwaysFailInfo struct {
	namespacetest.Unimplemented
}

func (alwaysFailInfo) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return nil, errors.New("files exploded")
}

// TestHandshakeSurfacesInfoErrorOverTheWire crosses the seam a pure
// buildSourceInfo unit test cannot reach: server -> Connect wire (proto-JSON)
// -> rpc.Client.Handshake. InfoError must survive that round trip so the
// wasm client can classify a broken namespace (client/sourcehealth).
func TestHandshakeSurfacesInfoErrorOverTheWire(t *testing.T) {
	hs := serveWeb(t, onFiles(t, &Sources{}, alwaysFailInfo{}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	plugins, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if len(plugins.Sources) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(plugins.Sources), plugins.Sources)
	}
	p := plugins.Sources[0]
	if p.RootGridId != "" || len(p.SourceGrids) != 0 {
		t.Errorf("broken row = %v, want no door", p)
	}
	if p.InfoError == "" {
		t.Error("a broken row must carry a non-empty InfoError over the wire")
	}
	if !strings.Contains(p.InfoError, "files exploded") {
		t.Errorf("InfoError = %q, want it to mention the underlying failure", p.InfoError)
	}
}

// infoFlake is a real namespace whose Info handshake can be failed at will.
type infoFlake struct {
	namespace.Namespace
	fail atomic.Bool
}

func (p *infoFlake) Info(ctx context.Context, req *pb.InfoRequest) (*pb.InfoResponse, error) {
	if p.fail.Load() {
		return nil, errors.New("namespace not responding")
	}
	return p.Namespace.Info(ctx, req)
}

// TestGetGridFailsWhenOwnerInfoFails crosses the read seam a buildSourceInfo
// unit test cannot reach: server -> Connect wire -> rpc.Client.GetGrid. Grid
// source_grids comes from the owning namespace's Info and scratch_grid_id
// from home's, from nowhere else, so a failed handshake must fail the read. The
// alternative — answering the grid with the fields unset — is a grid with no
// doors and no ephemeral visits, and that flips back on the next read,
// because Info is not negatively cached.
func TestGetGridFailsWhenOwnerInfoFails(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	uuid, err := st.PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bareRoot, err := st.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ns := &infoFlake{Namespace: local.New(st, nil)}
	reg := &Sources{}
	reg.Home = ns
	srv := mustNew(t, reg, Config{ID: uuid})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	root := uuid + "/" + bareRoot
	ns.fail.Store(true)
	resp, err := cl.GetGrid(ctx, root)
	if err == nil {
		g := resp.GetGrid()
		t.Fatalf("GetGrid answered while the owner's Info was failing: writable=%v scratch=%q declared grids=%d",
			g.GetWritable(), g.GetScratchGridId(), len(g.GetSourceGrids()))
	}
	if !strings.Contains(err.Error(), "namespace not responding") {
		t.Errorf("GetGrid error = %v, want it to carry the handshake failure", err)
	}

	// Not negatively cached: the same read answers the declarations once the
	// handshake works again, so the failure is an outage and not a verdict.
	ns.fail.Store(false)
	resp, err = cl.GetGrid(ctx, root)
	if err != nil {
		t.Fatalf("GetGrid after recovery: %v", err)
	}
	g := resp.GetGrid()
	if !g.GetWritable() || g.GetScratchGridId() == "" || len(g.GetSourceGrids()) == 0 {
		t.Errorf("recovered grid: writable=%v scratch=%q declared grids=%d, want the home namespace's full declarations",
			g.GetWritable(), g.GetScratchGridId(), len(g.GetSourceGrids()))
	}
}

// scratchless is a namespace with no scratch grid: its ephemeral visits land
// in the node's, home's.
type scratchless struct {
	namespacetest.Unimplemented
}

func (scratchless) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{DisplayName: "T"}, nil
}

func (scratchless) GetGrid(_ context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return &pb.GetGridResponse{Grid: &pb.Grid{Id: req.GridId}}, nil
}

// TestGetGridFailsWhenHomeInfoFails is the other arm of the same fact: the
// scratch grid every grid stamps is the node's, read from home's Info. A home
// that will not answer must fail a directory's read too, or the grid is
// answered with no landing for its ephemeral visits and nothing says so.
func TestGetGridFailsWhenHomeInfoFails(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	homeUUID, err := st.PluginUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home := &infoFlake{Namespace: local.New(st, nil)}
	reg := &Sources{}
	reg.Home = home
	reg.Files = scratchless{}
	srv := mustNew(t, reg, Config{ID: homeUUID})
	dir := rpc.QualifyID(homeUUID, rpc.KeyTileID("/r"))
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	home.fail.Store(true)
	resp, err := cl.GetGrid(ctx, dir)
	if err == nil {
		t.Fatalf("GetGrid answered while home's Info was failing: scratch=%q", resp.GetGrid().GetScratchGridId())
	}
	if !strings.Contains(err.Error(), "namespace not responding") {
		t.Errorf("GetGrid error = %v, want it to carry home's handshake failure", err)
	}

	home.fail.Store(false)
	resp, err = cl.GetGrid(ctx, dir)
	if err != nil {
		t.Fatalf("GetGrid after recovery: %v", err)
	}
	if got := resp.GetGrid().GetScratchGridId(); !strings.HasPrefix(got, homeUUID+"/") {
		t.Errorf("scratch grid = %q, want the node home's", got)
	}
}
