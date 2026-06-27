// Package cli implements the subcommand dispatch for the gridwell binary.
//
// Each subcommand returns the process exit code so main.go is a thin shell.
// The commands share a tiny database-path resolution helper and otherwise
// live in their own files.
package cli

import (
	"strings"
)

// reorderFlagsFirst groups all flag tokens to the front of args so that Go's
// stdlib flag package (which stops at the first non-flag) sees them. This
// lets users write either "adduser alice --db PATH" or "adduser --db PATH
// alice"; both produce the same parse.
//
// A "flag token" is anything starting with a single or double dash, plus
// (for non-bool flags) the value that follows. We don't know which flags
// take values without consulting the FlagSet, so we use a small heuristic:
// the next token is considered the value iff it doesn't itself start with
// '-' AND the flag wasn't given as "--name=value".
func reorderFlagsFirst(args []string, takesValue func(name string) bool) []string {
	var flagTokens, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) >= 2 && a[0] == '-' {
			flagTokens = append(flagTokens, a)
			if i+1 < len(args) {
				// Detect "--name=value" form: no separate value follows.
				name := strings.TrimLeft(a, "-")
				if _, _, hasEq := strings.Cut(name, "="); !hasEq {
					if takesValue(name) && args[i+1] != "" && args[i+1][0] != '-' {
						flagTokens = append(flagTokens, args[i+1])
						i++
					}
				}
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flagTokens, positional...)
}
