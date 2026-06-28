// Package plugin provides the shared constants, handshake config, and
// go-plugin glue for Gridwell's out-of-process plugin transport. The same
// gRPC service (api/gridwell/v1/data.proto) used for client↔server is also
// used for server↔plugin — every node speaks the same wire protocol.
//
// Usage:
//
//	Host side: plugin/host.Load(cfg) → gridwellv1.GridwellClient
//	Guest side: plugin/guest.Serve(impl) in the plugin binary's main()
package plugin

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// HandshakeConfig is the security/version handshake exchanged between the
// host and plugin process at startup. Both sides must present the same
// MagicCookie values or the host refuses the connection.
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "GRIDWELL_PLUGIN",
	MagicCookieValue: "gridwell-plugin-v1",
}

// PluginName is the key under which every Gridwell plugin registers itself.
const PluginName = "gridwell"

// gridwellGRPCPlugin bridges go-plugin's gRPC transport and the Gridwell
// gRPC service. Impl is set on the guest side (nil on the host side).
type gridwellGRPCPlugin struct {
	plugin.Plugin
	Impl gridwellv1.GridwellServer
}

func (p *gridwellGRPCPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	gridwellv1.RegisterGridwellServer(s, p.Impl)
	return nil
}

func (p *gridwellGRPCPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return gridwellv1.NewGridwellClient(c), nil
}

// PluginMap is the map passed to both plugin.NewClient and plugin.Serve. It
// contains only "gridwell"; the plugin name is the dispatch key. Pass a copy
// with Impl set to the real implementation on the guest side.
func PluginMap(impl gridwellv1.GridwellServer) map[string]plugin.Plugin {
	return map[string]plugin.Plugin{
		PluginName: &gridwellGRPCPlugin{Impl: impl},
	}
}
