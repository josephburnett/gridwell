// gridwell-fs is the out-of-process filesystem plugin binary. It serves the
// Gridwell gRPC interface over go-plugin's managed subprocess transport.
// The host (gridwell serve) spawns this binary and connects via the
// HandshakeConfig in internal/plugin/shared.go.
//
// Config keys (passed via Attach):
//   path: absolute path to the root directory to project
package main

import (
	"fmt"
	"os"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/fs"
	"github.com/josephburnett/gridwell/internal/plugin/guest"
)

// Factory satisfies plugin.ServerFactory for the fs kind. It is used by the
// host's LoadAll when binary="" is set (in-process mode). For subprocess mode,
// the guest.Serve path below is used.
func Factory(cfg *config.PluginConfig) (gridwellv1.GridwellServer, error) {
	dbPath := cfg.Config["db_file"]
	if dbPath == "" {
		return nil, fmt.Errorf("fs plugin: db_file config key required")
	}
	p, err := fs.Open(dbPath, nil)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func main() {
	dbPath := os.Getenv("GRIDWELL_FS_DB")
	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "gridwell-fs: GRIDWELL_FS_DB env var required")
		os.Exit(1)
	}
	impl, err := fs.Open(dbPath, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-fs: open db: %v\n", err)
		os.Exit(1)
	}
	// Register the plugin UUIDs stored via go-plugin handshake.
	// The binary is long-running; go-plugin manages its lifecycle.
	_ = plugin.HandshakeConfig // used by guest.Serve indirectly
	guest.Serve(impl)
}
