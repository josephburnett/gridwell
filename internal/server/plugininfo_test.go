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
	"github.com/josephburnett/gridwell/internal/plugin"
)

// buildPluginInfo is the pure assembly behind Handshake. These tests pin the
// fallback rules — especially the degraded case where a plugin's Info timed out
// or failed (info == nil), which must still produce a listed plugin so one slow
// plugin can't blank or freeze the + menu.

func TestBuildPluginInfo_InfoPresent(t *testing.T) {
	got := buildPluginInfo("uuid-1", "home", "Home", &pb.InfoResponse{
		RootGridId:  "7",
		DisplayName: "ignored-when-config-label-set",
	}, nil)
	if got.RootGridId != "uuid-1/7" {
		t.Errorf("RootGridId = %q, want qualified uuid-1/7", got.RootGridId)
	}
	if got.Label != "Home" {
		t.Errorf("Label = %q, want the configured label Home", got.Label)
	}
}

func TestBuildPluginInfo_LabelFallsBackToDisplayName(t *testing.T) {
	got := buildPluginInfo("u", "fs", "", &pb.InfoResponse{DisplayName: "Files"}, nil)
	if got.Label != "Files" {
		t.Errorf("Label = %q, want Info DisplayName Files when no config label", got.Label)
	}
}

// The degraded case: Info failed or timed out → info is nil. The plugin is still
// listed (so the + menu never drops a configured plugin), with no clickable
// root grid and the configured label.
func TestBuildPluginInfo_NilInfoStillListedWithConfigLabel(t *testing.T) {
	got := buildPluginInfo("u", "proc", "Processes", nil, errors.New("dial: connection refused"))
	if got.Label != "Processes" {
		t.Errorf("Label = %q, want the configured label even when Info failed", got.Label)
	}
	if got.RootGridId != "" {
		t.Errorf("a failed Info must leave the root empty, got root=%q", got.RootGridId)
	}
	if got.Uuid != "u" {
		t.Errorf("identity must survive a failed Info: uuid=%q", got.Uuid)
	}
}

func TestBuildPluginInfo_NilInfoNoLabelFallsBackToKind(t *testing.T) {
	got := buildPluginInfo("u", "remote", "", nil, errors.New("timeout"))
	if got.Label != "remote" {
		t.Errorf("Label = %q, want the kind when neither config nor Info supplies one", got.Label)
	}
}

// TestBuildPluginInfo_InfoErrorSetOnlyWhenInfoNil pins the fix: the
// info_error field is the ONLY thing that distinguishes a broken plugin from a
// healthy-but-rootless one — both otherwise leave RootGridId == "". It must be
// populated whenever Info failed, and empty whenever Info succeeded (even with
// no root).
func TestBuildPluginInfo_InfoErrorSetOnlyWhenInfoNil(t *testing.T) {
	broken := buildPluginInfo("u", "fs", "Files", nil, errors.New("dial tcp: connection refused"))
	if broken.InfoError == "" {
		t.Error("a failed Info must set InfoError")
	}
	if !strings.Contains(broken.InfoError, "connection refused") {
		t.Errorf("InfoError = %q, want it to carry the underlying error text", broken.InfoError)
	}

	rootless := buildPluginInfo("u", "fs", "Files", &pb.InfoResponse{DisplayName: "Files"}, nil)
	if rootless.InfoError != "" {
		t.Errorf("a successful Info (even with no root) must leave InfoError empty, got %q", rootless.InfoError)
	}

	// The two must be otherwise identical in the one field the + menu used to
	// key off of (RootGridId == "") — InfoError is the only signal that tells
	// them apart.
	if broken.RootGridId != "" || rootless.RootGridId != "" {
		t.Fatalf("test setup: expected both RootGridId empty, got broken=%q rootless=%q",
			broken.RootGridId, rootless.RootGridId)
	}
	if broken.InfoError == rootless.InfoError {
		t.Error("broken and rootless PluginInfo must be distinguishable by InfoError")
	}
}

// TestBuildPluginInfo_ErrorRidesAlongsideLiveInfo pins the revised contract
// An error passed with a non-nil info is a real post-handshake
// failure (the instance-grid read in instanceRows is the producer) and must
// ride the row verbatim — dropping it made "instance store down" and
// "healthy but rootless" identical on the wire. The old contract ("info
// non-nil wins, InfoError never set") guarded a stale-err case that had no
// producer; hiding a real error is the worse failure mode.
func TestBuildPluginInfo_ErrorRidesAlongsideLiveInfo(t *testing.T) {
	got := buildPluginInfo("u", "fs", "Files", &pb.InfoResponse{RootGridId: "1"}, errors.New("boom"))
	if got.InfoError != "boom" {
		t.Errorf("InfoError = %q, want the post-handshake failure verbatim", got.InfoError)
	}
}

