package boundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The + menu's lifetime is its pane's focus, ended in one place: draw applies
// menu.State.SyncFocus, and no other file in client/wasm calls it, so no path
// that moves focus or removes a pane closes the menu itself.
func TestOneMenuFocusSync(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "client", "wasm")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Concatenated so this file does not match its own needle.
	needle := "menu." + "SyncFocus("
	found := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, needle) {
				continue
			}
			found++
			if e.Name() != "render.go" {
				t.Errorf("client/wasm/%s:%d syncs the menu with focus outside draw: draw applies it at every frame", e.Name(), i+1)
			}
		}
	}
	if found != 1 {
		t.Errorf("menu.SyncFocus is called %d times in client/wasm, want once, at the top of draw", found)
	}
}
