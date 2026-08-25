package cli

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/plugin"
)

// Main is the shared CLI dispatch for every composed gridwell binary
// (docs/plugin.md): the stock host passes nil maps (all plugins
// out-of-process); a bundled binary passes its compiled-in loadout —
// plugin factories and/or provider factories — and everything else —
// init, status, backup, the whole serve wiring — is identical. Returns
// the process exit code.
func Main(args []string, factories map[string]plugin.ServerFactory, providers map[string]plugin.ProviderFactory) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "init":
		return RunInit(rest)
	case "serve":
		return RunServeWith(rest, factories, providers)
	case "status":
		return RunStatus(rest)
	case "backup":
		return RunBackup(rest)
	case "clear-browser-data":
		return RunClearBrowserData(rest)
	case "parity":
		return RunParity(rest)
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `gridwell — a spatial information system

Usage:
    gridwell init  --kind KIND --name NAME [--config k=v ...]
                                              create + register a plugin
    gridwell serve [--bind ADDR] [--bind-default ADDR] [--static DIR]
                                              run the backend server
    gridwell status                           report the running server for this
                                              home (exit 0 + banner) or "not
                                              serving" (exit 1)
    gridwell backup DEST                      snapshot every plugin DB + server.yaml
                                              (VACUUM INTO; safe while serving)
    gridwell parity --a URL --b URL [--password PW] [--ignore-fields f,g]
                                              crawl two nodes serving the same
                                              data and diff them (the migration
                                              oracle; exit 0 = parity)
    gridwell clear-browser-data [--user-data DIR]
                                              delete the desktop app's Chromium
                                              session (cookies, storage, caches
                                              of every live url tile); the app
                                              must not be running

init mints a plugin id, creates its DB (at ~/.gridwell/db/<id>/) with identity
metadata, and appends the entry to ~/.gridwell/server.yaml. serve requires that
config file — every plugin's DB path is derived from its id, not configured.`)
}
