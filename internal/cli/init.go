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

// RunInit registers a new entry in one coordinated step: it mints a
// routing id, creates the DB directory (and, for a native kind, the DB
// with the gridwell marker + id + kind metadata the server strictly
// verifies on every start), then appends the matching entry to
// <home>/server.yaml. The DB path is derived from the id — never
// specified by the user. Name is the launcher label; id and kind are the
// durable identity.
//
//	gridwell init --kind <kind> --name <name> [--config k=v ...]
func RunInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	kind := fs.String("kind", "", "kind: a native kind (local, remote) or a content provider (fs, proc, gitlab, …)")
	name := fs.String("name", "", "display name shown in the launcher (default: the kind)")
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
	if *kind == "" {
		fmt.Fprintln(os.Stderr, "init: --kind is required")
		return 2
	}
	// The name is a display alias; unset defaults to the kind — "fs is
	// called fs" is the right default for a single instance of anything.
	if *name == "" {
		*name = *kind
	}
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		return 1
	}

	// The one init door (node.Init — shared with the mobile bind's
	// first-run auto-init): mint the durable id (7-char base36, leading
	// letter, since 2026-07-25; earlier 32-hex ids stay valid forever),
	// create a native kind's identity-stamped DB, append the config
	// entry, ensure the node id.
	id, err := node.Init(home, *kind, *name, map[string]string(conf))
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		return 1
	}

	fmt.Printf("gridwell: initialized %s plugin %q (id %s)\n  db:     %s\n  config: %s\n",
		*kind, *name, id, config.DBFile(home, id), filepath.Join(home, "server.yaml"))
	return 0
}
