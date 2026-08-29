package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/proxytest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// buildPluginInfo is the pure assembly behind ListPlugins. These tests pin the
// fallback rules — especially the degraded case where a plugin's Info timed out
// or failed (info == nil), which must still produce a listed plugin so one slow
// plugin can't blank or freeze the launcher.

func TestBuildPluginInfo_InfoPresent(t *testing.T) {
	got := buildPluginInfo("uuid-1", "home", "Home", &pb.InfoResponse{
		RootGridId:    "7",
		ScratchGridId: "9",
		DisplayName:   "ignored-when-config-label-set",
		Writable:      true, // the handshake declares the capability
	}, nil)
	if got.RootGridId != "uuid-1/7" {
		t.Errorf("RootGridId = %q, want qualified uuid-1/7", got.RootGridId)
	}
	if got.ScratchGridId != "uuid-1/9" {
		t.Errorf("ScratchGridId = %q, want qualified uuid-1/9", got.ScratchGridId)
	}
	if got.Label != "Home" {
		t.Errorf("Label = %q, want the configured label Home", got.Label)
	}
	if !got.Writable {
		t.Error("a plugin whose Info declares writable must be writable")
	}
}

// The point of handshake-declared capabilities: a remote localdb reached
// through the ssh proxy has local kind "remote", but its forwarded Info still
// says writable — it must be presented writable, not stranded read-only by a
// kind check.
func TestBuildPluginInfo_WritableFromHandshakeNotKind(t *testing.T) {
	got := buildPluginInfo("u", "remote", "Remote", &pb.InfoResponse{
		RootGridId: "1",
		Writable:   true,
	}, nil)
	if !got.Writable {
		t.Error("an ssh-kind plugin whose Info declares writable must be writable")
	}
	// And the inverse: kind alone earns nothing.
	got = buildPluginInfo("u", "home", "Local", &pb.InfoResponse{RootGridId: "1"}, nil)
	if got.Writable {
		t.Error("writable must come from the Info handshake, not the kind string")
	}
}

// The parameterized-plugin declaration (issue #251): an instance grid rides
// the same handshake and gets the same qualification as the root grid, and
// declaring it never invents a root.
func TestBuildPluginInfo_InstanceGridQualified(t *testing.T) {
	got := buildPluginInfo("u", "remote", "connections", &pb.InfoResponse{
		InstanceGridId: "0",
		Writable:       true,
	}, nil)
	if got.InstanceGridId != "u/0" {
		t.Errorf("InstanceGridId = %q, want qualified u/0", got.InstanceGridId)
	}
	if got.RootGridId != "" {
		t.Errorf("RootGridId = %q, want empty — an instance grid is not a root", got.RootGridId)
	}
}

func TestBuildPluginInfo_LabelFallsBackToDisplayName(t *testing.T) {
	got := buildPluginInfo("u", "fs", "", &pb.InfoResponse{DisplayName: "Files"}, nil)
	if got.Label != "Files" {
		t.Errorf("Label = %q, want Info DisplayName Files when no config label", got.Label)
	}
	if got.Writable {
		t.Error("fs (whose Info does not declare writable) must not be writable")
	}
}

// The degraded case: Info failed or timed out → info is nil. The plugin is still
// listed (so the launcher never drops a configured plugin), with no clickable
// root/scratch grid and the configured label.
func TestBuildPluginInfo_NilInfoStillListedWithConfigLabel(t *testing.T) {
	got := buildPluginInfo("u", "proc", "Processes", nil, errors.New("dial: connection refused"))
	if got.Label != "Processes" {
		t.Errorf("Label = %q, want the configured label even when Info failed", got.Label)
	}
	if got.RootGridId != "" || got.ScratchGridId != "" {
		t.Errorf("a failed Info must leave root/scratch empty, got root=%q scratch=%q",
			got.RootGridId, got.ScratchGridId)
	}
	if got.Kind != "proc" || got.Uuid != "u" {
		t.Errorf("identity must survive a failed Info: kind=%q uuid=%q", got.Kind, got.Uuid)
	}
}

func TestBuildPluginInfo_NilInfoNoLabelFallsBackToKind(t *testing.T) {
	got := buildPluginInfo("u", "remote", "", nil, errors.New("timeout"))
	if got.Label != "remote" {
		t.Errorf("Label = %q, want the kind when neither config nor Info supplies one", got.Label)
	}
}

