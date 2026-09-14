package boundary

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestE2ELaunchesFromTheRunSnapshot pins that apps/desktop/e2e/runtree.ts is
// the only place the e2e suites say where a launch reads its build artifacts.
// A harness that resolves them itself runs whatever the shared checkout holds
// at that test's launch, which a rebuild beside a running gate replaces
// (docs/flake-ledger.md, lost-release.spec.ts).
func TestE2ELaunchesFromTheRunSnapshot(t *testing.T) {
	root := repoRoot(t)
	// The env vars naming the artifacts, and the constant a harness resolving
	// the repo root itself has always been called.
	names := regexp.MustCompile(`GRIDWELL_SIDECAR|GRIDWELL_STATIC|GRIDWELL_PLUGIN_DIR|GRIDWELL_SERVE_BIN|REPO_ROOT`)
	var offenders []string
	for _, dir := range []string{"e2e", "e2e-web"} {
		files, err := filepath.Glob(filepath.Join(root, "apps", "desktop", dir, "*.ts"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasPrefix(filepath.Base(f), "runtree.") {
				continue // the owner and its test
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if names.Match(src) {
				rel, _ := filepath.Rel(root, f)
				offenders = append(offenders, filepath.ToSlash(rel))
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("these e2e files name a launch artifact themselves; take it from e2e/runtree.ts (treeEnv, serveBin, staticDir) so the run tests the tree it pinned at setup:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
