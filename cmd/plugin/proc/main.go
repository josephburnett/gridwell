// gridwell-proc is the out-of-process process-table plugin binary. It serves
// the Gridwell gRPC interface over go-plugin's managed subprocess transport.
// The host spawns it and hands it config through GRIDWELL_PLUGIN_CONFIG.
//
// Config keys: db_file (required, the plugin's SQLite DB), pid (the root pid,
// default 1), uuid (durable identity, persisted in the DB).
package main

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/api/guest"
	"github.com/josephburnett/gridwell/api/pluginmeta"
	"github.com/josephburnett/gridwell/internal/plugin/proc"
)

func main() {
	cfg := guest.Config()
	if dbPath := cfg["db_file"]; dbPath != "" {
		if _, err := pluginmeta.Verify(dbPath, cfg["uuid"], cfg["kind"]); err != nil {
			fmt.Fprintf(os.Stderr, "gridwell-proc: %v\n", err)
			os.Exit(1)
		}
	}
	impl, err := proc.NewFactory(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-proc: %v\n", err)
		os.Exit(1)
	}
	guest.Serve(impl)
}
