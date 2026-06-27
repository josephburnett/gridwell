// gridwell-fs is the out-of-process filesystem plugin binary. It serves the
// Gridwell gRPC interface over go-plugin's managed subprocess transport. The
// host (gridwell serve) spawns it and connects via the HandshakeConfig in
// internal/plugin/shared.go, handing it config through GRIDWELL_PLUGIN_CONFIG.
//
// Config keys: db_file (required, the plugin's SQLite DB), root (the directory
// projected as the default root), uuid (durable identity, persisted in the DB).
package main

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin/fs"
	"github.com/josephburnett/gridwell/internal/plugin/guest"
	"github.com/josephburnett/gridwell/internal/plugin/pluginmeta"
)

func main() {
	cfg := guest.Config()
	if dbPath := cfg["db_file"]; dbPath != "" {
		if _, err := pluginmeta.Ensure(dbPath, cfg["uuid"], cfg["kind"]); err != nil {
			fmt.Fprintf(os.Stderr, "gridwell-fs: %v\n", err)
			os.Exit(1)
		}
	}
	impl, err := fs.NewFactory(&config.PluginConfig{Name: "fs", Config: cfg})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gridwell-fs: %v\n", err)
		os.Exit(1)
	}
	guest.Serve(impl)
}
