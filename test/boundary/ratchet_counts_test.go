package boundary

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	rpcImport           = repoModule + "/api/rpc"
	namespacetestImport = repoModule + "/internal/namespace/namespacetest"
)

// importName is the name f refers to path by, or "" when f does not import it.
func importName(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == path {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return filepath.Base(p)
		}
	}
	return ""
}

// productionGo parses every non-test Go file of the production tree.
func productionGo(t *testing.T, root string, visit func(rel string, fset *token.FileSet, f *ast.File)) {
	t.Helper()
	walkRepo(t, root, func(rel, path string, data []byte) {
		if !strings.HasSuffix(rel, ".go") || isTestFile(rel) || !isProduction(rel) {
			return
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, data, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		visit(rel, fset, f)
	})
}

// unimplementedEmbeds are the struct types in internal/ that embed
// namespacetest.Unimplemented, so a verb they never wrote answers for them.
func unimplementedEmbeds(t *testing.T, root string) []string {
	var out []string
	productionGo(t, root, func(rel string, fset *token.FileSet, f *ast.File) {
		if !strings.HasPrefix(filepath.ToSlash(rel), "internal/") {
			return
		}
		ns := importName(f, namespacetestImport)
		if ns == "" {
			return
		}
		ast.Inspect(f, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				if len(field.Names) > 0 {
					continue
				}
				typ := field.Type
				if star, ok := typ.(*ast.StarExpr); ok {
					typ = star.X
				}
				sel, ok := typ.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == ns && sel.Sel.Name == "Unimplemented" {
					out = append(out, fmt.Sprintf("%s:%d", rel, fset.Position(st.Pos()).Line))
				}
			}
			return true
		})
	})
	return out
}

// TestNoUnimplementedEmbeds holds every namespace owner to answering each verb
// itself, so a refusal names its reason rather than "method X not implemented".
func TestNoUnimplementedEmbeds(t *testing.T) {
	for _, at := range unimplementedEmbeds(t, repoRoot(t)) {
		t.Errorf("%s embeds namespacetest.Unimplemented: answer every verb of namespace.Namespace explicitly, a refusal naming its reason", at)
	}
}

// declaredNames is what the tree declares: per package (by directory basename
// and by package name, and per TS module by file basename) and in all.
type declaredNames struct {
	byPkg map[string]map[string]bool
	all   map[string]bool
	files map[string]string // a file's slash path to the byPkg key its names are under
}

func (d declaredNames) add(pkg, name string) {
	if d.byPkg[pkg] == nil {
		d.byPkg[pkg] = map[string]bool{}
	}
	d.byPkg[pkg][name] = true
	d.all[name] = true
}

var tsDecl = regexp.MustCompile(`(?m)^[\t ]*(?:export[\t ]+)?(?:default[\t ]+)?(?:declare[\t ]+)?(?:async[\t ]+)?(?:const|let|var|function\*?|class|interface|type|enum)[\t ]+([A-Za-z_$][\w$]*)`)

// tsProperty matches a class field or an interface or object member.
var tsProperty = regexp.MustCompile(`(?m)^[\t ]*(?:(?:public|private|protected|static|readonly|override|declare)[\t ]+)*([A-Za-z_$][\w$]*)[\t ]*[?!]?[\t ]*[:=]`)

// tsMethod matches a class or object method's definition line.
var tsMethod = regexp.MustCompile(`(?m)^[\t ]*(?:(?:public|private|protected|static|async|readonly|override|get|set)[\t ]+)*([A-Za-z_$][\w$]*)[\t ]*\([^()\n]*\)[\t ]*(?::[^{\n]*)?\{[\t ]*$`)

// declarations reads the tree's top-level Go declarations and methods and its
// TS declarations; production limits it to the production tree.
func declarations(t *testing.T, root string, production bool) declaredNames {
	t.Helper()
	d := declaredNames{byPkg: map[string]map[string]bool{}, all: map[string]bool{}, files: map[string]string{}}
	walkRepo(t, root, func(rel, path string, data []byte) {
		if production && (isTestFile(rel) || !isProduction(rel)) {
			return
		}
		if strings.HasSuffix(rel, ".ts") {
			mod := strings.TrimSuffix(filepath.Base(rel), ".ts")
			d.files[filepath.ToSlash(rel)] = mod
			for _, re := range []*regexp.Regexp{tsDecl, tsMethod, tsProperty} {
				for _, m := range re.FindAllSubmatch(data, -1) {
					d.add(mod, string(m[1]))
				}
			}
			return
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, data, parser.SkipObjectResolution)
		if err != nil {
			return
		}
		d.files[filepath.ToSlash(rel)] = f.Name.Name
		addGoDecls(d, f, filepath.Base(filepath.Dir(rel)), f.Name.Name)
	})
	return d
}

