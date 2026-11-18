package boundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A queued text save reads what it sends, the bytes and the claim, when its
// turn on the chain comes (textedit.SaveNow), never a snapshot taken when it
// was enqueued: so in client/wasm the one text write is called from
// saveQueuedContent alone, and the content entry's basis is read beside it
// only by the beforeunload beacon, which has no chain.
func TestOneTextSaveSpelling(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "client", "wasm")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Concatenated so this file does not match its own needles.
	allowed := map[string]string{
		"." + "postWriteContent(": "mutate.go:saveQueuedContent",
		"." + "SaveBasis(":        "text_flush.go:beaconTileContent",
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fn := ""
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "func ") {
				fn = line
			}
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for needle, owner := range allowed {
				if !strings.Contains(line, needle) {
					continue
				}
				file, name, _ := strings.Cut(owner, ":")
				if e.Name() == file && strings.Contains(fn, ") "+name+"(") {
					continue
				}
				t.Errorf("client/wasm/%s:%d calls %s outside %s: a queued text save reads its bytes and claim through textedit.SaveNow", e.Name(), i+1, needle, owner)
			}
		}
	}
}
