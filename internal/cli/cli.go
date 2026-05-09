// Package cli implements the subcommand dispatch for the ascent binary.
//
// Each subcommand returns the process exit code so main.go is a thin shell.
// The commands share a tiny database-path resolution helper and otherwise
// live in their own files.
package cli

import (
	"flag"
	"fmt"
	"io"
)

// resolveDB returns the database path. Default is "./ascent.db" — convenient
// for development; production users override via --db.
func resolveDB(fs *flag.FlagSet, defaultPath string) *string {
	return fs.String("db", defaultPath, "path to the SQLite database file")
}

// printErr writes to w. Centralized so tests can capture output.
func printErr(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, format, args...)
	fmt.Fprintln(w)
}
