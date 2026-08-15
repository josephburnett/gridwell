package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/node"
)

// kvFlag collects repeated `--config key=value` options into a config map.
type kvFlag map[string]string

func (m kvFlag) String() string { return "" }

func (m kvFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("expected key=value, got %q", s)
	}
	m[k] = v
	return nil
}

// RunInit creates and registers a new plugin in one coordinated step: it mints
// a routing id, creates the plugin's DB directory and DB (writing the gridwell
// marker + id + kind metadata that the server strictly verifies on every
// start), then appends the matching entry to <home>/server.yaml. The DB path is
// derived from the id (<home>/db/<id>/store.db) — it is never specified by the
// user. Name is the plugin's node-grid label; id and kind are the durable identity.
//
//	gridwell init --kind <kind> --name <name> [--config k=v ...]
func RunInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	kind := fs.String("kind", "", "plugin kind (localdb, fs, proc, ssh)")
	name := fs.String("name", "", "display name shown in the launcher")
	conf := kvFlag{}
	fs.Var(conf, "config", "plugin config as key=value (repeatable)")
	args = reorderFlagsFirst(args, func(n string) bool {
		switch n {
		case "kind", "name", "config":
			return true
		}
		return false
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *kind == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "init: --kind and --name are required")
		return 2
	}
	// Connections are DATA, never config (#199, #251): an ssh plugin takes
	// no connection keys — machines are added from inside Gridwell (drag
	// the ssh plugin onto a grid, fill in the picker). Refuse the retired
	// config-pinned shape up front; old server.yaml entries that predate
	// this are migrated automatically by `gridwell serve`.
	if *kind == "ssh" && sshConfigPinned(conf) {
		fmt.Fprintln(os.Stderr, "init: ssh connection config keys are retired (#251) — connections are data: register the plugin with no config, then add machines from inside Gridwell")
		return 2
	}

	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		return 1
	}

	// The one init door (node.InitPlugin — shared with the mobile bind's
	// first-run auto-init): mint the durable id (7-char base36, leading
	// letter, since 2026-07-25; earlier 32-hex ids stay valid forever),
	// create the identity-stamped DB, append the config entry, ensure the
	// node id.
	id, err := node.InitPlugin(home, *kind, *name, map[string]string(conf))
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		return 1
	}

	fmt.Printf("gridwell: initialized %s plugin %q (id %s)\n  db:     %s\n  config: %s\n",
		*kind, *name, id, config.DBFile(home, id), filepath.Join(home, "server.yaml"))
	return 0
}

// sshConfigPinned reports whether an ssh plugin's config carries any of the
// retired connection keys (the pre-#199 config-pinned shape, removed with
// #251). Any key present is refused — a PARTIAL set must not silently
// strand typo'd keys either.
func sshConfigPinned(conf map[string]string) bool {
	for _, k := range sshConnKeys {
		if conf[k] != "" {
			return true
		}
	}
	return false
}
