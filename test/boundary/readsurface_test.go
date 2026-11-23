package boundary

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A read's failure is told by one verdict, clientsync's outcome tables, so a
// disabled source's refusal says nothing on every read path alike (CLAUDE.md:
// it "reads as dark, with no notice standing"). In client/wasm a read verb's
// failure goes on the notice row through surfaceReadError, never straight
// through surfaceRPCError, which is the writes'.
func TestEveryReadFailureGoesThroughTheReadVerdict(t *testing.T) {
	// The service's verbs that change nothing.
	reads := map[string]bool{"Info": true, "Probe": true, "Handshake": true, "GetGrid": true, "GetTile": true,
		"GetTilePreview": true, "Find": true, "ReadContent": true, "ServeContent": true, "ShellTmuxAlive": true}
	call := regexp.MustCompile(`\.surface` + `(RPC|Read)Error\("([A-Za-z]+)"`)
	dir := filepath.Join(repoRoot(t), "client", "wasm")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			m := call.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			seen[m[1]] = true
			if reads[m[2]] && m[1] == "RPC" {
				t.Errorf("client/wasm/%s:%d tells a %s failure through surfaceRPCError: a read's goes through surfaceReadError", e.Name(), i+1, m[2])
			}
			if !reads[m[2]] && m[1] == "Read" {
				t.Errorf("client/wasm/%s:%d tells %s, a write, through surfaceReadError", e.Name(), i+1, m[2])
			}
		}
	}
	if !seen["Read"] {
		t.Error("no surfaceReadError call found: the needle no longer matches the code")
	}
}