// TestBuildPluginInfo_InfoErrorSetOnlyWhenInfoNil pins issue #47's fix: the
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

	// The two must be otherwise identical in the one field the launcher used to
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
// (2026-08-24): an error passed WITH a non-nil info is a real post-handshake
// failure (the instance-grid read in instanceRows is the producer) and must
// ride the row verbatim — dropping it made "instance store down" and
// "healthy but rootless" identical on the wire. The old contract ("info
// non-nil wins, InfoError never set") guarded a stale-err case that had no
// producer; hiding a real error is the worse failure mode.
func TestBuildPluginInfo_ErrorRidesAlongsideLiveInfo(t *testing.T) {
	got := buildPluginInfo("u", "fs", "Files", &pb.InfoResponse{RootGridId: "1"}, errors.New("instance grid unreadable: boom"))
	if got.InfoError != "instance grid unreadable: boom" {
		t.Errorf("InfoError = %q, want the post-handshake failure verbatim", got.InfoError)
	}
}

func TestBuildPluginInfo_EmptyGridIdsNotQualified(t *testing.T) {
	// A plugin whose Info omits the grids (e.g. no ephemeral support) must not
	// emit a bare "uuid/" — empty stays empty.
	got := buildPluginInfo("u", "fs", "Files", &pb.InfoResponse{RootGridId: "3"}, nil)
	if got.RootGridId != "u/3" {
		t.Errorf("RootGridId = %q, want u/3", got.RootGridId)
	}
	if got.ScratchGridId != "" {
		t.Errorf("ScratchGridId = %q, want empty (no scratch grid)", got.ScratchGridId)
	}
}

// TestBuildPluginInfo_RootViewForwardedFromInfo pins the launcher↔plugin-root
// seam (issue #32): the server must forward Info.root_view_* into PluginInfo
// so the client's ListPlugins response carries the framing needed to seed
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
// the first one — the seam for the Info cache tests.
type countingInfoPlugin struct {
	pb.UnimplementedGridwellServer
	calls     atomic.Int32
	failFirst bool
}

func (p *countingInfoPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	n := p.calls.Add(1)
	if p.failFirst && n == 1 {
		return nil, errors.New("transient info failure")
	}
	return &pb.InfoResponse{Kind: "test", DisplayName: "T", RootGridId: "1"}, nil
}

