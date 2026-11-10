// Package tmuxtest keeps a test binary's tmux directories and tmux server
// sockets out of the real TMPDIR and TMUX_TMPDIR. tmux.New keeps its
// directory after the node stops, because a running tmux still reads it, and
// a tmux server leaves its socket, so a test's would stay forever.
package tmuxtest

import (
	"fmt"
	"os"
	"testing"
)

// Main runs the package's tests with TMPDIR and TMUX_TMPDIR at a fresh
// directory, which it removes after. Every package whose tests can start a
// node calls it from TestMain (test/boundary TestEveryNodeTestRedirectsTMPDIR).
// The directory is short because unix sockets sit under it.
func Main(m *testing.M) {
	dir, err := os.MkdirTemp("", "gw")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmuxtest:", err)
		os.Exit(1)
	}
	for _, name := range []string{"TMPDIR", "TMUX_TMPDIR"} {
		if err := os.Setenv(name, dir); err != nil {
			fmt.Fprintln(os.Stderr, "tmuxtest:", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "tmuxtest:", err)
		code = 1
	}
	os.Exit(code)
}