// addGoDecls adds a Go file's top-level names, methods, struct fields and
// interface methods under each of pkgs.
func addGoDecls(d declaredNames, f *ast.File, pkgs ...string) {
	add := func(name string) {
		for _, p := range pkgs {
			d.add(p, name)
		}
	}
	for _, decl := range f.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			add(decl.Name.Name)
		case *ast.GenDecl:
			for _, s := range decl.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					add(s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(n.Name)
					}
				}
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		var fields *ast.FieldList
		switch n := n.(type) {
		case *ast.StructType:
			fields = n.Fields
		case *ast.InterfaceType:
			fields = n.Methods
		}
		if fields != nil {
			for _, field := range fields.List {
				for _, name := range field.Names {
					add(name.Name)
				}
			}
		}
		return true
	})
}

// dependencyDecls adds the declarations of every package outside this tree
// that the tree imports and whose name is in wanted, so a doc may name
// `log.Printf` or `codes.Unavailable`.
func dependencyDecls(t *testing.T, root string, d declaredNames, wanted map[string]bool) {
	t.Helper()
	cmd := exec.Command("go", "list", "-e", "-deps", "-f", "{{.Name}}|{{.Dir}}|{{join .GoFiles \",\"}}", "./...", "./api/...", "./apps/gridwell/...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 3 || !wanted[parts[0]] || strings.HasPrefix(parts[1], root) {
			continue
		}
		for _, name := range strings.Split(parts[2], ",") {
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(parts[1], name), nil, parser.SkipObjectResolution)
			if err == nil {
				addGoDecls(d, f, parts[0])
			}
		}
	}
}

var (
	qualifiedName = regexp.MustCompile(`\b([a-z][a-z0-9]*)\.([A-Za-z_][A-Za-z0-9_]*)\b`)
	// A bare name has to look like an identifier, not a capitalized word:
	// it has an underscore or a lower-to-upper step.
	bareName    = regexp.MustCompile(`\b[A-Za-z_$][\w$]*\b`)
	identShaped = regexp.MustCompile(`_|[a-z0-9][A-Z]`)
	tsConstLine = regexp.MustCompile(`^[\t ]*(?:export[\t ]+)?const[\t ]+([A-Za-z_$][\w$]*)[\t ]*(?::[\t ]*\w+[\t ]*)?=[\t ]*[0-9][0-9_]*(?:\.[0-9_]+)?[\t ]*;?[\t ]*(?://(.*))?$`)
)

// TestNoCopiedConstants: a test or harness value copied from production reads
// its owner or is pinned to it, so the owner cannot change under it unseen.
func TestNoCopiedConstants(t *testing.T) {
	for _, at := range copiedConstants(t, repoRoot(t)) {
		t.Errorf("%s by hand: read the value from its owner, or pin it with a drift test that spells both names", at)
	}
}

// copiedConstants are the numeric constants in tests and harnesses whose
// comment says they copy a production identifier, with no drift test (a test
// file whose code spells both names) pinning the copy to its owner.
func copiedConstants(t *testing.T, root string) []string {
	prod := declarations(t, root, true)
	type copied struct{ at, name, owner string }
	var found []copied
	codeWords := map[string]map[string]bool{}
	walkRepo(t, root, func(rel, path string, data []byte) {
		if !isTestFile(rel) || strings.HasPrefix(rel, filepath.Join("test", "boundary")) {
			return
		}
		codeWords[rel] = wordsOutsideComments(rel, data)
		lines := strings.Split(string(data), "\n")
		comment := func(i int) string {
			text := ""
			if j := strings.Index(lines[i], "//"); j >= 0 {
				text = lines[i][j+2:]
			}
			if i > 0 {
				if prev := strings.TrimSpace(lines[i-1]); strings.HasPrefix(prev, "//") {
					text += " " + prev
				}
			}
			return text
		}
		for _, c := range numericConsts(rel, path, data, lines) {
			for _, owner := range ownersNamed(comment(c.line-1), prod) {
				found = append(found, copied{fmt.Sprintf("%s:%d", rel, c.line), c.name, owner})
			}
		}
	})
	var out []string
	for _, c := range found {
		pinned := false
		for _, words := range codeWords {
			if words[c.name] && words[c.owner] {
				pinned = true
				break
			}
		}
		if !pinned {
			out = append(out, fmt.Sprintf("%s: %s copies %s", c.at, c.name, c.owner))
		}
	}
	return out
}

