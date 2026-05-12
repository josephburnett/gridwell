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
	case "adduser":
		os.Exit(cli.RunAddUser(args))
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
    gridwell init    [--db PATH]                       create the database and schema
    gridwell adduser USERNAME [--db PATH]              create a user (interactive password)
    gridwell serve   [--db PATH] [--addr ADDR]         run the server`)
}
