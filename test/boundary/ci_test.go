package boundary

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryGateTargetRunsInCI pins that the Makefile is the ONE recipe
// for every gate: each `check-*` target must be invoked (`make <target>`)
// from some workflow under .github/workflows. Before this pin, gates.yml
// re-spelled check-web / check-parity / check-e2e / check-electron by
// hand and the two copies drifted (retries, verbosity, a nested xvfb-run
// around an npm script that already wraps one) — the recipe a developer
// ran was not the recipe CI ran.
func TestEveryGateTargetRunsInCI(t *testing.T) {
	root := repoRoot(t)
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	targetRe := regexp.MustCompile(`(?m)^(check(?:-[a-z0-9]+)*):`)
	var targets []string
	for _, m := range targetRe.FindAllStringSubmatch(string(mk), -1) {
		targets = append(targets, m[1])
	}
	sort.Strings(targets)
	if len(targets) < 2 {
		t.Fatalf("expected the check gates in the Makefile, found %v", targets)
	}
	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) == 0 {
		t.Fatal("no workflows under .github/workflows")
	}
	var all strings.Builder
	for _, w := range workflows {
		b, err := os.ReadFile(w)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteByte('\n')
	}
	invoked := regexp.MustCompile(`\bmake\s+(check(?:-[a-z0-9]+)*)\b`)
	seen := map[string]bool{}
	for _, m := range invoked.FindAllStringSubmatch(all.String(), -1) {
		seen[m[1]] = true
	}
	for _, tgt := range targets {
		if !seen[tgt] {
			t.Errorf("Makefile target %q is not invoked by any workflow in .github/workflows — CI must call the make target, never re-spell its recipe", tgt)
		}
	}
}
