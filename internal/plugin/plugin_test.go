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
	return &gridwellv1.InfoResponse{Kind: "stub", DisplayName: "Stub Plugin"}, nil
}

func (s *stubServer) Attach(_ context.Context, _ *gridwellv1.AttachRequest) (*gridwellv1.AttachResponse, error) {
	return &gridwellv1.AttachResponse{RootGridId: 42, Label: "stub-root"}, nil
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

	ar, err := client.Attach(context.Background(), &gridwellv1.AttachRequest{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if ar.RootGridId != 42 {
		t.Errorf("Attach.RootGridId: got %d, want 42", ar.RootGridId)
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
		"stub": func(_ *config.PluginConfig) (gridwellv1.GridwellServer, error) {
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

// TestRegistry_Deregister verifies that Deregister removes the entry.
func TestRegistry_Deregister(t *testing.T) {
	client, closer, err := plugin.ServeInProcess(&stubServer{})
	if err != nil {
		t.Fatalf("ServeInProcess: %v", err)
	}
	reg := plugin.NewRegistry()
	reg.Register("test-id", client, closer)

	if _, ok := reg.Get("test-id"); !ok {
		t.Fatal("plugin not registered")
	}
	reg.Deregister("test-id")
	if _, ok := reg.Get("test-id"); ok {
		t.Error("plugin still in registry after Deregister")
	}
}
