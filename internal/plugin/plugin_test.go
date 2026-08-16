package plugin_test

import (
	"context"
	"net"
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
	factories := map[string]plugin.ServerFactory{
		"stub": func(_ map[string]string) (gridwellv1.GridwellServer, error) {
			return &stubServer{}, nil
		},
	}
	reg, err := plugin.LoadAll(cfg, factories)
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
