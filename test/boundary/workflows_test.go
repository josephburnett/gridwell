package boundary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goWorkUses returns the module dirs go.work stitches ("." for the root).
func goWorkUses(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	var uses []string
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "use ("):
			in = true
		case in && l == ")":
			in = false
		case in && l != "":
			uses = append(uses, strings.TrimPrefix(l, "./"))
		}
	}
	if len(uses) == 0 {
		t.Fatal("go.work lists no modules")
	}
	return uses
}

// yamlBlockList reads the indented list under `key: |` (a block scalar)
// or `key: [a, b]` (a flow list) from a workflow, at the first `key:`
// after a line containing `after` ("" = from the top) — enough of yaml
// for the two fields these tests pin, without a parser dependency.
func yamlBlockList(t *testing.T, path, after, key string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	armed := after == ""
	for i, line := range lines {
		l := strings.TrimSpace(line)
		if !armed {
			armed = strings.Contains(line, after)
			continue
		}
		if !strings.HasPrefix(l, key+":") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(l, key+":"))
		if strings.HasPrefix(rest, "[") {
			var out []string
			for _, item := range strings.Split(strings.Trim(rest, "[]"), ",") {
				out = append(out, strings.Trim(strings.TrimSpace(item), `'"`))
			}
			return out
		}
		if rest != "|" {
			return []string{strings.Trim(rest, `'"`)}
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		var out []string
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" {
				continue
			}
			if len(next)-len(strings.TrimLeft(next, " ")) <= indent {
				break
			}
			out = append(out, strings.TrimSpace(next))
		}
		return out
	}
	t.Fatalf("%s: no %q key after %q", path, key, after)
	return nil
}

// TestGoCacheKeyCoversEveryModule pins that the check workflow's Go cache key,
// setup-go's cache-dependency-path, hashes every module's go.sum. A module
// missing from it is a module whose dependency change never invalidates the
// cache, so CI restores a stale module cache and re-downloads on every run,
// or keeps building against what the old sums pinned.
func TestGoCacheKeyCoversEveryModule(t *testing.T) {
	root := repoRoot(t)
	// The setup-go step's key, not setup-node's; both use the name.
	patterns := yamlBlockList(t, filepath.Join(root, ".github", "workflows", "check.yml"), "actions/setup-go", "cache-dependency-path")
	matches := func(rel string) bool {
		for _, p := range patterns {
			if p == "**/go.sum" {
				return true // the universal glob: every go.sum anywhere
			}
			if ok, _ := filepath.Match(p, rel); ok {
				return true
			}
		}
		return false
	}
	for _, dir := range goWorkUses(t, root) {
		sum := filepath.Join(dir, "go.sum")
		if _, err := os.Stat(filepath.Join(root, sum)); err != nil {
			continue // a module with no dependencies has no go.sum
		}
		if !matches(filepath.ToSlash(sum)) {
			t.Errorf("check.yml cache-dependency-path %v does not cover %s — its dependency changes never invalidate CI's Go cache (use **/go.sum)", patterns, sum)
		}
	}
}
