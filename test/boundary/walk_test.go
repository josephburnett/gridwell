package boundary

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// pruned reports whether a walker skips the directory at path. Every walker
// here reads the one tree its root names, so three things are never entered:
// the git store, dependencies, and a nested checkout. A nested checkout
// carries a .git entry of its own, a file for a worktree, and everything under
// it is a second copy of the tree already being walked; the agent worktrees
// under .claude/worktrees are how make check in the shared checkout came to
// fail on files no commit on main held.
func pruned(root, path string, d fs.DirEntry) bool {
	if !d.IsDir() {
		return false
	}
	switch d.Name() {
	case ".git", "node_modules", "vendor":
		return true
	}
	if path == root {
		return false
	}
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}

// A directory that is its own checkout is not this tree, wherever it sits.
func TestWalkersPruneNestedCheckouts(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{".git", "internal/server", ".claude/worktrees/agent-x/internal/server"} {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A worktree's .git is a file naming the main store, not a directory.
	if err := os.WriteFile(filepath.Join(root, ".claude/worktrees/agent-x/.git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := func(rel string) fs.DirEntry {
		d, err := os.Lstat(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return fs.FileInfoToDirEntry(d)
	}
	for _, rel := range []string{".git", ".claude/worktrees/agent-x"} {
		if !pruned(root, filepath.Join(root, rel), entry(rel)) {
			t.Errorf("%s is not pruned; a walker would read a second copy of the tree", rel)
		}
	}
	for _, rel := range []string{".claude", "internal/server"} {
		if pruned(root, filepath.Join(root, rel), entry(rel)) {
			t.Errorf("%s is pruned; a walker would miss part of this tree", rel)
		}
	}
	if pruned(root, root, entry(".")) {
		t.Error("the root itself is pruned")
	}
}
