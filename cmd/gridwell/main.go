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
	case "open-browser":
		os.Exit(cli.RunOpenBrowser(args))
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
    gridwell init         [--db PATH]                        create the database
    gridwell open-browser [--browser NAME] [--browser-bin PATH] [--profile-dir PATH]
                                                             launch the chosen browser
                                                             headful against the
                                                             gridwell-managed profile,
                                                             so you can sign into sites
                                                             once before running serve.
                                                             Ctrl-C terminates the
                                                             browser.
    gridwell serve        [--db PATH] [--bind ADDR] [--browser NAME] [--browser-bin PATH]
                          [--profile-dir PATH] [--headless]
                                                             run the server`)
}
