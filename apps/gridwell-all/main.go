// gridwell-all — the BUNDLED example binary (docs/plugin.md): the same
// server, the same CLI, with the four included plugins COMPILED IN
// through the compose door. It exists to prove the door's contract — the
// e2e parity gate runs the same suite against gridwell (all external)
// and this binary (all in-process) and must see identical behavior — and
// as the template for composing your own binary: import your plugins,
// hand Main their factories, done. Enumeration of what ships is a
// LEAF-BINARY privilege; this is one of the two leaves (mobile is the
// other).
package main

import (
	"os"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/cli"
	"github.com/josephburnett/gridwell/internal/plugin"
	fsplugin "github.com/josephburnett/gridwell/plugins/fs"
	"github.com/josephburnett/gridwell/plugins/local"
	"github.com/josephburnett/gridwell/plugins/proc"
	"github.com/josephburnett/gridwell/plugins/remote"
	"github.com/josephburnett/gridwell/plugins/remote/dial"
)

func main() { os.Exit(cli.Main(os.Args[1:], factories())) }

// factories is this binary's loadout: each kind constructed exactly as
// its subprocess main would, minus the process boundary. Shells work
// in-process too (the tmux manager is not wired here — a bundled desktop
// binary wanting live shells would add it exactly as
// plugins/local/cmd/gridwell-plugin-local does).
func factories() map[string]plugin.ServerFactory {
	return map[string]plugin.ServerFactory{
		"local": func(cfg map[string]string) (gridwellv1.GridwellServer, error) {
			st, err := local.OpenVerified(cfg["db_file"], cfg["uuid"], cfg["kind"])
			if err != nil {
				return nil, err
			}
			return local.New(st, nil), nil
		},
		"fs":   fsplugin.NewFactory,
		"proc": proc.NewFactory,
		"remote": func(cfg map[string]string) (gridwellv1.GridwellServer, error) {
			db, err := remote.OpenDB(cfg["db_file"])
			if err != nil {
				return nil, err
			}
			home := os.Getenv("GRIDWELL_HOME")
			return remote.New(db, dial.Dial, home), nil
		},
	}
}
