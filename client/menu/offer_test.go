package menu

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

func TestOffer(t *testing.T) {
	cases := []struct {
		name                    string
		in                      Offer
		primitives, shell, disk bool
	}{
		{"writable node with shells", Offer{AcceptsTiles: true}, true, true, false},
		{"writable node, shells disabled", Offer{AcceptsTiles: true, ShellsDisabled: true}, true, false, false},
		{"read-only grid offers nothing, shells or not", Offer{}, false, false, false},
		{"read-only grid on a shell-less node", Offer{ShellsDisabled: true}, false, false, false},
		// A directory is a place for Gridwell tiles (2026-11-08): every
		// primitive, and a file and a directory while the disk is writable.
		{"a writable directory", Offer{AcceptsTiles: true, Directory: true, Writable: true}, true, true, true},
		{"a read-only directory takes Gridwell tiles, not files", Offer{AcceptsTiles: true, Directory: true}, true, true, false},
		{"a writable home grid makes no file", Offer{AcceptsTiles: true, Writable: true}, true, true, false},
	}
	for _, c := range cases {
		if got := c.in.Primitives(); got != c.primitives {
			t.Errorf("%s: Primitives = %v, want %v", c.name, got, c.primitives)
		}
		if got := c.in.Shell(); got != c.shell {
			t.Errorf("%s: Shell = %v, want %v", c.name, got, c.shell)
		}
		if got := c.in.Disk(); got != c.disk {
			t.Errorf("%s: Disk = %v, want %v", c.name, got, c.disk)
		}
	}
}

func TestDiskCreate(t *testing.T) {
	const dir = "n/~L3I"
	if _, ok := DiskCreate(dir, rpc.KindText, "  ", 1, 2); ok {
		t.Error("an empty name created something")
	}
	req, ok := DiskCreate(dir, rpc.KindText, " notes.txt ", 1, 2)
	if !ok || !req.File || req.GridId != dir || req.Tile.Kind != rpc.KindText || req.Tile.AltText != "notes.txt" ||
		req.Tile.X != 1 || req.Tile.Y != 2 || req.Tile.W != 1 || req.Tile.H != 1 {
		t.Errorf("a file = %v, %v", req, ok)
	}
	req, ok = DiskCreate(dir, rpc.KindDoor, "sub", 3, 4)
	if !ok || !req.File || req.Tile.Kind != rpc.KindDoor || req.Tile.AltText != "sub" {
		t.Errorf("a directory = %v, %v", req, ok)
	}
}
