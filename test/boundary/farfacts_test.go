package boundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// What a connection API verb asks of a far node is that node's home, never
// its roster: a roster holds the far node's own connections, which ask back,
// so two nodes that declare each other would ask round until a deadline. The
// connection's row reads the far Info (connection.Server.Rows), and the
// connection API's Info answers from home (router.Info); neither calls a
// Handshake.
func TestFarFactsComeFromInfoNotTheRoster(t *testing.T) {
	root := repoRoot(t)
	for _, c := range []struct{ file, fn string }{
		{"internal/connection/connection.go", "Rows"},
		{"internal/server/router.go", "Info"},
	} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, c.file), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != c.fn {
				continue
			}
			found = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Handshake" {
					t.Errorf("%s: %s calls Handshake: ask the far node's Info, which answers from its home", fset.Position(sel.Pos()), c.fn)
				}
				return true
			})
		}
		if !found {
			t.Errorf("%s declares no method %s; move this pin with it", c.file, c.fn)
		}
	}
}
