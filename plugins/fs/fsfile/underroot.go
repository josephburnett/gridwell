package fsfile

import (
	"path/filepath"
	"strings"
)

// UnderRoot reports whether path lies within root's subtree, root itself
// included. It is the one confinement predicate for the fs plugin's path
// checks, and it uses filepath.Rel rather than a hand-built root+"/" prefix:
// with root "/" that prefix is "//", which no path starts with, so the check
// would refuse everything under a whole-machine root.
func UnderRoot(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
