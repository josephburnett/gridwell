// Package tmuxtest keeps a test binary's tmux directories out of the real
// TMPDIR. tmux.New keeps its directory after the node stops, because a
// running tmux still reads it, so a test's would stay forever.
package tmuxtest

import (
	"fmt"
	"os"
	"testing"
)

// Main runs the package's tests with TMPDIR at a fresh directory, which it
// removes after. Every package whose tests can start a node calls it from
// TestMain (test/boundary TestEveryNodeTestRedirectsTMPDIR). The directory is
// short because a node's unix sockets sit under it.
func Main(m *testing.M) {
	dir, err := os.MkdirTemp("", "gw")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmuxtest:", err)
		os.Exit(1)
	}
	if err := os.Setenv("TMPDIR", dir); err != nil {
		fmt.Fprintln(os.Stderr, "tmuxtest:", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "tmuxtest:", err)
		code = 1
	}
	os.Exit(code)
}
