package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func TestCreateFileWellHappyPath(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path:   rpc.Path{},
		GridID: root, X: 0, Y: 0, W: 2, H: 2,
		FSPath: "/etc",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.Kind != rpc.KindFileWell {
		t.Errorf("kind=%q, want file-well", w.Kind)
	}
	if w.FSPath != "/etc" {
		t.Errorf("fs_path=%q, want /etc", w.FSPath)
	}
	if w.ChildGridID == "" {
		t.Error("no child grid")
	}
	g, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if g.Grid.SourceKind != rpc.GridSourceFS {
		t.Errorf("child grid source_kind=%q, want fs", g.Grid.SourceKind)
	}
	if g.Grid.SourceID != "/etc" {
		t.Errorf("child grid source_id=%q, want /etc", g.Grid.SourceID)
	}
}

// Two file-wells at the same canonical path must share one backing grid:
// FS identity is the path, not the tile.
func TestCreateFileWellSharesGridByPath(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	a, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: "/etc",
	})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	// Use the messy form to also cover path normalization.
	b, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 2, Y: 0, W: 1, H: 1, FSPath: "/etc/./",
	})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if a.ChildGridID == "" || a.ChildGridID != b.ChildGridID {
		t.Errorf("expected shared child grid, got a=%s b=%s", a.ChildGridID, b.ChildGridID)
	}
	if b.FSPath != "/etc" {
		t.Errorf("path not canonicalized: %q", b.FSPath)
	}
}

func TestCreateFileWellRejectsRelative(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	_, err := s.CreateFileWell(context.Background(), &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: "etc",
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("err=%v, want ErrInvalidArgument", err)
	}
}

func TestCreateProcessWellHappyPath(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	w, err := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, PID: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.Kind != rpc.KindProcessWell {
		t.Errorf("kind=%q, want process-well", w.Kind)
	}
	if w.PID != 1 {
		t.Errorf("pid=%d, want 1", w.PID)
	}
	g, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if g.Grid.SourceKind != rpc.GridSourceProc {
		t.Errorf("child grid source_kind=%q, want proc", g.Grid.SourceKind)
	}
	if g.Grid.SourceID != "1" {
		t.Errorf("child grid source_id=%q, want 1", g.Grid.SourceID)
	}
}

func TestCreateProcessWellSharesGridByPID(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	a, err := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, PID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 2, Y: 0, W: 1, H: 1, PID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.ChildGridID != b.ChildGridID {
		t.Errorf("expected shared child grid")
	}
}

func TestCreateProcessWellRejectsZeroPID(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	_, err := s.CreateProcessWell(context.Background(), &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, PID: 0,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("err=%v, want ErrInvalidArgument", err)
	}
}

// canonicalFSPath is a small pure function — table tests cover its
// edges without touching the DB.
func TestCanonicalFSPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"/", "/", false},
		{"/foo", "/foo", false},
		{"/foo/", "/foo", false},
		{"/foo/./bar", "/foo/bar", false},
		{"/foo//bar", "/foo/bar", false},
		{"  /foo  ", "/foo", false},
		{"", "", true},
		{"foo", "", true},
		{"./foo", "", true},
	}
	for _, c := range cases {
		got, err := canonicalFSPath(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("canonicalFSPath(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("canonicalFSPath(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("canonicalFSPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
