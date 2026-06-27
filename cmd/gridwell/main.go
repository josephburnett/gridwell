package main

import (
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "init":
		os.Exit(cli.RunInit(args))
	case "serve":
		os.Exit(cli.RunServe(args))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `gridwell — a spatial information system

Usage:
    gridwell init  --kind KIND --name NAME [--config k=v ...]
                                              create + register a plugin
    gridwell serve [--bind ADDR] [--static DIR]
                                              run the backend server

init mints a plugin id, creates its DB (at ~/.gridwell/db/<id>/) with identity
metadata, and appends the entry to ~/.gridwell/server.yaml. serve requires that
config file — every plugin's DB path is derived from its id, not configured.

The server is the loopback backend for the Gridwell desktop app (see
apps/desktop). It serves the RPC/SSE data plane, the wasm client, and shell
PTYs; live URL tiles are hosted natively by the Electron shell.`)
}
