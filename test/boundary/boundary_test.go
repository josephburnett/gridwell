// Package boundary pins the module structure: the dependency arrows between
// the in-repo modules, and the api module's dependency budget. It imports
// nothing of ours, reading the tree and shelling out to `go list`, so it can
// police every module without being inside any of them.
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

// pluginsModule is the plugins repository. Nothing here may reach into it.
const pluginsModule = "github.com/josephburnett/gridwell-plugins"

// modules maps each in-repo module, by path suffix with "" for the root, to
// the other in-repo modules its non-test packages may import. Tests are
// exempt, because `go list .Imports` excludes test files and a seam test
// crosses on purpose.
var modules = map[string][]string{
	// The api imports nothing of ours: it is the contract.
	"api": {},
	// doctype: neutral text-document semantics, self-contained.
	"internal/doctype": {},
	// The plugins are their own repository, whose modules depend on the api
	// and never on this one; TestNoPluginImplementation keeps it that way.
	// The root module is the server library and its embedded client.
	"": {"api", "internal/doctype"},
	// The stock host takes the server and the api. It spawns plugin
	// binaries rather than importing them.
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

// TestAPIDependencyBudget pins the api module's direct dependencies. Every
// plugin ever written inherits that graph, so a new entry is a decision. The
// budget is wire only, and host persistence lives in the root module, so no
// third-party plugin inherits a database driver it never calls.
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

// No package here, test files included, may import a plugin implementation,
// and no module may declare the plugins repository as a dependency, so every
// plugin-specific behavior rides a wire declaration. A test reaches a real
// plugin the way production does, through internal/plugintest.
func TestNoPluginImplementation(t *testing.T) {
	root := repoRoot(t)
	for mod := range modules {
		dir := filepath.Join(root, mod)
		// Every import edge, the ones only tests have included.
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
	// A go.mod that names the plugins repository, by require or by
	// replace, is the same coupling one step earlier.
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

// doorServerOwners maps each source file allowed to construct a door server or
// its listener to why it may, so a harness cannot serve a door configured
// differently from the node's. nodeexport.go owns the node doors; the other two
// entries are not node doors, being a plugin subprocess's own server and a
// throwaway for the namespace codec round trip.
var doorServerOwners = map[string]string{
	"internal/server/nodeexport.go":        "the node's door owner: WebDoorServer, ConnectionDoorServer, ConnectionHandler, ListenConnectionDoor",
	"internal/plugintest/plugintest.go":    "not a node door: the plugin subprocess's own gRPC server",
	"internal/namespace/roundtrip_test.go": "not a node door: a throwaway gRPC server for the namespace codec test",
}

// No file here, test files included, builds a raw http.Server or gRPC server or
// opens a raw unix listener outside the owners above. A harness that built its
// own would serve a shape the node never runs, and every seam test through it
// would cross a door that does not exist in production.
func TestOneDoorServerOwner(t *testing.T) {
	root := repoRoot(t)
	// Concatenated so this file does not match its own needles.
	httpNeedle := "&http.Server" + "{"
	grpcNeedle := "grpc.NewServer" + "("
	unixNeedle := "net.Listen(" + `"unix"`
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if pruned(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, ok := doorServerOwners[rel]; ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for line := 1; sc.Scan(); line++ {
			text := sc.Text()
			if strings.Contains(text, httpNeedle) {
				t.Errorf("%s:%d builds a raw http.Server — a node door's server shape has one owner (server.WebDoorServer / server.ConnectionDoorServer); route through it", rel, line)
			}
			if strings.Contains(text, grpcNeedle) {
				t.Errorf("%s:%d builds a raw gRPC server — the node door's gRPC server lives in %s; if this is not a node door, exempt it in doorServerOwners with the reason", rel, line, "internal/server/nodeexport.go")
			}
			if strings.Contains(text, unixNeedle) {
				t.Errorf("%s:%d opens a raw unix listener — the connection door's listener has one owner (server.ListenConnectionDoor), and its 0600 mode is the door's whole gate; route through it or exempt this file in doorServerOwners with the reason", rel, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// No file here, test files included, keeps a subscriber set as a map of
// channels outside internal/eventhub. Three fan-outs once existed where the
// tree declared one, and each copy dropped a distinct change when a consumer
// stalled. eventhub.Hub is the one fan-out and the one drop policy.
func TestOneEventFanOut(t *testing.T) {
	root := repoRoot(t)
	// Concatenated so this file does not match its own needle.
	needle := "]chan" + " "
	hub := filepath.Join("internal", "eventhub") + string(filepath.Separator)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if pruned(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, hub) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for line := 1; sc.Scan(); line++ {
			text := sc.Text()
			i := strings.Index(text, needle)
			if i < 0 || !strings.Contains(text[:i], "map[") {
				continue
			}
			t.Errorf("%s:%d keeps a map of channels — an event fan-out has one owner, internal/eventhub, which coalesces per entity instead of dropping; use eventhub.Hub", rel, line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
