package files

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A tile's name becomes a file name with nothing a directory would read as
// a path, a hidden file or a control, bounded, and ending in .md.
func TestFileName(t *testing.T) {
	long := strings.Repeat("é", 100) // 200 bytes
	for _, c := range []struct{ in, want string }{
		{"Plan", "Plan.md"},
		{"notes.md", "notes.md"},
		{"NOTES.MD", "NOTES.MD"},
		{"a/b\\c", "abc.md"},
		{"  .hidden  ", "hidden.md"},
		{"..", "untitled.md"},
		{"", "untitled.md"},
		{"\x00\t\n", "untitled.md"},
		{"tab\there", "tabhere.md"},
		{"/", "untitled.md"},
		{long, strings.Repeat("é", 60) + ".md"},
	} {
		if got := fileName(c.in); got != c.want {
			t.Errorf("fileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func createText(f *fixture, dir, name, body string, x, y int64) (*pb.TileResponse, error) {
	return f.s.CreateTile(context.Background(), &pb.CreateTileRequest{GridId: idOf(dir),
		Tile: &pb.Tile{Kind: rpc.KindText, AltText: name, X: x, Y: y, W: 2, H: 3}, Content: []byte(body)})
}

// A text made in a directory is a file holding its body, its row minted at
// the placement asked and stamped from disk, and nothing else is left
// beside it.
func TestATextMadeInADirectoryIsAFile(t *testing.T) {
	f := newFixture(t)
	resp, err := createText(f, f.root, "Plan", "# Plan\n", 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	p := f.path("Plan.md")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	got := f.tile(p)
	if resp.Tile.Id != idOf(p) || got.X != 3 || got.Y != 4 || got.W != 2 || got.H != 3 ||
		got.ContentStamp != contentStamp(fi.ModTime(), fi.Size()) {
		t.Errorf("made %v, listed %v; want Plan.md at 3,4 2x3 with its stamp", resp.Tile, got)
	}
	if n := names(f.grid(f.root)); !slices.Equal(n, []string{"Plan.md"}) {
		t.Errorf("the root lists %v, want Plan.md alone", n)
	}
}

// A file made at the name after it was checked is never overwritten: the
// create is refused and the file stands, alone.
func TestACreateNeverReplacesAFileMadeMeanwhile(t *testing.T) {
	f := newFixture(t)
	was := resolved
	t.Cleanup(func() { resolved = was })
	resolved = func() { f.write("Plan.md", "theirs") }
	_, err := createText(f, f.root, "Plan", "mine", 0, 0)
	wantCode(t, "a create over a file made meanwhile", err, codes.InvalidArgument)
	if b, _ := os.ReadFile(f.path("Plan.md")); string(b) != "theirs" {
		t.Errorf("Plan.md holds %q, want theirs", b)
	}
	if es, _ := os.ReadDir(f.root); len(es) != 1 {
		t.Errorf("the root holds %d names, want Plan.md alone", len(es))
	}
}
