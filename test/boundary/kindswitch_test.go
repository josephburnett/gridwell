package boundary

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"testing"
)

var kindConsts = map[string]bool{"KindText": true, "KindURL": true, "KindShell": true, "KindDoor": true, "KindWorkspace": true}

// No production Go file outside api/rpc/types.go asks which kind a tile is:
// a site asks the kind table (rpc.KindOf) what the kind can do, so a new kind
// is one row and no site learns its name.
func TestNoKindSwitches(t *testing.T) {
	for _, s := range kindSwitches(t, repoRoot(t)) {
		t.Errorf("%s: names a tile kind; read a capability on rpc.KindOf instead", s)
	}
}

// kindSwitches are the case clauses, ==/!= comparisons and composite-literal
// keys that name a tile kind outside the kind table, each a dispatch a
// capability on the table answers once.
func kindSwitches(t *testing.T, root string) []string {
	var out []string
	productionGo(t, root, func(rel string, fset *token.FileSet, f *ast.File) {
		if filepath.ToSlash(rel) == "api/rpc/types.go" {
			return
		}
		local := filepath.ToSlash(filepath.Dir(rel)) == "api/rpc"
		rpc := importName(f, rpcImport)
		isKind := func(e ast.Expr) bool {
			switch e := e.(type) {
			case *ast.SelectorExpr:
				x, ok := e.X.(*ast.Ident)
				return ok && rpc != "" && x.Name == rpc && kindConsts[e.Sel.Name]
			case *ast.Ident:
				return local && kindConsts[e.Name]
			}
			return false
		}
		at := func(n ast.Node) { out = append(out, fmt.Sprintf("%s:%d", rel, fset.Position(n.Pos()).Line)) }
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CaseClause:
				for _, e := range n.List {
					if isKind(e) {
						at(n)
						break
					}
				}
			case *ast.BinaryExpr:
				if (n.Op == token.EQL || n.Op == token.NEQ) && (isKind(n.X) || isKind(n.Y)) {
					at(n)
				}
			case *ast.KeyValueExpr:
				if isKind(n.Key) {
					at(n)
				}
			}
			return true
		})
	})
	return out
}
