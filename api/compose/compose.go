package compose

// The in-process gridwell.v1 shape: the node's NATIVE kinds (the local
// store, the remote transport) and the pluginhost adapters are served
// over a real gRPC loopback so the router holds one GridwellClient
// whatever stands behind it. Enumeration of what ships is a leaf-binary
// privilege (charter, 2026-08-15).

import (
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// NativeFactory constructs an in-process gridwell.v1 server (a native kind)
// from its config map: db_file, uuid, kind, plus kind-specific keys —
// the same vocabulary a provider binary reads from the spawn environment
// (guest.Config).
type NativeFactory func(cfg map[string]string) (gridwellv1.GridwellServer, error)

// ServeInProcess starts a gRPC server in a goroutine on a loopback TCP
// port and returns a client connected to it. closer stops the server and
// closes the connection. The in-process half of the Loadout door; also
// the seam-test harness everywhere a real plugin is exercised without a
// subprocess.
func ServeInProcess(impl gridwellv1.GridwellServer) (gridwellv1.GridwellClient, func(), error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("in-process listen: %w", err)
	}

	srv := grpc.NewServer()
	gridwellv1.RegisterGridwellServer(srv, impl)
	go srv.Serve(lis)

	addr := lis.Addr().String()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		return nil, nil, fmt.Errorf("in-process dial %s: %w", addr, err)
	}

	closer := func() {
		cc.Close()
		srv.GracefulStop()
	}
	return gridwellv1.NewGridwellClient(cc), closer, nil
}
