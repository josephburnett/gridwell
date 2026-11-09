package cli

import (
	"fmt"
	"os"
)

// Main dispatches a subcommand and returns the process exit code.
func Main(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "serve":
		return RunServe(rest)
	case "status":
		return RunStatus(rest)
	case "backup":
		return RunBackup(rest)
	case "size":
		return RunSize(rest)
	case "clean":
		return RunClean(rest)
	case "clear-browser-data":
		return RunClearBrowserData(rest)
	case "version", "--version", "-v":
		fmt.Println(VersionString())
		return 0
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
                                              fresh node: the id is minted)
    gridwell status                           report the running server for this
                                              ~/.gridwell (exit 0 + banner) or "not
                                              serving" (exit 1)
    gridwell backup DEST                      snapshot every DB + server.yaml
                                              (VACUUM INTO; safe while serving)
    gridwell size [--json]                    report what gridwell.db holds: tables,
                                              tiles by kind and place, blobs, the
                                              trash, and what nothing reaches
                                              (read-only; safe while serving)
    gridwell clean [--yes]                    remove what nothing reaches (never
                                              the trash or a tombstone), then
                                              VACUUM; refuses while serving, and
                                              without --yes only reports
    gridwell clear-browser-data [--user-data DIR]
                                              delete the desktop app's browser
                                              cookies, storage and caches (every
                                              live url tile's); the app
                                              must not be running
    gridwell version                          print the release version this
                                              binary was built from, or "dev"

server.yaml names the node's id, its connections and its files' roots;
every DB path is derived from an id, never configured.`)
}
