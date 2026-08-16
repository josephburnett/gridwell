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

	"github.com/josephburnett/gridwell/api/guest"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/shellsvc"
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
	// Verify + open + identity injection are one fused step (issue #196:
	// a store opened without the verified config id silently answers
	// identity reads with the bootstrap mint).
	st, err := localdb.OpenVerified(dbPath, uuid, cfg["kind"])
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: %v\n", err)
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
	// `shell:` in the plugin's server.yaml config picks the login shell for
	// new shell tiles; unset falls back to $SHELL then bash (resolved once
	// inside tmux.New).
	ctrl, tmuxCleanup, err := tmux.New(socket, "", cfg["shell"])
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: tmux init: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = tmuxCleanup() }()

	p := localdb.New(st, shellsvc.NewManager(shellsvc.NewLive(ctrl)))
	// Scratch tiles are ephemeral (deleted on ascent); this sweep is the
	// crash net. Before the orphan sweep, so a swept shell's session reads
	// as orphaned and gets killed there.
	if swept, err := p.CleanupScratch(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: scratch cleanup: %v\n", err)
	} else if swept > 0 {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: scratch cleanup removed %d ephemeral tile(s)\n", swept)
	}
	if killed, err := p.CleanupOrphanedShells(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: orphan cleanup: %v\n", err)
	} else if killed > 0 {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: orphan cleanup killed %d stale shell session(s)\n", killed)
	}

	guest.Serve(p)
}
