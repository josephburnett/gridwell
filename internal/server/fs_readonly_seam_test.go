package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/client/textedit"
)

// A body the source already knows it would refuse is never offered for
// editing: fs says so per entry (Entry.read_only), the node carries it on the
// tile, and the client's one predicate reads it beside the grid's writable.
func TestFsBodyItWouldRefuseIsReadOnlyOnTheTile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a 0444 file")
	}
	root := t.TempDir()
	write := func(name string, data []byte, perm os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), data, perm); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.md", []byte("# mine\n"), 0o644)
	write("locked.md", []byte("# theirs\n"), 0o444)
	write("data.bin", []byte{0, 1, 2, 3}, 0o644)
	write("big.txt", []byte(strings.Repeat("x", 4<<20+1)), 0o644)

	k := newContentClient(t, "pfsro", root)
	k.fetchGrid(k.landing)
	g, _ := k.c.Grid(k.landing)
	cases := []struct {
		name string
		want string // a word the reason must hold, "" for a body that takes edits
	}{
		{"notes.md", ""},
		{"locked.md", "permission"},
		{"data.bin", "summary"},
		{"big.txt", "larger"},
	}
	for _, c := range cases {
		row := k.tile(c.name)
		if got := textedit.ReadOnly(row, g.Meta.GetWritable()); got != (c.want != "") {
			t.Errorf("%s: read-only %v (read_only %q), want %v", c.name, got, row.ReadOnly, c.want != "")
		}
		if !strings.Contains(row.ReadOnly, c.want) {
			t.Errorf("%s: read_only %q does not say %q", c.name, row.ReadOnly, c.want)
		}
	}

	// A file made read-only while it is shown reads so with no gesture.
	if err := k.cl.SetInterest(k.ctx, []string{k.landing}); err != nil {
		t.Fatal(err)
	}
	k.settle()
	if err := os.Chmod(filepath.Join(root, "notes.md"), 0o444); err != nil {
		t.Fatal(err)
	}
	if !k.run(5*time.Second, func() bool { return k.tile("notes.md").ReadOnly != "" }) {
		t.Error("notes.md made 0444 while shown still takes edits")
	}
}
