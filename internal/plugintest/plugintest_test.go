package plugintest

// What the harness adds to a spawn config. Spawning itself needs a built
// binary; this pins the part that does not.

import (
	"os"
	"testing"
)

// TestWithStateDir_MintsATempDir pins that a spawn always carries a state_dir,
// a directory of this test's own, so no test writes into a real home.
func TestWithStateDir_MintsATempDir(t *testing.T) {
	cfg := map[string]string{"root": "/srv"}
	out := withStateDir(t, cfg)

	if out["state_dir"] == "" {
		t.Fatal("no state_dir in the spawn config")
	}
	if fi, err := os.Stat(out["state_dir"]); err != nil || !fi.IsDir() {
		t.Fatalf("state_dir %q is not a directory: %v", out["state_dir"], err)
	}
	if out["root"] != "/srv" {
		t.Errorf("the test's own keys were lost: %v", out)
	}
	if _, ok := cfg["state_dir"]; ok {
		t.Error("the caller's map was mutated")
	}
}

// TestWithStateDir_KeepsTheTestsOwn pins that a test restarting a plugin over
// a kept directory gets the directory it named.
func TestWithStateDir_KeepsTheTestsOwn(t *testing.T) {
	dir := t.TempDir()
	out := withStateDir(t, map[string]string{"state_dir": dir})
	if out["state_dir"] != dir {
		t.Errorf("state_dir = %q, want the test's own %q", out["state_dir"], dir)
	}
}
