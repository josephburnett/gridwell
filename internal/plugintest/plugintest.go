// Package plugintest is the seam harness for tests that need a plugin.v1
// client without a subprocess. A plugin loads exactly one way in production —
// compose.LoadPlugin spawns a gridwell-plugin-<kind> binary — and
// internal/plugin's e2e test and test/federation are what cover that spawn.
// What the tests here need is the OTHER side of the adapter: a
// pluginv1.PluginServer value, built in the test, reached through the same
// pluginv1.PluginClient interface the spawn hands back.
//
// Loopback is a real gRPC server on an in-memory listener, not a direct method
// call, so the marshalling the wire does still happens: a message the plugin
// cannot serialize fails here too, and no answer is shared by pointer across
// the seam.
package plugintest

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// Loopback serves impl over an in-memory gRPC connection and returns the
// connected client plus its closer. No socket, so nothing outside the process
// can reach it.
func Loopback(impl pluginv1.PluginServer) (pluginv1.PluginClient, func(), error) {
	lis := bufconn.Listen(1 << 20)

	srv := grpc.NewServer()
	pluginv1.RegisterPluginServer(srv, impl)
	go srv.Serve(lis)

	cc, err := grpc.NewClient("passthrough:///plugintest",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		srv.Stop()
		return nil, nil, fmt.Errorf("plugintest loopback dial: %w", err)
	}

	closer := func() {
		cc.Close()
		srv.GracefulStop()
	}
	return pluginv1.NewPluginClient(cc), closer, nil
}
