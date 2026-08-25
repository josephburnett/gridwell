package fsfile

import (
	"path/filepath"
	"strings"
)

// UnderRoot reports whether path lies within root's subtree (root itself
// included). The ONE confinement predicate for the fs plugin's path
// checks: filepath.Rel, never a hand-built root+"/" prefix — with root
// "/" that prefix is "//", which no path starts with, and the check
// falsely refused everything under a whole-machine root.
func UnderRoot(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
