package boundary

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryBundledKindIsCrawledByTheWebSuite pins that the composition
// parity gate (`make check-parity`, docs/plugin.md) exercises EVERY
// provider kind gridwell-all bundles: each key of pluginFactories() in
// apps/gridwell-all/main.go must be seeded (`kind: '<k>'`) by some spec
// under apps/desktop/e2e-web. Before this pin the suite only ever seeded
// fs, so "parity" for proc and gitlab was a gate that ran no test — the
// in-process composition of those kinds was never crossed.
func TestEveryBundledKindIsCrawledByTheWebSuite(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "apps", "gridwell-all", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func pluginFactories()")
	if i < 0 {
		t.Fatal("apps/gridwell-all/main.go: pluginFactories() not found")
	}
	body = body[i:]
	if end := strings.Index(body, "\n}\n"); end >= 0 {
		body = body[:end]
	}
	keyRe := regexp.MustCompile(`(?m)^\s*"([a-z0-9]+)":\s*\S`)
	var kinds []string
	for _, m := range keyRe.FindAllStringSubmatch(body, -1) {
		kinds = append(kinds, m[1])
	}
	sort.Strings(kinds)
	if len(kinds) == 0 {
		t.Fatal("pluginFactories() enumerates no kinds — the regexp or the loadout changed shape")
	}

	specs, err := filepath.Glob(filepath.Join(root, "apps", "desktop", "e2e-web", "*.spec.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) == 0 {
		t.Fatal("no specs under apps/desktop/e2e-web")
	}
	seeded := map[string]bool{}
	seedRe := regexp.MustCompile(`kind:\s*'([a-z0-9]+)'`)
	for _, s := range specs {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range seedRe.FindAllStringSubmatch(string(b), -1) {
			seeded[m[1]] = true
		}
	}
	for _, k := range kinds {
		if !seeded[k] {
			t.Errorf("gridwell-all bundles kind %q but no apps/desktop/e2e-web spec seeds it — the parity gate never crosses that kind in-process", k)
		}
	}
}
