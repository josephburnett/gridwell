package boundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// claimOwners are the store's comparators, one per claimed fact (CLAUDE.md,
// "one claim per fact"): the version, the placement and the freeze.
var claimOwners = []string{"ClaimPlacement", "ClaimVersion", "setFrozenTx"}

// The store builds the version conflict only in its claim comparators, so a
// write that claims a fact reads its verdict from the fact's one owner
// instead of comparing it again.
func TestTheStoreComparesEachClaimOnce(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "local", "store")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	fset := token.NewFileSet()
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				if id, ok := call.Args[1].(*ast.Ident); ok && id.Name == "ErrVersionConflict" {
					sites = append(sites, fn.Name.Name)
					if !slices.Contains(claimOwners, fn.Name.Name) {
						t.Errorf("%s: %s builds a version conflict; read the claim's verdict from its comparator (%v)",
							fset.Position(call.Pos()), fn.Name.Name, claimOwners)
					}
				}
				return true
			})
		}
	}
	slices.Sort(sites)
	if !slices.Equal(sites, claimOwners) {
		t.Errorf("version conflicts built in %v, want once in each of %v", sites, claimOwners)
	}
}
