// gridwell-plugin-proc — the proc plugin binary (docs/v2-design.md §5):
// a stateless projection of the process table serving plugin.v1. The
// config vocabulary is plugin.FromConfig's — the one derivation every
// door shares. No database — the node owns this external's memory.
package main

import (
	"github.com/josephburnett/gridwell/api/guest"
	"github.com/josephburnett/gridwell/plugins/proc/plugin"
)

func main() { guest.Main(plugin.FromConfig) }
