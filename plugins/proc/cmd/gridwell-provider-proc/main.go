// gridwell-provider-proc — the v2 proc content provider binary
// (docs/v2-design.md §5): a stateless projection of the process table
// serving contentprovider.v1. Config: pid (optional root pid, default 1).
// No database — the node owns this external's memory.
package main

import (
	"strconv"

	"github.com/josephburnett/gridwell/api/guest"
	"github.com/josephburnett/gridwell/plugins/proc/provider"
)

func main() {
	cfg := guest.Config()
	pid, _ := strconv.ParseInt(cfg["pid"], 10, 64)
	guest.ServeProvider(provider.New("", pid, nil))
}
