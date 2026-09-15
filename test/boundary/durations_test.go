package boundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// allowedUntested names the declared durations no test's wait may be bound
// to, each with the reason waiting on it proves nothing. An entry whose
// reason is the 2026-09-13 line below is a debt to remove, not an exemption:
// it records what was already unbound when this gate was written, so the gate
// could go green on the branch that added it. Delete the entry as the test
// lands; never add a new one with that reason.
var allowedUntested = map[string]string{
	"snapMs":     "the drag ghost's flight to its landing cell: pixels only, gating nothing a test could observe",
	"snapBackMs": "the drag ghost's flight home after a refused drop: pixels only, gating nothing a test could observe",
}

// A declared duration is a promise about timing, and lens 8 of the holistic
// assessment found the same class broken in four runs: a cadence spelled in
// production that nothing waits on, so its value can change and every gate
// stays green. The lens had no enforcement, so each round of fixes decayed.
// This is the enforcement: every duration declared in the production tree has
// to appear, by name, in a test.
//
// The search is the identifier. A client/wasm value the page only reaches
// through window.__gridwellTest counts when a spec spells either the
// identifier or the hook key that reads it, so an accessor a spec polls
// vouches for the duration it computes from.
//
// Only package-level declarations are read. A duration written inline has no
// name to bind a test to, and giving it one is the first half of the fix.
func TestEveryDeclaredDurationHasATest(t *testing.T) {
	root := repoRoot(t)
	tested := identifiersInTests(t, root)
	hookKeys := testHookKeys(t, root)
	for _, d := range declaredDurations(t, root) {
		if tested[d.name] || allowedUntested[d.name] != "" {
			continue
		}
		if key, ok := hookKeys[d.name]; ok && tested[key] {
			continue
		}
		t.Errorf("%s:%d: %s is a declared duration no test names\n"+
			"  searched: the identifier %q as a word in every *_test.go, *.test.ts and *.spec.ts file "+
			"(apps/desktop/e2e and e2e-web included) and every file under apps/desktop/src/harness, "+
			"and the __gridwellTest hook keys that read it\n"+
			"  bind a test's wait to the value, or add %q to allowedUntested with the reason waiting on it proves nothing",
			d.rel, d.line, d.name, d.name, d.name)
	}
}

// testHookKeys maps each identifier the __gridwellTest accessors read to a
// hook key that reads it, following a key whose value is a method of this file
// into that method's body.
func testHookKeys(t *testing.T, root string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, "client", "wasm", "testhook.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse client/wasm/testhook.go: %v", err)
	}
	bodies := map[string]map[string]bool{}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			bodies[fn.Name.Name] = identsIn(fn.Body)
		}
	}
	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		lit, ok := kv.Key.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		key := strings.Trim(lit.Value, `"`)
		for name := range identsIn(kv.Value) {
			out[name] = key
			for inner := range bodies[name] {
				out[inner] = key
			}
		}
		return true
	})
	return out
}

func identsIn(n ast.Node) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			out[id.Name] = true
		}
		return true
	})
	return out
}

type durationDecl struct {
	rel  string
	line int
	name string
}

// timeUnit matches the spellings that make a value a duration.
var timeUnit = map[string]bool{"Second": true, "Millisecond": true, "Minute": true, "Hour": true}

// tsDuration matches a TypeScript module const named for milliseconds whose
// value carries a number: the exported cadences and the `silenceMs` option
// defaults alike.
var tsDuration = regexp.MustCompile(`(?m)^[\t ]*(?:export[\t ]+)?const[\t ]+([A-Za-z_$][\w$]*(?:_MS|Ms))[\t ]*=[^;\n]*\d`)

var identifier = regexp.MustCompile(`[A-Za-z_$][\w$]*`)

func declaredDurations(t *testing.T, root string) []durationDecl {
	t.Helper()
	var out []durationDecl
	walkRepo(t, root, func(rel, path string, data []byte) {
		if isTestFile(rel) || !isProduction(rel) {
			return
		}
		switch {
		case strings.HasSuffix(rel, ".go"):
			out = append(out, goDurations(t, rel, path)...)
		case strings.HasSuffix(rel, ".ts") && strings.HasPrefix(rel, filepath.Join("apps", "desktop", "src")):
			for _, m := range tsDuration.FindAllSubmatchIndex(data, -1) {
				name := string(data[m[2]:m[3]])
				out = append(out, durationDecl{rel, 1 + strings.Count(string(data[:m[2]]), "\n"), name})
			}
		}
	})
	return out
}

// goDurations reads the package-level const and var declarations: a value
// spelled in time units anywhere inside it (a plain product, or a struct
// literal field like the keepalive parameters and the dial backoff), a
// declared time.Duration, or a millisecond count named *Ms, wherever it
// lives: client/cadence owns the shim's, and a wait is a wait in any package.
func goDurations(t *testing.T, rel, path string) []durationDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var out []durationDecl
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, s := range gen.Specs {
			spec, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			isDuration := isDurationType(spec.Type)
			for _, v := range spec.Values {
				ast.Inspect(v, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok {
						if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && timeUnit[sel.Sel.Name] {
							isDuration = true
						}
					}
					return true
				})
			}
			for i, name := range spec.Names {
				ms := msName(name.Name) && i < len(spec.Values) && isNumber(spec.Values[i])
				if !isDuration && !ms {
					continue
				}
				out = append(out, durationDecl{rel, fset.Position(name.Pos()).Line, name.Name})
			}
		}
	}
	return out
}

func isDurationType(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time" && sel.Sel.Name == "Duration"
}

func msName(n string) bool { return strings.HasSuffix(n, "Ms") || strings.HasSuffix(n, "MS") }

func isNumber(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && (lit.Kind == token.INT || lit.Kind == token.FLOAT)
}

// identifiersInTests is every word the tree's tests spell. This file is not
// among them: the names in allowedUntested would otherwise vouch for
// themselves.
func identifiersInTests(t *testing.T, root string) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	walkRepo(t, root, func(rel, path string, data []byte) {
		if !isTestFile(rel) || strings.HasPrefix(rel, filepath.Join("test", "boundary")) {
			return
		}
		for _, w := range identifier.FindAll(data, -1) {
			seen[string(w)] = true
		}
	})
	return seen
}

func isTestFile(rel string) bool {
	return strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, ".test.ts") ||
		strings.HasSuffix(rel, ".spec.ts") || strings.HasPrefix(rel, filepath.Join("apps", "desktop", "src", "harness"))
}

// notProduction is the tree scripts/comment-share.sh reads, in reverse:
// generated code, build output, harnesses and test scaffolding.
var notProduction = []string{
	"/gen/", "/dist/", "/out/", "/e2e/", "e2e-web", "playwright",
	"/harness/", "plugintest", "servertest", "dialtest", "shellsvctest", "test/boundary",
}

func isProduction(rel string) bool {
	slashed := "/" + filepath.ToSlash(rel)
	for _, frag := range notProduction {
		if strings.Contains(slashed, frag) {
			return false
		}
	}
	return !strings.HasSuffix(rel, ".d.ts")
}

func walkRepo(t *testing.T, root string, visit func(rel, path string, data []byte)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if pruned(root, path, d) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".ts") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		visit(rel, path, data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
