package boundary

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// A verdict with one owner is read from it: no production file outside the
// owners spells the verdict's body. allowed is the sites still left, and
// falls to zero as they call the owner.
var verdictOwners = []struct {
	verdict string
	owners  []string
	re      *regexp.Regexp
	allowed int
}{
	{"a leaf link (rpc.LeafLink)", []string{"api/rpc/types.go"},
		regexp.MustCompile(`LinkTargetId\s*[!=]=\s*""`), 0},
	{"a grid is the scratch grid (rpc.Ephemeral, store.Ephemeral)",
		[]string{"api/rpc/types.go", "internal/local/store/store.go"},
		regexp.MustCompile(`[!=]=\s*[\w.]*[sS]cratch\w*|[sS]cratch\w*(\(\))?\s*[!=]=\s*[^"\s0]`), 0},
	{"an id names a file or a directory (rpc.OnDisk)", []string{"api/rpc/segment.go"},
		regexp.MustCompile(`ShapeOf\([^)]*\)\s*[!=]=\s*(rpc\.)?ShapeKey`), 0},
}

func TestVerdictsAreReadFromTheirOwner(t *testing.T) {
	root := repoRoot(t)
	for _, row := range verdictOwners {
		var sites []string
		walkRepo(t, root, func(rel, _ string, data []byte) {
			rel = filepath.ToSlash(rel)
			if isTestFile(rel) || !isProduction(rel) || slices.Contains(row.owners, rel) {
				return
			}
			for i, line := range bytes.Split(data, []byte("\n")) {
				if row.re.Match(line) {
					sites = append(sites, fmt.Sprintf("%s:%d", rel, i+1))
				}
			}
		})
		t.Logf("%s: %d sites outside its owner", row.verdict, len(sites))
		switch {
		case len(sites) > row.allowed:
			t.Errorf("%s is spelled outside %v at %v (allowed %d): call the owner", row.verdict, row.owners, sites, row.allowed)
		case len(sites) < row.allowed:
			t.Errorf("%s is spelled at %d sites, below its allowance of %d: lower it", row.verdict, len(sites), row.allowed)
		}
	}
}
