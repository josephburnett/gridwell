package compose

// The v2 content-provider half of the compose sugar (docs/v2-design.md):
// a provider binary serves contentprovider.v1 instead of gridwell.v1;
// this helper is the in-process shape — a real gRPC loopback, so the
// caller holds the same client interface a subprocess dial would give.

import (
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	contentproviderv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
)

// ServeProviderInProcess serves a ContentProvider implementation over a
// loopback gRPC server and returns the connected client — the provider
// twin of ServeInProcess.
func ServeProviderInProcess(impl contentproviderv1.ContentProviderServer) (contentproviderv1.ContentProviderClient, func(), error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("in-process provider listen: %w", err)
	}

	srv := grpc.NewServer()
	contentproviderv1.RegisterContentProviderServer(srv, impl)
	go srv.Serve(lis)

	addr := lis.Addr().String()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		return nil, nil, fmt.Errorf("in-process provider dial %s: %w", addr, err)
	}

	closer := func() {
		cc.Close()
		srv.GracefulStop()
	}
	return contentproviderv1.NewContentProviderClient(cc), closer, nil
}
