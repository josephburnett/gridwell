// Package boundary CODIFIES the module structure (docs/plugin.md): the
// dependency arrows between the in-repo modules, and the api module's
// dependency budget. It imports NOTHING of ours — it reads the tree and
// shells out to `go list` — so it can police every module without being
// inside any of them. A wrong arrow is a failing build, not a review
// argument: this coupling eroded repeatedly when left to intention
// (charter, 2026-08-15).
package boundary

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const repoModule = "github.com/josephburnett/gridwell"

// modules maps each in-repo module (by path suffix, "" = root) to the
// OTHER in-repo modules its NON-TEST packages may import. Tests are
// exempt by construction: `go list .Imports` excludes test files, and
// seam tests deliberately cross (that is their job). Leaf composers
// (apps/gridwell-all, mobile) may import anything — enumeration is the
// leaf-binary privilege.
var modules = map[string][]string{
	// The api imports nothing of ours: it IS the contract.
	"api": {},
	// doctype: neutral text-document semantics, self-contained.
	"internal/doctype": {},
	// griddb: the fs/proc shared layout DB, over the api only.
	"plugins/griddb": {"api"},
	// Plugins: the api, the shared neutral homes — never the host, never
	// each other.
	"plugins/local":  {"api", "internal/doctype"},
	"plugins/fs":     {"api", "internal/doctype", "plugins/griddb"},
	"plugins/proc":   {"api", "plugins/griddb"},
	"plugins/remote": {"api"},
	// The root module is the SERVER LIBRARY (+ its embedded client): the
	// api and the neutral homes — never a plugin implementation.
	"": {"api", "internal/doctype"},
	// The stock host: server + api. NO plugins — it spawns binaries.
	"apps/gridwell": {"", "api", "internal/doctype"},
	// Leaf composers: enumeration is legal here.
	"apps/gridwell-all": {"*"},
	"mobile":            {"*"},
}

// moduleOf resolves an import path to its in-repo module by longest
// prefix, "" for the root module, or "-" for a foreign import.
func moduleOf(imp string) string {
	if !strings.HasPrefix(imp, repoModule) {
		return "-"
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(imp, repoModule), "/")
	best := ""
	for m := range modules {
		if m == "" {
			continue
		}
		if rest == m || strings.HasPrefix(rest, m+"/") {
			if len(m) > len(best) {
				best = m
			}
		}
	}
	return best
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.work above the test dir")
		}
		dir = parent
	}
}

// TestArrows asserts every non-test import edge between our modules is in
// the allowed set.
func TestArrows(t *testing.T) {
	root := repoRoot(t)
	for mod, allowed := range modules {
		if len(allowed) == 1 && allowed[0] == "*" {
			continue // leaf composer: anything goes
		}
		allowedSet := map[string]bool{mod: true}
		for _, a := range allowed {
			allowedSet[a] = true
		}
		dir := filepath.Join(root, mod)
		cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./...")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list in %s: %v\n%s", mod, err, out)
		}
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		var bad []string
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			from := fields[0]
			for _, imp := range fields[1:] {
				m := moduleOf(imp)
				if m == "-" {
					continue
				}
				if !allowedSet[m] {
					bad = append(bad, from+" imports "+imp)
				}
			}
		}
		if len(bad) > 0 {
			sort.Strings(bad)
			name := mod
			if name == "" {
				name = "(root)"
			}
			t.Errorf("module %s crosses a forbidden arrow (docs/plugin.md):\n  %s",
				name, strings.Join(bad, "\n  "))
		}
	}
}

// TestAPIDependencyBudget pins the api module's DIRECT dependencies: this
// graph is inherited by every plugin ever written, so a new entry is a
// deliberate, reviewed decision — never drift. (modernc.org/sqlite is the
// one non-wire dep: pluginmeta/dbformat are the persistence contract's
// mechanical half; the driver itself is only linked by binaries that
// import it.)
func TestAPIDependencyBudget(t *testing.T) {
	allowed := map[string]bool{
		"connectrpc.com/connect":         true,
		"github.com/hashicorp/go-hclog":  true,
		"github.com/hashicorp/go-plugin": true,
		"google.golang.org/grpc":         true,
		"google.golang.org/protobuf":     true,
		"modernc.org/sqlite":             true,
	}
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "api", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "require ("):
			inBlock = true
			continue
		case inBlock && l == ")":
			inBlock = false
			continue
		}
		if !inBlock || strings.Contains(l, "// indirect") || l == "" {
			continue
		}
		dep := strings.Fields(l)[0]
		if !allowed[dep] {
			t.Errorf("api/go.mod gained a direct dependency outside the budget: %s (docs/plugin.md — every plugin inherits this graph)", dep)
		}
	}
}