type namedConst struct {
	line int
	name string
}

// numericConsts are a file's const declarations, at any scope, whose value is
// a number, in Go optionally scaled by a time unit.
func numericConsts(rel, path string, data []byte, lines []string) []namedConst {
	var out []namedConst
	if strings.HasSuffix(rel, ".ts") {
		for i, l := range lines {
			if m := tsConstLine.FindStringSubmatch(l); m != nil {
				out = append(out, namedConst{i + 1, m[1]})
			}
		}
		return out
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	ast.Inspect(f, func(n ast.Node) bool {
		gen, ok := n.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			return true
		}
		for _, s := range gen.Specs {
			spec := s.(*ast.ValueSpec)
			for i, name := range spec.Names {
				if i < len(spec.Values) && numeric(spec.Values[i]) {
					out = append(out, namedConst{fset.Position(name.Pos()).Line, name.Name})
				}
			}
		}
		return true
	})
	return out
}

func numeric(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.BasicLit:
		return e.Kind == token.INT || e.Kind == token.FLOAT
	case *ast.ParenExpr:
		return numeric(e.X)
	case *ast.BinaryExpr:
		return e.Op == token.MUL && (numeric(e.X) && timeUnitExpr(e.Y) || timeUnitExpr(e.X) && numeric(e.Y))
	}
	return false
}

func timeUnitExpr(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time" && timeUnit[sel.Sel.Name]
}

// ownersNamed are the production identifiers a comment names, as pkg.Name or
// as a bare identifier-shaped name.
func ownersNamed(comment string, prod declaredNames) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range qualifiedName.FindAllStringSubmatch(comment, -1) {
		if prod.byPkg[m[1]][m[2]] && !seen[m[2]] {
			seen[m[2]] = true
			out = append(out, m[2])
		}
	}
	for _, w := range bareName.FindAllString(comment, -1) {
		if identShaped.MatchString(w) && prod.all[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// wordsOutsideComments is every identifier-shaped word a file spells in code
// or in a string, so a comment cannot vouch for a drift test.
func wordsOutsideComments(rel string, data []byte) map[string]bool {
	out := map[string]bool{}
	if strings.HasSuffix(rel, ".go") {
		fset := token.NewFileSet()
		var sc scanner.Scanner
		sc.Init(fset.AddFile(rel, fset.Base(), len(data)), data, nil, 0)
		for {
			_, tok, lit := sc.Scan()
			if tok == token.EOF {
				return out
			}
			if tok == token.IDENT || tok == token.STRING {
				for _, w := range identifier.FindAllString(lit, -1) {
					out[w] = true
				}
			}
		}
	}
	for _, w := range identifier.FindAll(stripTSComments(data), -1) {
		out[string(w)] = true
	}
	return out
}

// stripTSComments drops // and /* */ comments, stepping over string literals
// so a URL in a string is kept.
func stripTSComments(data []byte) []byte {
	var out bytes.Buffer
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(data) && data[j] != c && (c == '`' || data[j] != '\n') {
				if data[j] == '\\' {
					j++
				}
				j++
			}
			end := min(j+1, len(data))
			out.Write(data[i:end])
			i = end - 1
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			end := bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				return out.Bytes()
			}
			i += end + 3
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes()
}

// ratchetDocs are the docs whose backticked symbols must name the tree.
func ratchetDocs(t *testing.T, root string) []string {
	t.Helper()
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"CLAUDE.md", "ARCHITECTURE.md", "internal/local/store/CLAUDE.md"} {
		docs = append(docs, filepath.Join(root, d))
	}
	sort.Strings(docs)
	return docs
}