func TestBuildPluginInfo_EmptyGridIdNotQualified(t *testing.T) {
	// A plugin whose Info names no root must not emit a bare "uuid/" — empty
	// stays empty.
	got := buildPluginInfo("u", "fs", "Files", &pb.InfoResponse{}, nil)
	if got.RootGridId != "" {
		t.Errorf("RootGridId = %q, want empty", got.RootGridId)
	}
}

// TestBuildPluginInfo_RootViewForwardedFromInfo pins the menu-to-plugin-root
// seam: the server must forward Info.root_view_* into PluginInfo
// so the client's Handshake response carries the framing needed to seed
// enterPlugin. Zero (never visited) must pass through too — the client
// distinguishes it from an explicit user view.
func TestBuildPluginInfo_RootViewForwardedFromInfo(t *testing.T) {
	got := buildPluginInfo("uuid-1", "home", "Home", &pb.InfoResponse{
		RootGridId:   "7",
		RootViewCx:   3.5,
		RootViewCy:   -2.25,
		RootViewZoom: 1.75,
	}, nil)
	if got.RootViewCx != 3.5 {
		t.Errorf("RootViewCx = %v, want 3.5", got.RootViewCx)
	}
	if got.RootViewCy != -2.25 {
		t.Errorf("RootViewCy = %v, want -2.25", got.RootViewCy)
	}
	if got.RootViewZoom != 1.75 {
		t.Errorf("RootViewZoom = %v, want 1.75", got.RootViewZoom)
	}

	// Zero (never visited) passes through unchanged.
	zero := buildPluginInfo("u", "home", "X", &pb.InfoResponse{
		RootGridId:   "1",
		RootViewZoom: 0,
	}, nil)
	if zero.RootViewZoom != 0 {
		t.Errorf("RootViewZoom (never visited) = %v, want 0", zero.RootViewZoom)
	}
}

// countingInfoPlugin is a minimal plugin that counts Info calls and can fail
// the first one.
type countingInfoPlugin struct {
	namespacetest.Unimplemented
	calls     atomic.Int32
	failFirst bool
}

func (p *countingInfoPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	n := p.calls.Add(1)
	if p.failFirst && n == 1 {
		return nil, errors.New("transient info failure")
	}
	return &pb.InfoResponse{DisplayName: "T", RootGridId: "1"}, nil
}

