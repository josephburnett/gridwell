package server_test

import (
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A shown directory that goes away is told on its own grid, with or without
// files in it, and its grid then answers what a gone directory takes: a
// directory holding only Gridwell tiles lists no name before or after, so
// only its grid's facts say it moved.
func TestAShownDirectoryThatGoesIsTold(t *testing.T) {
	for _, withFile := range []bool{false, true} {
		root := t.TempDir()
		k := filepath.Join(root, "k")
		if err := os.Mkdir(k, 0o755); err != nil {
			t.Fatal(err)
		}
		if withFile {
			writeFile(t, filepath.Join(k, "f.txt"), "f")
		}
		n := newFilesNode(t, root)
		n.overText(fileID(k), 2, 2, "kept")
		events := n.show(fileID(k))
		if !n.grid(fileID(k)).Grid.Writable {
			t.Fatalf("withFile %v: k is not writable before it goes", withFile)
		}
		if err := os.RemoveAll(k); err != nil {
			t.Fatal(err)
		}
		await(t, events, "k's change", func(ev *gridwellv1.Event) bool {
			return ev.GetGridChanged().GetGridId() == fileID(k)
		})
		if g := n.grid(fileID(k)).Grid; g.Writable || g.AcceptsFiles {
			t.Errorf("withFile %v: the gone k still answers writable=%v accepts_files=%v", withFile, g.Writable, g.AcceptsFiles)
		}
	}
}
