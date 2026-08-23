// gridwell-provider-fs — the v2 fs content provider binary
// (docs/v2-design.md §5): a stateless projection of a directory tree
// serving contentprovider.v1. Config: root (the projected directory).
// No database — the node owns this external's memory.
package main

import (
	"github.com/josephburnett/gridwell/api/guest"
	"github.com/josephburnett/gridwell/plugins/fs/provider"
)

func main() {
	cfg := guest.Config()
	guest.ServeProvider(provider.New(cfg["root"], nil))
}
