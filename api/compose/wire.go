// Package compose is how a node gets its plugins. A plugin is always an
// out-of-process binary (LoadPlugin, over go-plugin: process isolation and a
// separate dependency graph, which is the third-party door). The node's
// pluginhost adapter fronts it with a pluginv1.PluginClient and reaches the
// router as an ordinary in-process namespace.
//
// The subprocess is one of the node's two gRPC hops; the other is the
// connection door. Everything else inside the node is a Go value
// (internal/namespace).
//
// This file is the go-plugin handshake both sides share, and plugin.go is
// the host-side spawn. That is all of the door this repository owns: the
// guest-side helper a plugin binary's main() calls lives in the plugins
// repository, github.com/josephburnett/gridwell-plugins/guest, and so does
// every plugin.
package compose

import (
	"github.com/hashicorp/go-plugin"
)

// HandshakeConfig is the security/version handshake exchanged between the
// host and plugin process at startup. Both sides must present the same
// MagicCookie values or the host refuses the connection.
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "GRIDWELL_PLUGIN",
	MagicCookieValue: "gridwell-plugin-v1",
}