// TestListPluginsCachesInfo: repeat ListPlugins calls serve the handshake from
// the per-uuid cache — the plugin is asked exactly once. Before the cache,
// every palette open re-handshook every plugin (up to pluginInfoTimeout each
// for a slow remote).
func TestListPluginsCachesInfo(t *testing.T) {
	fake := &countingInfoPlugin{}
	client, closer, err := plugin.ServeInProcess(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("u-1", "test", client, nil)
	srv := mustNew(t, reg, Config{})
	h := newConnectHandler(srv)

	for i := 0; i < 3; i++ {
		if _, err := h.ListPlugins(context.Background(), connect.NewRequest(&pb.ListPluginsRequest{})); err != nil {
			t.Fatalf("ListPlugins #%d: %v", i+1, err)
		}
	}
	if got := fake.calls.Load(); got != 1 {
		t.Errorf("Info called %d times across 3 ListPlugins, want 1 (cached)", got)
	}
}

// TestListPluginsRetriesFailedInfo: a failed handshake is NOT negatively
// cached — the next call retries and the plugin recovers its listing.
func TestListPluginsRetriesFailedInfo(t *testing.T) {
	fake := &countingInfoPlugin{failFirst: true}
	client, closer, err := plugin.ServeInProcess(fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("u-1", "test", client, nil)
	srv := mustNew(t, reg, Config{})
	h := newConnectHandler(srv)

	r1, err := h.ListPlugins(context.Background(), connect.NewRequest(&pb.ListPluginsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	// Degraded entry: listed, but no clickable root (info was nil).
	if got := r1.Msg.Plugins[0].RootGridId; got != "" {
		t.Errorf("failed Info should list a degraded entry, got root %q", got)
	}
	r2, err := h.ListPlugins(context.Background(), connect.NewRequest(&pb.ListPluginsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.Msg.Plugins[0].RootGridId; got != "u-1/1" {
		t.Errorf("retry after a failed Info should recover, got root %q, want u-1/1", got)
	}
	if got := fake.calls.Load(); got != 2 {
		t.Errorf("Info called %d times, want 2 (fail, then successful retry, then cache)", got)
	}
}

// alwaysFailInfoPlugin never succeeds its Info handshake — the persistently
// broken plugin the buildPluginInfo unit tests can only simulate by hand.
type alwaysFailInfoPlugin struct {
	pb.UnimplementedGridwellServer
}

func (alwaysFailInfoPlugin) Info(context.Context, *pb.InfoRequest) (*pb.InfoResponse, error) {
	return nil, errors.New("plugin exploded")
}

// TestListPluginsSurfacesInfoErrorOverTheWire crosses the seam a pure
// buildPluginInfo unit test cannot reach: server -> Connect wire (proto-JSON)
// -> rpc.Client.ListPlugins. InfoError must survive that round trip so the
// wasm launcher — which only ever sees an internal/rpc.PluginInfo, never the
// server's pb.PluginInfo — can classify a broken plugin (client/pluginhealth).
func TestListPluginsSurfacesInfoErrorOverTheWire(t *testing.T) {
	client, closer, err := plugin.ServeInProcess(alwaysFailInfoPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	reg := plugin.NewRegistry()
	reg.Register("u-broken", "fs", client, nil)
	reg.SetLabel("u-broken", "Broken")
	srv := mustNew(t, reg, Config{})
	hs := serveWeb(t, srv)
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	plugins, err := cl.ListPlugins(context.Background())
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(plugins.Plugins) != 1 {
		t.Fatalf("got %d plugins, want 1: %+v", len(plugins.Plugins), plugins.Plugins)
	}
	p := plugins.Plugins[0]
	if p.RootGridID != "" {
		t.Errorf("broken plugin RootGridID = %q, want empty", p.RootGridID)
	}
	if p.InfoError == "" {
		t.Error("broken plugin must carry a non-empty InfoError over the wire")
	}
	if !strings.Contains(p.InfoError, "plugin exploded") {
		t.Errorf("InfoError = %q, want it to mention the underlying failure", p.InfoError)
	}
}

// The #198 creation-form mechanism is RETIRED (2026-08-23, with the
// instance picker): a plugin may still declare create_schemas in Info,
// but the serving node stamps NOTHING onto grids — no client reads it,
// and a declared form that could only fail must never render. This pins
// the retirement so the stamp cannot quietly return.
func TestCreateSchemasStampAndTransit(t *testing.T) {
	ctx := context.Background()

	// A "remote" node whose one plugin declares a schema for well creation.
	remoteStore, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remoteStore.Close() })
	remotePlugin := &schemaPlugin{
		Plugin: local.New(remoteStore, nil),
		schemas: map[string]string{
			"well": `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
		},
	}
	remoteClient, remoteCloser, err := plugin.ServeInProcess(remotePlugin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remoteCloser)
	remoteReg := plugin.NewRegistry()
	remoteReg.Register("rp1", "home", remoteClient, nil)
	remoteSrv := mustNew(t, remoteReg, Config{})
	remoteHTTP := httptest.NewUnstartedServer(nil)
	remoteHTTP.Config.Handler = remoteSrv.FederationHandler()
	remoteHTTP.Config.Protocols = NodeProtocols()
	remoteHTTP.EnableHTTP2 = true
	remoteHTTP.Start()
	t.Cleanup(remoteHTTP.Close)

	// LEAF stamping: the remote's own front door carries the schema on the
	// plugin's grid.
	remoteWeb := serveWeb(t, remoteSrv) // the browser door: a second listener
	remoteCl := rpc.NewClient(remoteWeb.Client(), remoteWeb.URL, connect.WithProtoJSON())
	rootBare, err := remoteStore.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g, err := remoteCl.GetGrid(ctx, "rp1/"+rootBare)
	if err != nil {
		t.Fatalf("remote GetGrid: %v", err)
	}
	if len(g.Grid.CreateSchemas) != 0 {
		t.Fatalf("the retired mechanism stamped a schema: %v", g.Grid.CreateSchemas)
	}

	// TRANSIT: whatever the remote GRID carries (nothing, since the
	// retirement) rides verbatim — the local node adds nothing either.
	grpcConn, err := grpc.NewClient(strings.TrimPrefix(remoteHTTP.URL, "http://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = grpcConn.Close() })
	mountClient, mountCloser, err := plugin.ServeInProcess(proxytest.New(pb.NewGridwellClient(grpcConn)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mountCloser)
	localReg := plugin.NewRegistry()
	localReg.Register("sshm", "remote", mountClient, nil)
	localReg.SetTransit("sshm", true) // the declared transit (loader reads it from Info in production)
	localSrv := mustNew(t, localReg, Config{})
	localHTTP := serveWeb(t, localSrv)
	localCl := rpc.NewClient(localHTTP.Client(), localHTTP.URL, connect.WithProtoJSON())

	chained, err := localCl.GetGrid(ctx, "sshm/rp1/"+rootBare)
	if err != nil {
		t.Fatalf("chained GetGrid: %v", err)
	}
	if len(chained.Grid.CreateSchemas) != 0 {
		t.Fatalf("transit invented a schema: %v", chained.Grid.CreateSchemas)
	}
}

// schemaPlugin wraps a localdb with a create_schemas declaration.
type schemaPlugin struct {
	*local.Plugin
	schemas map[string]string
}

func (p *schemaPlugin) Info(ctx context.Context, req *pb.InfoRequest) (*pb.InfoResponse, error) {
	info, err := p.Plugin.Info(ctx, req)
	if err != nil {
		return nil, err
	}
	info.CreateSchemas = p.schemas
	return info, nil
}
