package compose

// The in-process gridwell.v1 shape: the node's NATIVE kinds (the local
// store, the remote transport) and the pluginhost adapters are served
// over a real gRPC loopback so the router holds one GridwellClient
// whatever stands behind it. Enumeration of what ships is a leaf-binary
// privilege (charter, 2026-08-15).

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// NativeFactory constructs an in-process gridwell.v1 server (a native kind)
// from its config map: db_file, uuid, kind, plus kind-specific keys —
// the same vocabulary a plugin binary reads from the spawn environment
// (guest.Config).
type NativeFactory func(cfg map[string]string) (gridwellv1.GridwellServer, error)

// ServeInProcess starts a gRPC server in a goroutine on a loopback TCP
// port and returns a client connected to it. closer stops the server and
// closes the connection. The in-process half of the Loadout door; also
// the seam-test harness everywhere a real plugin is exercised without a
// subprocess.
func ServeInProcess(impl gridwellv1.GridwellServer) (gridwellv1.GridwellClient, func(), error) {
	// An IN-MEMORY listener (bufconn): the hop never touches a socket, so
	// nothing outside this process can reach the impl. (It used to listen
	// on 127.0.0.1:0 with no auth — every local process could open the
	// home store's shells; security review 2026-08-26.)
	lis := bufconn.Listen(1 << 20)

	srv := grpc.NewServer()
	gridwellv1.RegisterGridwellServer(srv, impl)
	go srv.Serve(lis)

	cc, err := grpc.NewClient("passthrough:///in-process",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		srv.Stop()
		return nil, nil, fmt.Errorf("in-process dial: %w", err)
	}

	closer := func() {
		cc.Close()
		srv.GracefulStop()
	}
	return gridwellv1.NewGridwellClient(cc), closer, nil
}
