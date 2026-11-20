package server_test

import (
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/textedit"
)

// A directory takes a new file or directory only while this user may make
// a name in it (CLAUDE.md, a directory is a place for Gridwell tiles): its
// grid says so as accepts_files, a chmod of it while shown is told, and
// neither moves what it takes of Gridwell, whose tiles are rows.
func TestADirectoryAcceptsFilesWhileItsModeLets(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root makes a name in any directory")
	}
	root := t.TempDir()
	open, shut := filepath.Join(root, "open"), filepath.Join(root, "shut")
	writeFile(t, filepath.Join(open, "a.md"), "a")
	writeFile(t, filepath.Join(shut, "b.md"), "b")
	n := newFilesNode(t, root)
	kept := n.overText(fileID(shut), 3, 3, "kept")
	chmod(t, shut, 0o555)

	for _, c := range []struct {
		dir  string
		want bool
	}{{open, true}, {shut, false}} {
		g := n.grid(fileID(c.dir))
		if g.Grid.AcceptsFiles != c.want || !g.Grid.GetAcceptsTiles() || !g.Grid.Writable {
			t.Errorf("%s: accepts_files=%v accepts_tiles=%v writable=%v, want accepts_files %v, Gridwell tiles and edits taken",
				filepath.Base(c.dir), g.Grid.AcceptsFiles, g.Grid.GetAcceptsTiles(), g.Grid.Writable, c.want)
		}
		if c.dir == shut {
			if tl := listed(g, kept.Id); tl == nil || textedit.ReadOnly(tl, g.Grid.Writable) {
				t.Errorf("the Gridwell text over the shut directory reads %v, want it editable", tl)
			}
		}
	}

	events := n.show(fileID(shut))
	chmod(t, shut, 0o755)
	await(t, events, "the opened directory's change", func(ev *gridwellv1.Event) bool {
		return ev.GetGridChanged().GetGridId() == fileID(shut)
	})
	if !n.grid(fileID(shut)).Grid.AcceptsFiles {
		t.Error("the opened directory does not accept files")
	}
	chmod(t, shut, 0o555)
	await(t, events, "the shut directory's change", func(ev *gridwellv1.Event) bool {
		return ev.GetGridChanged().GetGridId() == fileID(shut)
	})
	if n.grid(fileID(shut)).Grid.AcceptsFiles {
		t.Error("the shut directory still accepts files")
	}
}

// chmod sets dir's mode, and gives it back its write bit at cleanup so the
// temp directory can be removed.
func chmod(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}