var (
	backticked = regexp.MustCompile("`([^`\n]+)`")
	// docSymbol is `pkg.Name`, `pkg.Name()`, `Name()` or `file.go:Name`. A
	// bare `Name` is not read: most are another program's (`localStorage`,
	// `WebContentsView`) or the plugins repository's.
	docSymbol = regexp.MustCompile(`^(?:([a-z][a-z0-9]*)\.|((?:[\w-]+/)*[\w-]+\.(?:go|ts)):)?([A-Za-z_][A-Za-z0-9_]*)(\(\))?$`)
	// snakeCase is a column, a config key or a wire field, never a symbol.
	snakeCase = regexp.MustCompile(`^[a-z0-9]+(?:_[a-z0-9]+)+$`)
	// fileSuffix is a dotted token's last part that makes it a file name, as
	// `server.yaml` or `gridwell.db`, rather than pkg.Name.
	fileSuffix = map[string]bool{
		"db": true, "yaml": true, "yml": true, "sock": true, "md": true, "go": true, "ts": true, "js": true,
		"json": true, "txt": true, "sh": true, "mod": true, "sum": true, "work": true, "proto": true,
		"html": true, "css": true, "log": true, "toml": true, "mjs": true, "exe": true, "dmg": true, "png": true,
	}
)

// TestEveryDocIdentifierResolves: a doc that names a symbol names one the
// tree declares, so a rename cannot leave a doc pointing at nothing.
func TestEveryDocIdentifierResolves(t *testing.T) {
	for _, at := range unresolvedDocIdentifiers(t, repoRoot(t)) {
		t.Errorf("%s names no symbol: rename it to the one that exists, or list it in scripts/docids-allow.txt with why it is not one", at)
	}
}

// unresolvedDocIdentifiers are the backticked doc symbols nothing declares,
// once per doc, less the tokens scripts/docids-allow.txt names as not being
// symbols. A pkg that names a package, this tree's or one it imports, must
// declare Name; one that names none is a value, as `a.gridWritable`, and
// resolves when anything declares Name.
func unresolvedDocIdentifiers(t *testing.T, root string) []string {
	decls := declarations(t, root, false)
	allowed := docIDsAllowed(t, root)
	type docToken struct {
		rel, tok, name string
		pkgs           []string // the packages that must declare name; none for a value's member
	}
	var toks []docToken
	wanted := map[string]bool{}
	for _, doc := range ratchetDocs(t, root) {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, doc)
		rel = filepath.ToSlash(rel)
		seen := map[string]bool{}
		for _, m := range backticked.FindAllStringSubmatch(string(data), -1) {
			tok := m[1]
			s := docSymbol.FindStringSubmatch(tok)
			if s == nil || seen[tok] || allowed[rel+" "+tok] {
				continue
			}
			pkg, file, name, call := s[1], s[2], s[3], s[4] != ""
			if pkg == "" && file == "" && !call || snakeCase.MatchString(name) || pkg != "" && fileSuffix[name] {
				continue
			}
			var pkgs []string
			if file != "" {
				// A file this tree does not hold is another repository's.
				if pkgs = decls.fileKeys(file); pkgs == nil {
					continue
				}
			} else if pkg != "" {
				pkgs = []string{pkg}
				wanted[pkg] = true
			}
			seen[tok] = true
			toks = append(toks, docToken{rel, tok, name, pkgs})
		}
	}
	dependencyDecls(t, root, decls, wanted)
	var out []string
	for _, d := range toks {
		isPkg, resolved := false, false
		for _, p := range d.pkgs {
			if names, ok := decls.byPkg[p]; ok {
				isPkg = true
				resolved = resolved || names[d.name]
			}
		}
		if !isPkg {
			resolved = decls.all[d.name]
		}
		if !resolved {
			out = append(out, d.rel+": `"+d.tok+"`")
		}
	}
	return out
}

// fileKeys are the byPkg keys of the tree's files whose path ends in file.
func (d declaredNames) fileKeys(file string) []string {
	var out []string
	for rel, key := range d.files {
		if rel == file || strings.HasSuffix(rel, "/"+file) {
			out = append(out, key)
		}
	}
	return out
}

// docIDsAllowed reads scripts/docids-allow.txt: "<doc> <token>" per line, a
// backticked token that is not a symbol of this tree.
func docIDsAllowed(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, fields := range allowLines(t, filepath.Join(root, "scripts", "docids-allow.txt")) {
		out[strings.Join(strings.Fields(fields), " ")] = true
	}
	return out
}

// allowLines are an allow file's entries, comments and blanks dropped.
func allowLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// An allowed doc token names a doc that still spells it.
func TestDocIDsAllowedAreLive(t *testing.T) {
	root := repoRoot(t)
	for entry := range docIDsAllowed(t, root) {
		doc, tok, _ := strings.Cut(entry, " ")
		data, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil || !bytes.Contains(data, []byte("`"+tok+"`")) {
			t.Errorf("scripts/docids-allow.txt: %s no longer names `%s`; delete the entry", doc, tok)
		}
	}
}

