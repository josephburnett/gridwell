// Package boundary codifies the module structure: the dependency arrows
// between the in-repo modules, and the api module's dependency budget. It
// imports nothing of ours — it reads the tree and shells out to `go list` — so
// it can police every module without being inside any of them. A wrong arrow
// is a failing build rather than a review argument, because this coupling
// erodes when left to intention.
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

// pluginsModule is the plugins repository. Nothing here may reach into it:
// gridwell owns the door, that repository owns the plugins.
const pluginsModule = "github.com/josephburnett/gridwell-plugins"

// modules maps each in-repo module, by path suffix with "" for the root, to
// the other in-repo modules its non-test packages may import. Tests are exempt
// by construction: `go list .Imports` excludes test files, and a seam test
// deliberately crosses.
var modules = map[string][]string{
	// The api imports nothing of ours: it is the contract.
	"api": {},
	// doctype: neutral text-document semantics, self-contained.
	"internal/doctype": {},
	// The plugins are not here at all: they are their own repository, whose
	// modules depend on the api and never on this one. TestNoPluginImplementation
	// below is what keeps it that way.
	//
	// The root module is the server library and its embedded client: the api
	// and the neutral packages, never a plugin implementation.
	"": {"api", "internal/doctype"},
	// The stock host: server and api. No plugins; it spawns binaries.
	"apps/gridwell": {"", "api", "internal/doctype"},
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
			t.Errorf("module %s crosses a forbidden arrow:\n  %s",
				name, strings.Join(bad, "\n  "))
		}
	}
}

// TestAPIDependencyBudget pins the api module's direct dependencies. That
// graph is inherited by every plugin ever written, so a new entry is a
// deliberate decision rather than drift. The budget is wire only: host
// persistence lives in the root module, so no third-party plugin inherits a
// database driver for code it never calls.
func TestAPIDependencyBudget(t *testing.T) {
	allowed := map[string]bool{
		"connectrpc.com/connect":         true,
		"github.com/hashicorp/go-hclog":  true,
		"github.com/hashicorp/go-plugin": true,
		"google.golang.org/grpc":         true,
		"google.golang.org/protobuf":     true,
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
			t.Errorf("api/go.mod gained a direct dependency outside the budget: %s (every plugin inherits this graph)", dep)
		}
	}
}

// TestNoPluginImplementation is the standing pin on the third-party door: no
// package in this repository, TEST FILES INCLUDED, may import a plugin
// implementation, and no module here may even declare the plugins repository
// as a dependency. The host must not know its plugins — every plugin-specific
// behavior rides a wire declaration — and this separation has eroded
// repeatedly when left to intention, so it is machinery, not a review
// argument.
//
// Tests are the half that used to leak: a seam test that linked the fs plugin
// gave the host tree an import edge no production code had. They reach a real
// plugin the way production does instead — internal/plugintest spawns the
// shipped gridwell-plugin-<kind> binary through compose.LoadPlugin.
func TestNoPluginImplementation(t *testing.T) {
	root := repoRoot(t)
	for mod := range modules {
		dir := filepath.Join(root, mod)
		// Every import edge, including the ones only tests have: Imports is
		// the non-test set, TestImports the in-package test files', and
		// XTestImports the _test package's.
		cmd := exec.Command("go", "list", "-f",
			`{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`, "./...")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list in %s: %v\n%s", mod, err, out)
		}
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			for _, imp := range fields[1:] {
				if imp == pluginsModule || strings.HasPrefix(imp, pluginsModule+"/") {
					t.Errorf("%s imports %s — a plugin implementation is another repository's; spawn the binary through internal/plugintest instead", fields[0], imp)
				}
			}
		}
	}
	// A go.mod that names the plugins repository at all — require or replace —
	// is the same coupling one step earlier, and would let an import back in.
	mods, err := filepath.Glob(filepath.Join(root, "**", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range append(goWorkUses(t, root), ".") {
		mods = append(mods, filepath.Join(root, dir, "go.mod"))
	}
	seen := map[string]bool{}
	for _, path := range mods {
		if seen[path] {
			continue
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), pluginsModule) {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s names %s — no module here may depend on the plugins repository", rel, pluginsModule)
		}
	}
}
