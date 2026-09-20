// Package compose is the plugin door this repository owns: the go-plugin
// handshake both sides present, and the host-side spawn. A plugin is always
// an out-of-process binary, so third-party code runs with its own dependency
// graph. The guest-side helper and every plugin live in gridwell-plugins.
package compose

import (
	"github.com/hashicorp/go-plugin"
)

// HandshakeConfig gates the spawn: on a mismatch the host refuses.
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "GRIDWELL_PLUGIN",
	MagicCookieValue: "gridwell-plugin-v1",
}
