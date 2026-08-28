package boundary

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestFlakeLedgerIndexesEveryFlakeNote pins docs/flake-ledger.md to the
// specs: every file under apps/desktop/e2e and e2e-web whose comments
// mention a flake must be listed in the ledger by path. CLAUDE.md sent
// readers to "the known-flake ledger" for months while no such file
// existed — the history lived only in spec comments. The ledger indexes
// them; this test keeps the index from drifting behind the comments.
func TestFlakeLedgerIndexesEveryFlakeNote(t *testing.T) {
	root := repoRoot(t)
	ledger, err := os.ReadFile(filepath.Join(root, "docs", "flake-ledger.md"))
	if err != nil {
		t.Fatal(err)
	}
	flaky := regexp.MustCompile(`(?i)flak`)
	var missing []string
	for _, dir := range []string{"e2e", "e2e-web"} {
		files, err := filepath.Glob(filepath.Join(root, "apps", "desktop", dir, "*.ts"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if !flaky.Match(src) {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			if !strings.Contains(string(ledger), "`"+filepath.ToSlash(rel)+"`") {
				missing = append(missing, rel)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("specs carry a flake note but docs/flake-ledger.md does not list them (add a row: what flaked, the mechanism, what closed it):\n  %s",
			strings.Join(missing, "\n  "))
	}
}