// productionPackages are the directories holding a non-test Go file of the
// production tree. A directory, not `go list`, because client/wasm builds
// only for js and a listing for the host would drop it.
func productionPackages(t *testing.T, root string) []string {
	dirs := map[string]bool{}
	walkRepo(t, root, func(rel, path string, data []byte) {
		if strings.HasSuffix(rel, ".go") && !isTestFile(rel) && isProduction(rel) {
			dirs[filepath.Dir(rel)] = true
		}
	})
	var out []string
	for d := range dirs {
		out = append(out, d)
	}
	return out
}

// traceBudgetFile is the one owner of the trace budget. The boundary test
// cannot run the e2e gate, so it reads the number this file records, and
// apps/desktop/e2e/trace-budget.spec.ts, which fails when its script's run
// leaves another count, is what keeps the number true.
const traceBudgetFile = "apps/desktop/e2e/trace-budget.json"

// traceBudget is the count of trace records the script's run owes to no
// gesture, event, view or decision, as trace-budget.json records it.
func traceBudget(t *testing.T, root string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, traceBudgetFile))
	if err != nil {
		t.Fatal(err)
	}
	var budget struct {
		Unattributed *int `json:"unattributed"`
	}
	if err := json.Unmarshal(data, &budget); err != nil || budget.Unattributed == nil {
		t.Fatalf("%s: want {\"unattributed\": N}: %v", traceBudgetFile, err)
	}
	return *budget.Unattributed
}

// TestTraceBudgetIsZero holds the trace budget at the floor it was first
// measured at, so it is a permanent test rather than a ledger row.
func TestTraceBudgetIsZero(t *testing.T) {
	if n := traceBudget(t, repoRoot(t)); n != 0 {
		t.Errorf("%s records %d unattributed records: attribute each to a gesture, an event, a view or a decision in CLAUDE.md, or stop the work", traceBudgetFile, n)
	}
}

// productionLines counts the lines of the tree scripts/comment-share.sh reads.
func productionLines(t *testing.T, root string) int {
	n := 0
	walkRepo(t, root, func(rel, path string, data []byte) {
		if isTestFile(rel) || !isProduction(rel) {
			return
		}
		n += bytes.Count(data, []byte("\n"))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			n++
		}
	})
	return n
}

// parityCells is the parity table's count as TestParity/count logs it. The
// table is Go data beside its checks in internal/server, so the count is read
// off a run of it rather than spelled a second time here.
func parityCells(t *testing.T, root string) int {
	return loggedCount(t, root, "./internal/server/", "TestParity", "parity count")
}

// axesUncovered is the flags rpc.Flags classes axis with no parity carrier,
// as TestFlags/count logs it.
func axesUncovered(t *testing.T, root string) int {
	return loggedCount(t, filepath.Join(root, "api"), "./rpc/", "TestFlags", "axes count")
}

// loggedCount is the number test's count subtest logs after label, run in pkg
// from dir, since this module imports nothing of the tree's.
func loggedCount(t *testing.T, dir, pkg, test, label string) int {
	cmd := exec.Command("go", "test", "-count=1", "-run", "^"+test+"$/^count$", "-v", pkg)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s/count: %v\n%s", test, err, out)
	}
	m := regexp.MustCompile(label + `: (\d+)`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("%s/count logged no count:\n%s", test, out)
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestParityIsZero holds the parity count at its floor, so it is a permanent
// test rather than a ledger row.
func TestParityIsZero(t *testing.T) {
	if n := parityCells(t, repoRoot(t)); n != 0 {
		t.Errorf("internal/server/parity_test.go counts %d: write the check for each same cell, and fill each hole with a decision in CLAUDE.md or a fix that makes it same", n)
	}
}

// TestNoUncarriedFlags holds every wire flag that changes behaviour to a
// parity dimension that varies on it (rpc.Flags); a new flag is a column
// nobody filled until a cell varies on it.
func TestNoUncarriedFlags(t *testing.T) {
	if n := axesUncovered(t, repoRoot(t)); n != 0 {
		t.Errorf("api/rpc/axes.go has %d flags no parity cell varies on: give each a variant in internal/server/parity_test.go, or reclassify it as presentation with the reason", n)
	}
}
