// gridwell-proc is the out-of-process process-table plugin binary. It serves
// the Gridwell gRPC interface over go-plugin's managed subprocess transport.
//
// Config keys (passed via Attach):
//   pid: root PID (default "1")
package main

import (
	"fmt"
	"os"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/guest"
	"github.com/josephburnett/gridwell/internal/plugin/proc"
)

// Factory satisfies plugin.ServerFactory for the proc kind (in-process mode).
func Factory(cfg *config.PluginConfig) (gridwellv1.GridwellServer, error) {
	dbPath := cfg.Config["db_file"]
	if dbPath == "" {
		return nil, fmt.Errorf("proc plugin: db_file config key required")
	}
	p, err := proc.Open(dbPath, "", nil)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func main() {
	dbPath := os.Getenv("GRIDWELL_PROC_DB")
	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "gridwell-proc: GRIDWELL_PROC_DB env var required")
		os.Exit(1)
	}
	impl, err := proc.Open(dbPath, "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-proc: open db: %v\n", err)
		os.Exit(1)
	}
	_ = plugin.HandshakeConfig
	guest.Serve(impl)
}
