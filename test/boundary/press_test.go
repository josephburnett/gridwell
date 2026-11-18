package boundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// gestureArms are the App fields a press arms: each holds a gesture in flight.
var gestureArms = map[string]bool{
	"dragging": true, "rightDrag": true, "leftResize": true, "band": true, "stripScroll": true,
}

// A release, a mouseup, a move reporting the button already up or a view's
// forwarded right-up, is finished in one place, App.finishRelease, which moves
// the gesture to the point the release was learned at before its commit path
// runs: no other function in client/wasm calls a commit path, so no release
// can land at another point.
func TestOneReleaseFinisher(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "client", "wasm")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	commits := map[string]bool{"finishLeftResize": true, "finishRightDrag": true, "finishLeftDrag": true, "finishBand": true}
	fset := token.NewFileSet()
	seen := 0
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !commits[sel.Sel.Name] {
					return true
				}
				seen++
				if fd.Name.Name != "finishRelease" {
					t.Errorf("%s: %s calls %s: finish a release through App.finishRelease", fset.Position(call.Pos()), fd.Name.Name, sel.Sel.Name)
				}
				return true
			})
		}
	}
	if seen != len(commits) {
		t.Errorf("found %d calls of the %d commit paths: the walker no longer sees them", seen, len(commits))
	}
}

// pressFree are the functions whose path to an arm no press can take, each
// with the reason.
var pressFree = map[string]string{
	// Its touch Select is a shift + left press on the bar, which
	// wsbar.RouteClick routes to a select or to nothing, never to a drag.
	"dispatchTouchActions": "the Select arm never arms",
}

// A gesture owns the pointer from its press to its own button's release, so
// what a press does with a gesture in flight is gesture.Press's (CLAUDE.md
// principle 6). Every function in client/wasm that arms a gesture is reached
// only through one that asked gesture.Press: walking the package's references
// back from each arm, every path to a function nothing references passes
// through a caller of gesture.Press.
func TestEveryArmAsksPress(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "client", "wasm")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	callers := map[string]map[string]bool{} // name -> the functions that reference it
	asks := map[string]bool{}
	arms := map[string]string{} // function -> where it arms
	funcs := map[string]bool{}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			fn := fd.Name.Name
			funcs[fn] = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					if x, ok := n.X.(*ast.Ident); ok && x.Name == "gesture" && n.Sel.Name == "Press" {
						asks[fn] = true
					}
					if callers[n.Sel.Name] == nil {
						callers[n.Sel.Name] = map[string]bool{}
					}
					callers[n.Sel.Name][fn] = true
				case *ast.Ident:
					if callers[n.Name] == nil {
						callers[n.Name] = map[string]bool{}
					}
					callers[n.Name][fn] = true
				case *ast.AssignStmt:
					for i, lhs := range n.Lhs {
						sel, ok := lhs.(*ast.SelectorExpr)
						if !ok || !gestureArms[sel.Sel.Name] || i >= len(n.Rhs) {
							continue
						}
						if id, ok := n.Rhs[i].(*ast.Ident); ok && id.Name == "nil" {
							continue
						}
						if _, ok := n.Rhs[i].(*ast.UnaryExpr); ok {
							arms[fn] = fset.Position(n.Pos()).String()
						}
					}
				}
				return true
			})
		}
	}
	if len(arms) == 0 {
		t.Fatal("found no arm in client/wasm: the walker no longer sees the gesture fields")
	}
	var bad []string
	for fn, at := range arms {
		seen := map[string]bool{}
		var unasked func(f string) []string
		unasked = func(f string) []string {
			if asks[f] || seen[f] || pressFree[f] != "" {
				return nil
			}
			seen[f] = true
			var from []string
			for c := range callers[f] {
				if c != f && funcs[c] {
					from = append(from, c)
				}
			}
			if len(from) == 0 {
				return []string{f}
			}
			var out []string
			for _, c := range from {
				out = append(out, unasked(c)...)
			}
			return out
		}
		for _, r := range unasked(fn) {
			bad = append(bad, at+" ("+fn+") is reached from "+r+", which never asks gesture.Press")
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}
