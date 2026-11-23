package boundary

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The controls table (controls.txt) is the one list of what in the page takes
// a press: every element client/wasm gives a press listener or makes as a
// native control, and every native control web/index.html declares. Each row
// is pressed with a finger and with a mouse by web-every-control.spec.ts,
// which reads the same file, so a control no device has pressed in a gate
// cannot be written (Joe, 2026-11-22: "Instead we should have tests for these
// cases").

// controlsSpec is the gate that presses every row.
const controlsSpec = "apps/desktop/e2e-web/web-every-control.spec.ts"

// pressEvents are the DOM events a press is heard by.
var pressEvents = map[string]bool{
	"mousedown": true, "mouseup": true, "click": true, "dblclick": true, "auxclick": true,
	"contextmenu": true, "submit": true, "touchstart": true, "touchend": true, "touchcancel": true,
	"pointerdown": true, "pointerup": true, "pointercancel": true,
}

// pressHelpers install a press on their first argument. Their own bodies are
// not sites: the call is.
var pressHelpers = map[string]bool{
	"installOverlayMouse": true, "installOverlayTouch": true, "installTextareaTouch": true,
}

// nativeControls are the tags a browser presses with no listener of ours.
var nativeControls = map[string]bool{"input": true, "textarea": true, "button": true, "select": true, "a": true}

// controlRow is one row of controls.txt.
type controlRow struct {
	name, onScreen, press, devices string
	sites                          []string
	line                           int
}

func (r controlRow) control() bool { return !strings.HasPrefix(r.devices, "not a control") }

func readControls(t *testing.T, root string) []controlRow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "test", "boundary", "controls.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []controlRow
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, " | ")
		if len(f) != 5 {
			t.Fatalf("controls.txt:%d: want name | on screen | a press does | devices | sites, got %d fields", i+1, len(f))
		}
		r := controlRow{name: f[0], onScreen: f[1], press: f[2], devices: f[3], line: i + 1}
		for _, s := range strings.Split(f[4], ";") {
			if s = strings.TrimSpace(s); s != "" {
				r.sites = append(r.sites, s)
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// pressSites are the places the page installs a press, one key each:
// "<file> <function> <target> <event>" for client/wasm, with the tag in angle
// brackets for a native control it makes, and "index.html #<id> <tag>" for a
// native control web/index.html declares.
func pressSites(t *testing.T, root string) map[string]bool {
	t.Helper()
	sites := map[string]bool{}
	dir := filepath.Join(root, "client", "wasm")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	expr := func(e ast.Expr) string {
		var b bytes.Buffer
		if err := printer.Fprint(&b, fset, e); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	lit := func(e ast.Expr) string {
		if l, ok := e.(*ast.BasicLit); ok && l.Kind == token.STRING {
			s, _ := strconv.Unquote(l.Value)
			return s
		}
		return ""
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || pressHelpers[fd.Name.Name] {
				continue
			}
			site := func(target ast.Expr, event string) {
				sites[fmt.Sprintf("%s %s %s %s", name, fd.Name.Name, expr(target), event)] = true
			}
			// A native control's site is the variable it is assigned to.
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
					if call, ok := as.Rhs[0].(*ast.CallExpr); ok && len(call.Args) > 0 {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "element" {
							if tag := lit(call.Args[0]); nativeControls[tag] {
								site(as.Lhs[0], "<"+tag+">")
							}
						}
					}
				}
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					if fn.Name == "listen" && len(call.Args) >= 2 && pressEvents[lit(call.Args[1])] {
						site(call.Args[0], lit(call.Args[1]))
					}
				case *ast.SelectorExpr:
					switch {
					case pressHelpers[fn.Sel.Name]:
						site(call.Args[0], fn.Sel.Name)
					case fn.Sel.Name == "Call" && lit(call.Args[0]) == "addEventListener" && len(call.Args) >= 2 &&
						pressEvents[lit(call.Args[1])]:
						site(fn.X, lit(call.Args[1]))
					}
				}
				return true
			})
		}
	}
	html, err := os.ReadFile(filepath.Join(root, "web", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	tagRe := regexp.MustCompile(`<([a-z]+)\b([^>]*)>`)
	idRe := regexp.MustCompile(`\bid="([^"]+)"`)
	for _, m := range tagRe.FindAllStringSubmatch(string(html), -1) {
		if !nativeControls[m[1]] {
			continue
		}
		id := idRe.FindStringSubmatch(m[2])
		if id == nil {
			t.Errorf("web/index.html: a <%s> with no id, so no row of controls.txt can name it", m[1])
			continue
		}
		sites[fmt.Sprintf("index.html #%s <%s>", id[1], m[1])] = true
	}
	return sites
}

// Every press the page installs is a row's, every row's sites are installed,
// and every control row is a case of the gate that presses it.
func TestEveryControlIsARow(t *testing.T) {
	root := repoRoot(t)
	rows := readControls(t, root)
	sites := pressSites(t, root)
	claimed := map[string]bool{}
	names := map[string]bool{}
	spec, err := os.ReadFile(filepath.Join(root, controlsSpec))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if names[r.name] {
			t.Errorf("controls.txt:%d: a second row named %q", r.line, r.name)
		}
		names[r.name] = true
		if len(r.sites) == 0 {
			t.Errorf("controls.txt:%d: %s names no site", r.line, r.name)
		}
		for _, s := range r.sites {
			claimed[s] = true
			if !sites[s] {
				t.Errorf("controls.txt:%d: %s names %q, which nothing installs", r.line, r.name, s)
			}
		}
		if r.devices != "same" && !strings.HasPrefix(r.devices, "differs: ") && !strings.HasPrefix(r.devices, "not a control: ") {
			t.Errorf("controls.txt:%d: devices is same, differs: <the decision>, or not a control: <why>; got %q", r.line, r.devices)
		}
		q := regexp.QuoteMeta(r.name)
		if r.control() && !regexp.MustCompile(`(?m)^\s*(`+q+`|'`+q+`'|"`+q+`"):`).Match(spec) {
			t.Errorf("controls.txt:%d: %s has no case in %s", r.line, r.name, controlsSpec)
		}
	}
	var missing []string
	for s := range sites {
		if !claimed[s] {
			missing = append(missing, s)
		}
	}
	sort.Strings(missing)
	for _, s := range missing {
		t.Errorf("%s: a press no row of test/boundary/controls.txt names; add the control, and its case in %s", s, controlsSpec)
	}
}
