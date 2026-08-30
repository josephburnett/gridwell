// Package compose is the api library's COMPOSITION door: how a node gets
// its plugins, without anything above it knowing which way. A
// plugin is either an out-of-process binary (LoadPlugin — go-plugin:
// process isolation, a separate dependency graph — the third-party door)
// or a compiled-in factory (PluginInProcess: bundled binaries;
// iOS, where fork/exec does not exist); the node's pluginhost adapter
// fronts either with the same pluginv1.PluginClient and reaches the router
// as an ordinary in-process namespace. The gridwell.v1 subprocess door (a
// plugin owning its own ids and layout) was retired 2026-08-27: plugins
// ARE plugins (docs/content-presentation.md §9).
//
// This subprocess is ONE of the node's two remaining gRPC hops (the other
// is the federation socket) — it crosses a process boundary and a separate
// dependency graph, which is the whole point of the door. Everything
// inside the node is a Go value (internal/namespace,
// docs/simplify-plan.md S2); ServeInProcess — the bufconn loopback that
// used to front the node's OWN store and transport — is gone with it.
//
// This file is the go-plugin handshake both sides share; plugin.go is
// the host-side spawn; guest.Serve (api/guest) is the binary's
// main.
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
