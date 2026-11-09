package boundary

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// copyExecutors are the files that carry a copy out, each reading what the
// copy becomes from rpc.CopyOf. A later fold that makes another executor a
// reader adds its file here.
var copyExecutors = []string{
	"internal/server/deepcopy.go",
	"internal/local/store/copy.go",
	"internal/local/store/move_copy.go",
}

// copyMechanics are the lines of copyExecutors that match copyVerdict and are
// not a verdict on what a copy becomes, each with why.
var copyMechanics = map[string]string{
	"internal/server/deepcopy.go: if rpc.IsBodyKind(t.Kind) {": "copyLeaf: which bytes a leaf's create carries",
	"internal/server/deepcopy.go: tile.LinkTargetId = target": "linkCopy: the field a leaf link's target binds to",
	"internal/local/store/copy.go: case rpc.IsDoorKind(kind):": "tileRefs: what a delete releases, not what a copy becomes",
	"internal/local/store/copy.go: case rpc.IsBodyKind(kind):": "tileRefs: what a delete releases",
}

var copyVerdict = regexp.MustCompile(`rpc\.IsDoorKind|LinkTargetId|\.Tmux|IsBodyKind|CopiesAsLink\(`)

// TestCopyExecutorsReadCopyOf holds the one owner of what a copy of a tile
// becomes: no copy executor asks a tile's kind or links itself, outside the
// mechanics listed, so the router's walk and the store's copy cannot decide
// apart.
func TestCopyExecutorsReadCopyOf(t *testing.T) {
	root := repoRoot(t)
	seen := map[string]bool{}
	for _, rel := range copyExecutors {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") || !copyVerdict.MatchString(code) {
				continue
			}
			key := rel + ": " + code
			if _, ok := copyMechanics[key]; !ok {
				t.Errorf("%s:%d: %s: a copy verdict outside rpc.CopyOf; read its CopyAs", rel, i+1, code)
			}
			seen[key] = true
		}
	}
	for key := range copyMechanics {
		if !seen[key] {
			t.Errorf("copyMechanics lists %q, which no longer appears: remove it", key)
		}
	}
}
