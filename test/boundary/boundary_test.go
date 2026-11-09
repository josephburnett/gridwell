// Package boundary pins the package structure: the dependency arrows between
// the repo's trees, and the api's dependency budget. It imports nothing of
// ours, reading the tree and shelling out to `go list`.
package boundary

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const repoModule = "github.com/josephburnett/gridwell"

// trees maps each package tree, by path under the module with "" for the
// rest of it, to the other trees its non-test packages may import. Tests are
// exempt, because `go list .Imports` excludes test files and a seam test
// crosses on purpose.
var trees = map[string][]string{
	// The api imports nothing of ours: it is the contract.
	"api": {},
	// doctype: neutral text-document semantics, self-contained.
	"internal/doctype": {},
	// The rest is the server library and its embedded client.
	"": {"api", "internal/doctype"},
	// The node's binary takes the server and the api.
	"apps": {"", "api", "internal/doctype"},
}

// treeOf resolves an import path to its tree by longest prefix, "" for the
// rest of the module, or "-" for a foreign import.
func treeOf(imp string) string {
	if imp != repoModule && !strings.HasPrefix(imp, repoModule+"/") {
		return "-"
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(imp, repoModule), "/")
	best := ""
	for tr := range trees {
		if tr != "" && (rest == tr || strings.HasPrefix(rest, tr+"/")) && len(tr) > len(best) {
			best = tr
		}
	}
	return best
}

// repoRoot is the directory holding the module's go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && bytes.HasPrefix(data, []byte("module "+repoModule+"\n")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod of " + repoModule + " above the test dir")
		}
		dir = parent
	}
}

// goList runs `go list` at the repo root with a template over every package
// of the module, one line per package.
func goList(t *testing.T, format string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", format, repoModule+"/...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		t.Fatalf("go list: %v\n%s", err, ee.Stderr)
	} else if err != nil {
		t.Fatal(err)
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// TestArrows asserts every non-test import edge between our trees is in the
// allowed set.
func TestArrows(t *testing.T) {
	var bad []string
	for _, line := range goList(t, `{{.ImportPath}} {{join .Imports " "}}`) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		from := treeOf(fields[0])
		allowed := map[string]bool{from: true}
		for _, a := range trees[from] {
			allowed[a] = true
		}
		for _, imp := range fields[1:] {
			if tr := treeOf(imp); tr != "-" && !allowed[tr] {
				bad = append(bad, fields[0]+" imports "+imp)
			}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Errorf("a package crosses a forbidden arrow:\n  %s", strings.Join(bad, "\n  "))
	}
}

// TestAPIDependencyBudget pins the modules the api's packages and their tests
// import beyond the standard library and this module, whose arrows TestArrows
// owns: the contract every client of the wire builds on, so a new entry is a
// decision. The budget is wire only; persistence lives outside the api.
func TestAPIDependencyBudget(t *testing.T) {
	allowed := []string{
		"connectrpc.com/connect",
		"google.golang.org/grpc",
		"google.golang.org/protobuf",
	}
	within := func(imp, mod string) bool { return imp == mod || strings.HasPrefix(imp, mod+"/") }
	for _, line := range goList(t, `{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`) {
		fields := strings.Fields(line)
		if len(fields) == 0 || treeOf(fields[0]) != "api" {
			continue
		}
	imports:
		for _, imp := range fields[1:] {
			if !strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") || treeOf(imp) != "-" {
				continue
			}
			for _, mod := range allowed {
				if within(imp, mod) {
					continue imports
				}
			}
			t.Errorf("%s imports %s, outside the api's budget (every client of the wire inherits this graph)", fields[0], imp)
		}
	}
}

// apiServerOwners maps each source file allowed to construct an API server or
// its socket to why it may, so a harness cannot serve an API configured
// differently from the node's. nodeexport.go owns the node APIs; the other
// entry is not a node API, being a throwaway for the connection API round
// trip.
var apiServerOwners = map[string]string{
	"internal/server/nodeexport.go":        "the node's API owner: WebAPIServer, ConnectionAPIServer, ConnectionHandler, ListenConnectionAPI",
	"internal/namespace/roundtrip_test.go": "not a node API: a throwaway gRPC server for the connection API round-trip test",
}

// apiClientOwners is apiServerOwners for the dialing side: the files allowed
// to build a gRPC client, and why.
var apiClientOwners = map[string]string{
	"internal/connection/dial/dial.go":     "the node's dial owner: ClientConn",
	"internal/namespace/roundtrip_test.go": "not a node API: a throwaway gRPC client for the connection API round-trip test",
}

