// Package shelldriver spawns a process attached to a PTY: one Session is one
// PTY is one process. What that process is stays in internal/local/shellsvc.
// shelldriver_nopty.go gives a platform without a PTY a Start that refuses
// with ErrShellsUnavailable and no creack/pty dependency.
package shelldriver

import "errors"

// ErrShellsUnavailable is a state the client is told about: the shell door
// turns it into an exit message, like a node that sets disable_shells.
var ErrShellsUnavailable = errors.New("shell tiles are unavailable on this node: no PTY on this platform")

type Config struct {
	// Cwd empty, or a path that does not exist, falls back through
	// resolveCwd.
	Cwd string
	// Cols and Rows are the initial PTY window size in cells, both > 0.
	Cols, Rows uint16
	// BashPath is the binary to exec. Empty looks up "bash" on $PATH.
	BashPath string
	// Args empty defaults to {"-i"}, so the user's rc files are sourced.
	Args []string
	// Env non-nil replaces the environment; nil uses os.Environ() with TERM
	// defaulted.
	Env []string
}
