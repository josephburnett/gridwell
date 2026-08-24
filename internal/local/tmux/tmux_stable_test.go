package tmux

// Stable artifact paths (2026-08-23): the per-boot CreateTemp trio
// leaked three artifacts per server start once the local plugin folded
// into the node, and a /tmp cleaner could delete a RUNNING session's
// shim. One directory per socket, overwritten idempotently, is the
// class fix — this pins it.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactsAreStablePerSocket(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	c1, cleanup1, err := New("stabletest", "true", "bash")
	if err != nil {
		t.Fatal(err)
	}
	c2, _, err := New("stabletest", "true", "bash")
	if err != nil {
		t.Fatal(err)
	}
	if c1.configPath != c2.configPath || c1.browserShim != c2.browserShim || c1.shadowDir != c2.shadowDir {
		t.Fatalf("paths not stable across boots:\n%+v\n%+v", c1, c2)
	}
	// One directory total — no per-boot accumulation.
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "gridwell-tmux-stabletest" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp dir grew: %v", names)
	}
	// A second socket keeps its own home; cleanup removes only its own.
	c3, cleanup3, err := New("othertest", "true", "bash")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(c3.configPath) == filepath.Dir(c1.configPath) {
		t.Fatal("sockets must not share artifact dirs")
	}
	if err := cleanup3(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c1.configPath); err != nil {
		t.Fatal("cleanup of one socket removed another's artifacts")
	}
	if err := cleanup1(); err != nil {
		t.Fatal(err)
	}
}