// No file here, test files included, builds a raw http.Server or gRPC server or
// opens a raw unix listener outside the owners above, nor a gRPC client outside
// apiClientOwners. A harness that built its own would serve or dial a shape
// the node never runs, and every seam test through it would cross an API that
// does not exist in production.
func TestOneAPIServerOwner(t *testing.T) {
	root := repoRoot(t)
	// Concatenated so this file does not match its own needles.
	httpNeedle := "&http.Server" + "{"
	grpcNeedle := "grpc.NewServer" + "("
	unixNeedle := "net.Listen(" + `"unix"`
	clientNeedle := "grpc.NewClient" + "("
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
		_, server := apiServerOwners[rel]
		_, client := apiClientOwners[rel]
		if server && client {
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
			if !client && strings.Contains(text, clientNeedle) {
				t.Errorf("%s:%d builds a raw gRPC client — the node dials a connection API with one shape (dial.ClientConn); a test that dials another can pass against an API the node cannot reach. Route through it, or exempt this file in apiClientOwners with the reason", rel, line)
			}
			if server {
				continue
			}
			if strings.Contains(text, httpNeedle) {
				t.Errorf("%s:%d builds a raw http.Server — a node API's server shape has one owner (server.WebAPIServer / server.ConnectionAPIServer); route through it", rel, line)
			}
			if strings.Contains(text, grpcNeedle) {
				t.Errorf("%s:%d builds a raw gRPC server — the node API's gRPC server lives in %s; if this is not a node API, exempt it in apiServerOwners with the reason", rel, line, "internal/server/nodeexport.go")
			}
			if strings.Contains(text, unixNeedle) {
				t.Errorf("%s:%d opens a raw unix listener — the connection API's socket has one owner (server.ListenConnectionAPI), and its 0600 mode is the API's whole gate; route through it or exempt this file in apiServerOwners with the reason", rel, line)
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

// storeWriteFunnel is the one function in internal/local/store that may open
// a write on the store's handle. Everything it writes is in a transaction and
// leaves a trace record; a write beside it does neither.
const storeWriteFunnel = "withMutation"

// storeCompact is the one write beside the funnel: VACUUM rewrites the
// file's pages and no row, and SQLite refuses it inside a transaction.
const storeCompact = "Compact"

// storeTxOpener is the one function that begins a transaction, and only the
// funnel and the read snapshot may call it: a read of several statements
// needs one state of the database, and a write needs the funnel's trace.
const storeTxOpener = "withTx"

var storeTxCallers = map[string]bool{storeWriteFunnel: true, "readSnapshot": true}

// No non-test file in the store writes on its handle (a .db selector) outside
// storeWriteFunnel, or begins a transaction outside storeTxOpener, or calls
// storeTxOpener outside storeTxCallers. Its write helpers take a *sql.Tx, so
// the handle cannot be passed to one either.
func TestOneStoreWriteFunnel(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "local", "store")
	writes := map[string]bool{"Exec": true, "ExecContext": true, "Begin": true, "BeginTx": true, "Prepare": true, "PrepareContext": true}
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == storeWriteFunnel || fn.Name.Name == storeCompact {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == storeTxOpener && !storeTxCallers[fn.Name.Name] {
					t.Errorf("%s: %s begins a transaction — only %s and readSnapshot may", fset.Position(call.Pos()), fn.Name.Name, storeWriteFunnel)
				}
				if !writes[sel.Sel.Name] || fn.Name.Name == storeTxOpener {
					return true
				}
				if recv, ok := sel.X.(*ast.SelectorExpr); ok && recv.Sel.Name == "db" {
					t.Errorf("%s: %s writes on the store's handle — every store write goes through %s, which runs it in a transaction and traces it", fset.Position(call.Pos()), fn.Name.Name, storeWriteFunnel)
				}
				return true
			})
		}
	}
}

// sqliteOpeners maps each non-test file outside internal/local/store allowed to
// open a SQLite handle to why it may. The store's handle never leaves its
// package, so a handle opened here is the only way to write gridwell.db beside
// storeWriteFunnel.
var sqliteOpeners = map[string]string{
	"internal/sourcecache/sourcecache.go":         "cache.db, the disposable connection cache, not gridwell.db",
	"internal/dbmeta/dbmeta.go":                   "the storage-only _gridwell_meta identity marker, checked before the store opens; it carries no node fact (internal/local/store/CLAUDE.md)",
	"internal/cli/backup.go":                      "VACUUM INTO a fresh snapshot file; it writes no row of the source",
	"internal/local/store/storetest/storetest.go": "a read-only (mode=ro) handle for tests that ask what is at rest",
}

// gridwell.db has one writer. No non-test file outside the store opens a
// SQLite handle unless sqliteOpeners names it, and no exported store function
// hands out the store's handle or a transaction on it. A write from anywhere
// else is untraced and outside storeWriteFunnel's transaction.
func TestOneStoreHandle(t *testing.T) {
	root := repoRoot(t)
	storeDir := filepath.Join("internal", "local", "store")
	openNeedle := "sql.Open" + "("
	fset := token.NewFileSet()
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
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if filepath.Dir(rel) == storeDir {
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || !fn.Name.IsExported() || fn.Type.Results == nil {
					continue
				}
				for _, res := range fn.Type.Results.List {
					star, ok := res.Type.(*ast.StarExpr)
					if !ok {
						continue
					}
					if sel, ok := star.X.(*ast.SelectorExpr); ok && (sel.Sel.Name == "DB" || sel.Sel.Name == "Tx") {
						if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "sql" {
							t.Errorf("%s: %s hands out the store's handle — a caller could write gridwell.db outside %s; add a store method that writes through it", fset.Position(fn.Pos()), fn.Name.Name, storeWriteFunnel)
						}
					}
				}
			}
			return nil
		}
		if _, ok := sqliteOpeners[rel]; ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(openNeedle)) {
			t.Errorf("%s opens a SQLite handle — gridwell.db is written only by internal/local/store through %s; add a store method, or name this file in sqliteOpeners with the reason it does not write the node's file", rel, storeWriteFunnel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
