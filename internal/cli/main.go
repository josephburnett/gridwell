package cli

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/plugin"
)

// Main is the shared CLI dispatch for every composed gridwell binary
// (docs/plugin.md): the stock host passes nil (every plugin
// out-of-process); a bundled binary passes its compiled-in plugin
// factories — and everything else — status, backup, the whole serve
// wiring — is identical. Returns the process exit code.
func Main(args []string, plugins map[string]plugin.Factory) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "serve":
		return RunServeWith(rest, plugins)
	case "status":
		return RunStatus(rest)
	case "backup":
		return RunBackup(rest)
	case "clear-browser-data":
		return RunClearBrowserData(rest)
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
    gridwell serve [--bind ADDR] [--bind-default ADDR] [--static DIR]
                                              run the node (a missing
                                              ~/.gridwell/server.yaml is a
                                              fresh home: the id is minted)
    gridwell status                           report the running server for this
                                              home (exit 0 + banner) or "not
                                              serving" (exit 1)
    gridwell backup DEST                      snapshot every DB + server.yaml
                                              (VACUUM INTO; safe while serving)
    gridwell clear-browser-data [--user-data DIR]
                                              delete the desktop app's Chromium
                                              session (cookies, storage, caches
                                              of every live url tile); the app
                                              must not be running

server.yaml names the node's id, its connections and its content plugins
(docs/one-node.md); every DB path is derived from an id, never configured.`)
}
