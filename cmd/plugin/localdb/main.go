// gridwell-localdb is the out-of-process Gridwell-DB plugin binary — the "main"
// plugin that owns the wells, text, url, and shell tiles you create. It serves
// the Gridwell gRPC interface over go-plugin's managed subprocess transport;
// the host spawns it and hands it config through GRIDWELL_PLUGIN_CONFIG.
//
// Config keys: db_file (required, the Gridwell SQLite DB), uuid (durable
// identity, persisted in the DB).
package main

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/plugin/guest"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/plugin/pluginmeta"
	"github.com/josephburnett/gridwell/internal/store"
)

func main() {
	cfg := guest.Config()
	dbPath := cfg["db_file"]
	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "gridwell-localdb: db_file config key required")
		os.Exit(1)
	}
	if _, err := pluginmeta.Ensure(dbPath, cfg["uuid"]); err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: %v\n", err)
		os.Exit(1)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-localdb: open db: %v\n", err)
		os.Exit(1)
	}
	// sess is nil: shell PTYs are still served by the host's tmux controller
	// (keyed by qualified tile id). Stage 3 moves OpenShell into this plugin.
	guest.Serve(localdb.New(st, nil))
}
