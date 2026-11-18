package boundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A connection's health is derived in one place (connection.derive) from
// facts written in one place (connection.Server.setHealth): no other function
// in internal/connection builds a connState or writes the facts, so no
// answer can overwrite another writer's news, as the Info that answered
// while the stream was down once did.
func TestOneConnectionHealthDeriver(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "connection")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
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
				switch n := n.(type) {
				case *ast.CompositeLit:
					if id, ok := n.Type.(*ast.Ident); ok && id.Name == "connState" && fd.Name.Name != "derive" {
						t.Errorf("%s: %s builds a connState: derive is its one owner", fset.Position(n.Pos()), fd.Name.Name)
					}
				case *ast.AssignStmt:
					for _, l := range n.Lhs {
						if ix, ok := l.(*ast.IndexExpr); ok {
							if sel, ok := ix.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "facts" && fd.Name.Name != "setHealth" {
								t.Errorf("%s: %s writes a connection's facts: write them through setHealth", fset.Position(n.Pos()), fd.Name.Name)
							}
						}
					}
				}
				return true
			})
		}
	}
}
