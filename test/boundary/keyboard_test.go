package boundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The keyboard's owner (gesture.Keyboard) is applied in one place in the page:
// no file in client/wasm but keyboard.go focuses or blurs an element, so no
// path can move the keyboard beside it (CLAUDE.md, the click-lands decision,
// 2026-11-17).
func TestOneKeyboardApplierInThePage(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "client", "wasm")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Concatenated so this file does not match its own needles.
	needles := []string{`Call("` + `focus"`, `Call("` + `blur"`}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || e.Name() == "keyboard.go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, n := range needles {
				if strings.Contains(line, n) {
					t.Errorf("client/wasm/%s:%d moves the keyboard outside keyboard.go's applyKeyboard: derive the owner (gesture.Keyboard) instead", e.Name(), i+1)
				}
			}
		}
	}
}
