// gridwell-plugin-fs — the v2 fs content provider binary
// (docs/v2-design.md §5): a stateless projection of a directory tree
// serving plugin.v1. Config: root (the projected directory).
// No database — the node owns this external's memory.
package main

import (
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/guest"
	"github.com/josephburnett/gridwell/plugins/fs/plugin"
)

func main() {
	guest.Main(func(cfg map[string]string) (pluginv1.PluginServer, error) {
		return plugin.New(cfg["root"], nil), nil
	})
}
