// gridwell-localdb is the out-of-process Gridwell-DB plugin binary — the "main"
// plugin that owns the wells, text, url, and shell tiles you create, and the
// live shell PTYs (OpenShell). It serves the Gridwell gRPC interface over
// go-plugin's managed subprocess transport; the host spawns it and hands it
// config through GRIDWELL_PLUGIN_CONFIG.
//
// Config keys: db_file (required, the Gridwell SQLite DB), uuid (durable
// identity, persisted in the DB and used to name the private tmux socket so
// shell sessions survive a plugin restart).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/plugin/guest"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/plugin/pluginmeta"
	"github.com/josephburnett/gridwell/internal/shellsvc"
	"github.com/josephburnett/gridwell/internal/store"
	"github.com/josephburnett/gridwell/internal/tmux"
)

func main() {
	cfg := guest.Config()
	dbPath := cfg["db_file"]
	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "gridwell-localdb: db_file config key required")
		os.Exit(1)
	}
	uuid := cfg["uuid"]
	if _, err := pluginmeta.Ensure(dbPath, uuid); err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: %v\n", err)
		os.Exit(1)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: open db: %v\n", err)
		os.Exit(1)
	}

	// The private tmux server backs every shell tile in this DB. The socket name
	// is derived from the uuid so it is stable across restarts (sessions live in
	// the tmux server, which outlives this process), and distinct per localdb so
	// two DBs' shells never collide.
	socket := "gridwell"
	if uuid != "" {
		socket = "gridwell-" + uuid
	}
	ctrl, tmuxCleanup, err := tmux.New(socket, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: tmux init: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = tmuxCleanup() }()

	p := localdb.New(st, shellsvc.NewManager(shellsvc.NewLive(ctrl)))
	if killed, err := p.CleanupOrphanedShells(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: orphan cleanup: %v\n", err)
	} else if killed > 0 {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: orphan cleanup killed %d stale shell session(s)\n", killed)
	}

	guest.Serve(p)
}
