package plugin_test

import (
	"context"
	"fmt"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// stubServer is a minimal GridwellServer for transport tests.
type stubServer struct {
	gridwellv1.UnimplementedGridwellServer
}

func (s *stubServer) Info(_ context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{Kind: "stub", DisplayName: "Stub Plugin", RootGridId: "42"}, nil
}

// TestInProcessRoundTrip verifies the gRPC server/client stubs round-trip
// over a loopback TCP connection (same as ServeInProcess uses).
func TestInProcessRoundTrip(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	gridwellv1.RegisterGridwellServer(srv, &stubServer{})
	go srv.Serve(lis)
	defer srv.GracefulStop()

	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cc.Close()
	client := gridwellv1.NewGridwellClient(cc)

	info, err := client.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Kind != "stub" {
		t.Errorf("Info.Kind: got %q, want %q", info.Kind, "stub")
	}
	if info.RootGridId != "42" {
		t.Errorf("Info.RootGridId: got %q, want %q", info.RootGridId, "42")
	}
}

// TestServeInProcess verifies that ServeInProcess returns a working client.
func TestServeInProcess(t *testing.T) {
	client, closer, err := plugin.ServeInProcess(&stubServer{})
	if err != nil {
		t.Fatalf("ServeInProcess: %v", err)
	}
	defer closer()

	info, err := client.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Kind != "stub" {
		t.Errorf("Info.Kind: got %q, want %q", info.Kind, "stub")
	}
}

// TestLoadAll verifies that LoadAll builds a registry with the correct entries.
func TestLoadAll(t *testing.T) {
	cfg := &config.ServerConfig{
		Plugins: []config.PluginConfig{
			{ID: "uuid-a", Name: "alpha", Kind: "stub"},
			{ID: "uuid-b", Name: "beta", Kind: "stub"},
		},
	}
	factories := map[string]plugin.NativeFactory{
		"stub": func(_ map[string]string) (gridwellv1.GridwellServer, error) {
			return &stubServer{}, nil
		},
	}
	reg, err := plugin.LoadAll(cfg, factories, nil)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	defer reg.Close()

	for _, id := range []string{"uuid-a", "uuid-b"} {
		c, ok := reg.Get(id)
		if !ok {
			t.Errorf("plugin %q not in registry", id)
			continue
		}
		info, err := c.Info(context.Background(), &gridwellv1.InfoRequest{})
		if err != nil {
			t.Errorf("Info via %q: %v", id, err)
			continue
		}
		if info.Kind != "stub" {
			t.Errorf("plugin %q: Info.Kind = %q, want %q", id, info.Kind, "stub")
		}
	}
}

// TestRegistry_GetMissing verifies that a missing plugin returns (nil, false).
func TestRegistry_GetMissing(t *testing.T) {
	reg := plugin.NewRegistry()
	_, ok := reg.Get("nonexistent")
	if ok {
		t.Error("Get of nonexistent plugin should return false")
	}
}

// TestRegistry_Label round-trips the configured display name and returns ""
// for an unlabelled plugin (so callers fall back to Info / kind).
func TestRegistry_Label(t *testing.T) {
	reg := plugin.NewRegistry()
	reg.SetLabel("p1", "files")
	if got := reg.Label("p1"); got != "files" {
		t.Errorf("Label(p1) = %q, want files", got)
	}
	if got := reg.Label("unset"); got != "" {
		t.Errorf("Label(unset) = %q, want empty", got)
	}
}

// handshakeRefuser is a plugin whose Info refuses — the shape of "I do
// not have the config I need" (FailedPrecondition with the reason).
type handshakeRefuser struct {
	pluginv1.UnimplementedPluginServer
}

func (handshakeRefuser) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return nil, status.Error(codes.FailedPrecondition, "token_file not configured")
}

// TestLoadAllFailsOnARefusedHandshake (owner decision 2026-08-27): a plugin
// that cannot answer Info stops the launch with its reason, instead of
// coming up as an empty grid with a log line nobody reads.
func TestLoadAllFailsOnARefusedHandshake(t *testing.T) {
	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{
		ID: "gl1234a", Name: "todos", Kind: "gitlab", Config: map[string]string{"db_file": filepath.Join(t.TempDir(), "mem.db")},
	}}}
	factories := map[string]plugin.Factory{"gitlab": func(map[string]string) (pluginv1.PluginServer, error) { return handshakeRefuser{}, nil }}
	_, err := plugin.LoadAll(cfg, nil, factories)
	if err == nil || !strings.Contains(err.Error(), "token_file not configured") || !strings.Contains(err.Error(), "todos") {
		t.Fatalf("LoadAll = %v, want the plugin's own reason, naming it", err)
	}
}

// closingStub is a native impl that owns a resource (as local.Plugin owns
// its store and remote.Server its ssh sessions) and releases it in Close.
type closingStub struct {
	stubServer
	closed bool
}

func (s *closingStub) Close() error { s.closed = true; return nil }

// TestLoadAllClosesNativeImpls crosses the loader→registry seam for the
// native lifecycle: the closer LoadAll registers must release the impl's
// own resources, not just the loopback transport in front of it. Before
// this, local.Plugin.Close and remote.Server.Close had zero production
// callers — every serve exited with the store and ssh sessions never
// closed.
func TestLoadAllClosesNativeImpls(t *testing.T) {
	impl := &closingStub{}
	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{ID: "uuid-a", Name: "alpha", Kind: "stub"}}}
	factories := map[string]plugin.NativeFactory{
		"stub": func(_ map[string]string) (gridwellv1.GridwellServer, error) { return impl, nil },
	}
	reg, err := plugin.LoadAll(cfg, factories, nil)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	reg.Close()
	if !impl.closed {
		t.Fatal("Registry.Close did not Close the native impl")
	}
}

// TestLoadAllFailsOnARefusingFactory: the in-process door's twin of the
// refused handshake — a Factory (a plugin's FromConfig) that refuses its
// config stops the launch with the reason, naming the plugin. The bundled
// binaries hand the loader the SAME FromConfig the subprocess main hands
// guest.Main, so `pid: abc` cannot come up as the whole process tree
// through either door.
func TestLoadAllFailsOnARefusingFactory(t *testing.T) {
	cfg := &config.ServerConfig{Plugins: []config.PluginConfig{{
		ID: "pr1234a", Name: "procs", Kind: "proc", Config: map[string]string{"pid": "abc", "db_file": filepath.Join(t.TempDir(), "mem.db")},
	}}}
	factories := map[string]plugin.Factory{"proc": func(cfg map[string]string) (pluginv1.PluginServer, error) {
		return nil, fmt.Errorf("pid %q is not a positive process id", cfg["pid"])
	}}
	_, err := plugin.LoadAll(cfg, nil, factories)
	if err == nil || !strings.Contains(err.Error(), `pid "abc"`) || !strings.Contains(err.Error(), "procs") {
		t.Fatalf("LoadAll = %v, want the factory's reason, naming the plugin", err)
	}
}

// Close is terminal for the clients, and must be for every per-plugin fact:
// labels and transit verdicts survived it, so a re-Register after Close
// inherited a stale transit-ness.
func TestRegistry_CloseForgetsEveryFact(t *testing.T) {
	reg := plugin.NewRegistry()
	reg.Register("p1", "fs", nil, nil)
	reg.SetLabel("p1", "files")
	reg.SetTransit("p1", true)
	reg.Close()
	if reg.Label("p1") != "" || reg.Transit("p1") {
		t.Fatalf("after Close: label=%q transit=%v, want nothing remembered", reg.Label("p1"), reg.Transit("p1"))
	}
}
