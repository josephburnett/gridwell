package host_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// stubServer is a minimal GridwellServer used to verify in-process gRPC
// round-trips over the transport layer. Embeds UnimplementedGridwellServer
// so only the methods under test need to be defined.
type stubServer struct {
	gridwellv1.UnimplementedGridwellServer
}

func (s *stubServer) Info(ctx context.Context, req *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{Kind: "stub", DisplayName: "Stub Plugin"}, nil
}

func (s *stubServer) Attach(ctx context.Context, req *gridwellv1.AttachRequest) (*gridwellv1.AttachResponse, error) {
	return &gridwellv1.AttachResponse{RootGridId: 1, Label: "root"}, nil
}

// TestInProcessRoundTrip verifies that the gRPC server/client stubs generated
// from data.proto can serve and receive calls. This does not involve subprocess
// management — it tests the gRPC layer only.
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

	resp, err := client.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if resp.Kind != "stub" {
		t.Errorf("Info.Kind: got %q, want %q", resp.Kind, "stub")
	}

	ar, err := client.Attach(context.Background(), &gridwellv1.AttachRequest{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if ar.RootGridId != 1 {
		t.Errorf("Attach.RootGridId: got %d, want 1", ar.RootGridId)
	}
}
