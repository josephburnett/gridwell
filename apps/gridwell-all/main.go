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

	cpv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
	"github.com/josephburnett/gridwell/internal/cli"
	"github.com/josephburnett/gridwell/internal/plugin"
	fsprovider "github.com/josephburnett/gridwell/plugins/fs/provider"
	gitlabprovider "github.com/josephburnett/gridwell/plugins/gitlab/provider"
	procprovider "github.com/josephburnett/gridwell/plugins/proc/provider"
)

func main() { os.Exit(cli.Main(os.Args[1:], providerFactories())) }

// providerFactories is this binary's loadout: each kind constructed
// exactly as its subprocess main (cmd/gridwell-provider-*) would, minus
// the process boundary. The native store (kind local) and the builtin
// transport (kind remote) are NODE code since the v2 fold — the serve
// wiring supplies them; only real providers are enumerated here.
func providerFactories() map[string]plugin.ProviderFactory {
	return map[string]plugin.ProviderFactory{
		"fs": func(cfg map[string]string) (cpv1.ContentProviderServer, error) {
			return fsprovider.New(cfg["root"], nil), nil
		},
		"proc": func(cfg map[string]string) (cpv1.ContentProviderServer, error) {
			pid, _ := strconv.ParseInt(cfg["pid"], 10, 64)
			return procprovider.New("", pid, nil), nil
		},
		"gitlab": func(cfg map[string]string) (cpv1.ContentProviderServer, error) {
			return gitlabprovider.FromConfig(cfg), nil
		},
	}
}
