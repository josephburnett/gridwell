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
	"strconv"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/cli"
	"github.com/josephburnett/gridwell/internal/plugin"
	fsplugin "github.com/josephburnett/gridwell/plugins/fs/plugin"
	gitlabplugin "github.com/josephburnett/gridwell/plugins/gitlab/plugin"
	procplugin "github.com/josephburnett/gridwell/plugins/proc/plugin"
)

func main() { os.Exit(cli.Main(os.Args[1:], pluginFactories())) }

// pluginFactories is this binary's loadout: each kind constructed
// exactly as its subprocess main (cmd/gridwell-plugin-*) would, minus
// the process boundary. The native store (kind local) and the builtin
// transport (kind remote) are NODE code since the v2 fold — the serve
// wiring supplies them; only real providers are enumerated here.
func pluginFactories() map[string]plugin.Factory {
	return map[string]plugin.Factory{
		"fs": func(cfg map[string]string) (pluginv1.PluginServer, error) {
			return fsplugin.New(cfg["root"], nil), nil
		},
		"proc": func(cfg map[string]string) (pluginv1.PluginServer, error) {
			pid, _ := strconv.ParseInt(cfg["pid"], 10, 64)
			return procplugin.New("", pid, nil), nil
		},
		"gitlab": func(cfg map[string]string) (pluginv1.PluginServer, error) {
			return gitlabplugin.FromConfig(cfg), nil
		},
	}
}