// TestListPluginsRetriesFailedInfo: a failed Info lists a degraded row, and
// the next Handshake asks the namespace again and lists it whole.
func TestListPluginsRetriesFailedInfo(t *testing.T) {
	fake := &countingInfoPlugin{failFirst: true}
	client := fake
	reg := plugin.NewRegistry()
	reg.Register("u-1", "test", client, nil)
	srv := mustNew(t, reg, Config{})
	h := newConnectHandler(newRouter(srv))

	r1, err := h.Handshake(context.Background(), connect.NewRequest(&pb.HandshakeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	// Degraded entry: listed, but no clickable root (info was nil).
	if got := r1.Msg.Plugins[0].RootGridId; got != "" {
		t.Errorf("failed Info should list a degraded entry, got root %q", got)
	}
	r2, err := h.Handshake(context.Background(), connect.NewRequest(&pb.HandshakeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.Msg.Plugins[0].RootGridId; got != "u-1/1" {
		t.Errorf("retry after a failed Info should recover, got root %q, want u-1/1", got)
	}
	if got := fake.calls.Load(); got != 2 {
		t.Errorf("Info called %d times, want 2 (fail, then the answer)", got)
	}
}

// hungInfoPlugin answers Info only when its caller gives up.
type hungInfoPlugin struct{ namespacetest.Unimplemented }

func (hungInfoPlugin) Info(ctx context.Context, _ *pb.InfoRequest) (*pb.InfoResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A namespace whose Info never answers lists broken once pluginInfoTimeout
// runs out, and the rest of the menu is not held behind it.
func TestAHungInfoListsBrokenAfterTheTimeout(t *testing.T) {
	reg := plugin.NewRegistry()
	reg.Register("u-hung", "test", hungInfoPlugin{}, nil)
	h := newConnectHandler(newRouter(mustNew(t, reg, Config{})))
	start := time.Now()
	r, err := h.Handshake(context.Background(), connect.NewRequest(&pb.HandshakeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < pluginInfoTimeout || took > pluginInfoTimeout+2*time.Second {
		t.Errorf("Handshake took %v, want it bounded by pluginInfoTimeout (%v)", took, pluginInfoTimeout)
	}
	if r.Msg.Plugins[0].InfoError == "" {
		t.Error("a hung Info lists no InfoError")
	}
}

// alwaysFailInfoPlugin never succeeds its Info handshake — the persistently
// broken plugin the buildPluginInfo unit tests can only simulate by hand.
type alwaysFailInfoPlugin struct {
	namespacetest.Unimplemented
}

func (alwaysFailInfoPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return nil, errors.New("plugin exploded")
}

// TestListPluginsSurfacesInfoErrorOverTheWire crosses the seam a pure
// buildPluginInfo unit test cannot reach: server -> Connect wire (proto-JSON)
// -> rpc.Client.Handshake. InfoError must survive that round trip so the
// wasm client — which only ever sees an pb.PluginInfo, never the
// server's pb.PluginInfo — can classify a broken plugin (client/pluginhealth).
func TestListPluginsSurfacesInfoErrorOverTheWire(t *testing.T) {
	client := alwaysFailInfoPlugin{}
	reg := plugin.NewRegistry()
	reg.Register("u-broken", "fs", client, nil)
	reg.SetLabel("u-broken", "Broken")
	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	plugins, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if len(plugins.Plugins) != 1 {
		t.Fatalf("got %d plugins, want 1: %+v", len(plugins.Plugins), plugins.Plugins)
	}
	p := plugins.Plugins[0]
	if p.RootGridId != "" {
		t.Errorf("broken plugin RootGridID = %q, want empty", p.RootGridId)
	}
	if p.InfoError == "" {
		t.Error("broken plugin must carry a non-empty InfoError over the wire")
	}
	if !strings.Contains(p.InfoError, "plugin exploded") {
		t.Errorf("InfoError = %q, want it to mention the underlying failure", p.InfoError)
	}
}

// infoFlakePlugin is a real namespace whose Info handshake can be failed at
// will: the hung or just-restarted plugin, without a subprocess.
type infoFlakePlugin struct {
	namespace.Namespace
	fail atomic.Bool
}

func (p *infoFlakePlugin) Info(ctx context.Context, req *pb.InfoRequest) (*pb.InfoResponse, error) {
	if p.fail.Load() {
		return nil, errors.New("plugin not responding")
	}
	return p.Namespace.Info(ctx, req)
}

// TestGetGridFailsWhenOwnerInfoFails crosses the read seam a buildPluginInfo
// unit test cannot reach: server -> Connect wire -> rpc.Client.GetGrid. Grid
// plugin_grids comes from the owning namespace's Info and scratch_grid_id
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
	ns := &infoFlakePlugin{Namespace: local.New(st, nil)}
	reg := plugin.NewRegistry()
	reg.Register(uuid, "home", ns, nil)
	srv := mustNew(t, reg, Config{ID: uuid})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	root := uuid + "/" + bareRoot
	ns.fail.Store(true)
	resp, err := cl.GetGrid(ctx, root)
	if err == nil {
		g := resp.GetGrid()
		t.Fatalf("GetGrid answered while the owner's Info was failing: writable=%v scratch=%q declared grids=%d",
			g.GetWritable(), g.GetScratchGridId(), len(g.GetPluginGrids()))
	}
	if !strings.Contains(err.Error(), "plugin not responding") {
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
	if !g.GetWritable() || g.GetScratchGridId() == "" || len(g.GetPluginGrids()) == 0 {
		t.Errorf("recovered grid: writable=%v scratch=%q declared grids=%d, want the home namespace's full declarations",
			g.GetWritable(), g.GetScratchGridId(), len(g.GetPluginGrids()))
	}
}

// plainPlugin is a plugin: its ephemeral visits land in the node's scratch
// grid, home's.
type plainPlugin struct {
	namespacetest.Unimplemented
}

func (plainPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return &pb.InfoResponse{DisplayName: "T", RootGridId: "1"}, nil
}

func (plainPlugin) GetGrid(_ context.Context, req *pb.GetGridRequest) (*pb.GetGridResponse, error) {
	return &pb.GetGridResponse{Grid: &pb.Grid{Id: req.GridId}}, nil
}

// TestGetGridFailsWhenHomeInfoFails is the other arm of the same fact: the
// scratch grid every grid stamps is the node's, read from home's Info. A home
// that will not answer must fail a plugin grid's read too, or the grid is
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
	home := &infoFlakePlugin{Namespace: local.New(st, nil)}
	reg := plugin.NewRegistry()
	reg.Register(homeUUID, "home", home, nil)
	reg.Register("u-scratchless", "test", plainPlugin{}, nil)
	srv := mustNew(t, reg, Config{ID: homeUUID})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	home.fail.Store(true)
	resp, err := cl.GetGrid(ctx, "u-scratchless/1")
	if err == nil {
		t.Fatalf("GetGrid answered while home's Info was failing: scratch=%q", resp.GetGrid().GetScratchGridId())
	}
	if !strings.Contains(err.Error(), "plugin not responding") {
		t.Errorf("GetGrid error = %v, want it to carry home's handshake failure", err)
	}

	home.fail.Store(false)
	resp, err = cl.GetGrid(ctx, "u-scratchless/1")
	if err != nil {
		t.Fatalf("GetGrid after recovery: %v", err)
	}
	if got := resp.GetGrid().GetScratchGridId(); !strings.HasPrefix(got, homeUUID+"/") {
		t.Errorf("scratch grid = %q, want the node home's", got)
	}
}
