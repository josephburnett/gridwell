// gridwell-plugin-gitlab — the gitlab todos plugin binary: a stateless
// projection of one GitLab account's to-do list, serving plugin.v1. The
// config vocabulary is plugin.FromConfig's — the one derivation every
// door shares. No database — the node owns this external's memory.
package main

import (
	"github.com/josephburnett/gridwell/api/guest"
	"github.com/josephburnett/gridwell/plugins/gitlab/plugin"
)

func main() { guest.Main(plugin.FromConfig) }
