// Package cli implements the subcommand dispatch for the gridwell binary.
// Each subcommand returns the process exit code, so main.go is a thin shell.
package cli

import (
	"fmt"
	"os"
	"strings"
)

// die reports a fatal subcommand error in the one shape every subcommand
// prints one, and answers 1, the code that means the command failed. status
// prints through it and answers 2, because its 1 means "not serving".
func die(cmd string, err error) int {
	fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
	return 1
}

// reorderFlagsFirst groups flag tokens to the front so Go's flag package,
// which stops at the first non-flag, sees them: "serve --static DIR" and
// "serve DIR --static" parse the same. The FlagSet is not consulted, so a
// following token is the value only when takesValue says so, it does not
// start with '-', and the flag was not written "--name=value".
func reorderFlagsFirst(args []string, takesValue func(name string) bool) []string {
	var flagTokens, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) >= 2 && a[0] == '-' {
			flagTokens = append(flagTokens, a)
			if i+1 < len(args) {
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
