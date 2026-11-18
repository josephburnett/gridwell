package boundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// claimAtSendWaiting names the send closures that still read the cache when
// they go out, as "file:function", each owned by a change not yet landed.
// The list only shrinks.
var claimAtSendWaiting = map[string]string{
	// The ephemeral cleanup claims the row as the cache holds it at send
	// (CLAUDE.md, the ephemeral visit decision), so a foreign bump the cache
	// heard is claimed too; ephemeral.go's owner.
	"ephemeral.go:deleteEphemeralTile": "ephemeral cleanup claims the cached row",
}

// A write claims what the user saw when the gesture read it (CLAUDE.md, the
// claims decision): a closure the dispatcher sends, and sends again on every
// outbox retry, never reads the cache, where a foreign writer's bump would
// become its claim. The claim is taken at the gesture and carried, or kept by
// an owner that only this client's own landings move (outbox.Claims,
// pane.Levels.Claim).
func TestNoClaimReadAtSend(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "client", "wasm")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	// Cache reads a send may make: its own write's bracket, and the
	// selection's pending set, which is the payload of a write that claims
	// nothing.
	allowed := map[string]bool{"BeginWrite": true, "SelectionUp": true}
	var found []string
	fs := token.NewFileSet()
	parsed := map[string]*ast.File{}
	methods := map[string]*ast.BlockStmt{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fs, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed[filepath.Base(f)] = af
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil && fd.Body != nil {
				methods[fd.Name.Name] = fd.Body
			}
		}
	}
	for name, af := range parsed {
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			site := name + ":" + fd.Name.Name
			for _, body := range sendClosures(fd.Body, methods) {
				reads := cacheReads(body, allowed)
				if len(reads) == 0 {
					continue
				}
				if _, ok := claimAtSendWaiting[site]; ok {
					found = append(found, site)
					continue
				}
				t.Errorf("client/wasm/%s: a write's send reads the cache (%s): take the claim at the gesture and carry it",
					site, strings.Join(reads, ", "))
			}
		}
	}
	sort.Strings(found)
	for site := range claimAtSendWaiting {
		if i := sort.SearchStrings(found, site); i == len(found) || found[i] != site {
			t.Errorf("%s no longer reads the cache at send: delete it from claimAtSendWaiting", site)
		}
	}
}

// sendClosures is the bodies of every `call` and `beacon` func literal in a
// write literal under body, with each local func literal and App method they
// call, one level down.
func sendClosures(body *ast.BlockStmt, methods map[string]*ast.BlockStmt) []*ast.BlockStmt {
	var out []*ast.BlockStmt
	ast.Inspect(body, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if id, ok := cl.Type.(*ast.Ident); !ok || id.Name != "write" {
			return true
		}
		for _, e := range cl.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || (key.Name != "call" && key.Name != "beacon") {
				continue
			}
			ast.Inspect(kv.Value, func(m ast.Node) bool {
				switch x := m.(type) {
				case *ast.FuncLit:
					out = append(out, x.Body)
				case *ast.CallExpr:
					if lit := localFuncLit(x.Fun); lit != nil {
						out = append(out, lit.Body)
					}
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "a" && methods[sel.Sel.Name] != nil {
							out = append(out, methods[sel.Sel.Name])
						}
					}
				}
				return true
			})
		}
		return true
	})
	return out
}

// localFuncLit is the func literal a local variable called as fn() was
// assigned, or nil.
func localFuncLit(fn ast.Expr) *ast.FuncLit {
	id, ok := fn.(*ast.Ident)
	if !ok || id.Obj == nil {
		return nil
	}
	as, ok := id.Obj.Decl.(*ast.AssignStmt)
	if !ok {
		return nil
	}
	for i, l := range as.Lhs {
		if li, ok := l.(*ast.Ident); ok && li.Name == id.Name && i < len(as.Rhs) {
			lit, _ := as.Rhs[i].(*ast.FuncLit)
			return lit
		}
	}
	return nil
}

// cacheReads is each a.cachedTileByID and a.c.<method> body calls, but the
// allowed methods.
func cacheReads(body *ast.BlockStmt, allowed map[string]bool) []string {
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "a" && sel.Sel.Name == "cachedTileByID" {
			out = append(out, "a.cachedTileByID")
		}
		if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "c" && !allowed[sel.Sel.Name] {
			if recv, ok := inner.X.(*ast.Ident); ok && recv.Name == "a" {
				out = append(out, "a.c."+sel.Sel.Name)
			}
		}
		return true
	})
	return out
}
