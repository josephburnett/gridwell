// gridwell-all — the BUNDLED example binary (docs/plugin.md): the same
// server, the same CLI, with the included content providers COMPILED IN
// through the compose door. It exists to prove the door's contract — the
// e2e parity gate runs the same suite against gridwell (all external)
// and this binary (all in-process) and must see identical behavior — and
// as the template for composing your own binary: import your providers,
// hand Main their factories, done. Enumeration of what ships is a
// LEAF-BINARY privilege; this is one of the two leaves (mobile is the
// other).
package main

import (
	"os"

	"github.com/josephburnett/gridwell/internal/cli"
	"github.com/josephburnett/gridwell/internal/plugin"
	fsplugin "github.com/josephburnett/gridwell/plugins/fs/plugin"
	gitlabplugin "github.com/josephburnett/gridwell/plugins/gitlab/plugin"
	procplugin "github.com/josephburnett/gridwell/plugins/proc/plugin"
)

func main() { os.Exit(cli.Main(os.Args[1:], pluginFactories())) }

// pluginFactories is this binary's loadout: each kind's FromConfig —
// THE SAME function its subprocess main hands guest.Main, so the two
// doors cannot derive a plugin differently. The native store (kind
// local) and the builtin transport (kind remote) are NODE code since
// the v2 fold — the serve wiring supplies them; only real plugins are
// enumerated here.
func pluginFactories() map[string]plugin.Factory {
	return map[string]plugin.Factory{
		"fs":     fsplugin.FromConfig,
		"proc":   procplugin.FromConfig,
		"gitlab": gitlabplugin.FromConfig,
	}
}
