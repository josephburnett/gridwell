// Package compose is the api library's COMPOSITION door: how a node gets
// its content providers, without anything above it knowing which way. A
// provider is either an out-of-process binary (LoadPlugin — go-plugin:
// process isolation, a separate dependency graph — the third-party door)
// or a compiled-in factory (PluginInProcess: bundled binaries;
// iOS, where fork/exec does not exist); the node's pluginhost adapter
// fronts either with the same gridwellv1.GridwellClient. The gridwell.v1
// subprocess door (a plugin owning its own ids and layout) was retired
// 2026-08-27: plugins ARE providers (docs/content-presentation.md §9).
//
// This file is the go-plugin handshake both sides share; provider.go is
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
