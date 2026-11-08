package server_test

// Ids that name no file the files hold, through the router: each is dead,
// never an error, and says nothing about the files' health.

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A path's id has one spelling: the same path spelled with a newline inside
// its base64 names nothing.
func TestFilesAnotherSpellingOfAPathIsDead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/notes.md", "# notes\n")
	n := newFilesNode(t, root)
	seg := rpc.KeyTileID(root + "/notes.md")
	other := rpc.QualifyID(localNodeID, seg[:4]+"\n"+seg[4:])
	if _, err := n.cl.GetTile(n.ctx, other); !gwerr.IsDeadLink(err) {
		t.Fatalf("GetTile(%q) = %v, want dead", strings.ReplaceAll(other, "\n", `\n`), err)
	}
}

// An id whose path the OS cannot name, a NUL inside it, or one that is not an
// absolute path at all, names nothing: dead, never an error, whichever verb
// asks.
func TestFilesAnIdNamingNoPathIsDead(t *testing.T) {
	root := t.TempDir()
	n := newFilesNode(t, root)
	for _, path := range []string{root + "/a\x00b", "notes.md", ""} {
		id := fileID(path)
		if _, err := n.cl.GetGrid(n.ctx, id); !gwerr.IsDeadLink(err) {
			t.Errorf("GetGrid(%q) = %v, want dead", path, err)
		}
		if _, err := n.cl.GetTile(n.ctx, id); !gwerr.IsDeadLink(err) {
			t.Errorf("GetTile(%q) = %v, want dead", path, err)
		}
	}
}
