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

// Main makes the owner true in the OS through one function: no non-test file
// in apps/desktop/src/main focuses a webContents outside
// WebviewRegistry.applyKeyboard. index.ts's second-instance handler raises a
// window, which no owner names.
func TestOneKeyboardApplierInMain(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "apps", "desktop", "src", "main")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	needle := "." + "focus()"
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".test.ts") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		inApplier := false
		for i, line := range strings.Split(string(data), "\n") {
			switch {
			case name == "webviews.ts" && strings.HasPrefix(line, "  private applyKeyboard("):
				inApplier = true
			case inApplier && line == "  }":
				inApplier = false
			}
			if !strings.Contains(line, needle) || inApplier {
				continue
			}
			if name == "index.ts" && strings.Contains(line, "win"+needle) {
				continue
			}
			t.Errorf("apps/desktop/src/main/%s:%d focuses a webContents outside WebviewRegistry.applyKeyboard", name, i+1)
		}
	}
}
