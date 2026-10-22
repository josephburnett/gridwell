package boundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A log-only arm is a log call handed an error in a block that neither
// returns an error nor calls a surface: a failure the user never hears of.
// trace.Emit is no surface, since a record is diagnostics.

// logFunc matches a log function a package builds for itself, as the shim's
// shellLog, urlLog and urlConsole, or a plugin's Logf.
var logFunc = regexp.MustCompile(`(?:Log|[Ll]ogf|Console)$`)

// notLogPkgs spell a Log that is no log: math.Log, traceevent.Log's record.
var notLogPkgs = map[string]bool{"math": true, "traceevent": true}

// surfacePkgFuncs are the package functions whose call reaches a client: a
// health event, the cache-less node's notice, an HTTP status, a shell exit.
var surfacePkgFuncs = map[string]bool{
	"rpc.HealthEvent": true, "sourcecache.Unavailable": true, "http.Error": true,
	"shellwire.EncodeExit": true, "shellwire.EncodeOtherBuild": true,
}

// surfaceNames are the functions and methods, by name, whose call reaches a
// client: eventhub.Hub.Publish; the health and grid events of
// sourcecache.Layer and pluginhost.Adapter; plugin.Supervisor.setHealth;
// connection.Server.note; namespace.Refollow.Down; the router's reportHealth;
// an HTTP status; the shim's App.reportErr onto client/errsurface.
var surfaceNames = map[string]bool{
	"Publish": true, "emitHealth": true, "emitGridChanged": true, "setHealth": true,
	"note": true, "Down": true, "reportHealth": true, "WriteHeader": true, "reportErr": true,
}

func isSurface(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return surfaceNames[fn.Name]
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok && (surfacePkgFuncs[x.Name+"."+fn.Sel.Name] || x.Name == "errsurface") {
			return true
		}
		if fn.Sel.Name == "Close" && len(call.Args) > 0 {
			// A websocket closed with a status tells the client why.
			if s, ok := call.Args[0].(*ast.SelectorExpr); ok {
				if x, ok := s.X.(*ast.Ident); ok && x.Name == "websocket" && strings.HasPrefix(s.Sel.Name, "Status") {
					return true
				}
			}
		}
		return surfaceNames[fn.Sel.Name]
	}
	return false
}

func calleeName(call *ast.CallExpr) (pkg, name string) {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return "", fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name, fn.Sel.Name
		}
		return "", fn.Sel.Name
	}
	return "", ""
}

// isErrIdent is an identifier spelled as an error: err, or a name ending in
// err or Err (terr, writeErr).
func isErrIdent(e ast.Expr) (string, bool) {
	id, ok := e.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, strings.HasSuffix(strings.ToLower(id.Name), "err")
}

// errArgs are the error identifiers a call's arguments carry, as themselves
// or as x.Error(); hasErr is also true for an x.Err or an Err() call.
func errArgs(call *ast.CallExpr) (names []string, hasErr bool) {
	for _, a := range call.Args {
		ast.Inspect(a, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncLit:
				return false
			case *ast.Ident:
				if name, ok := isErrIdent(n); ok {
					names, hasErr = append(names, name), true
				}
			case *ast.SelectorExpr:
				if n.Sel.Name == "Err" || n.Sel.Name == "err" || n.Sel.Name == "Error" {
					hasErr = true
				}
			}
			return true
		})
	}
	return names, hasErr
}

type logPkg struct {
	funcs   map[string][]*ast.FuncDecl
	helpers map[string]bool // functions whose whole body is a log of their error
}

// isLogCall is log.Print, Printf or Println, a package's own log function, or
// a call to one of its log helpers.
func (p logPkg) isLogCall(call *ast.CallExpr) bool {
	pkg, name := calleeName(call)
	switch {
	case pkg == "log":
		return name == "Print" || name == "Printf" || name == "Println"
	case notLogPkgs[pkg]:
		return false
	case pkg == "" && p.helpers[name]:
		return true
	}
	return logFunc.MatchString(name)
}

// isLogHelper is a function whose body is nothing but logs, bare or under an
// if: its sites are judged at its callers, where the error has an arm.
func (p logPkg) isLogHelper(fn *ast.FuncDecl) bool {
	logs := func(stmts []ast.Stmt) bool {
		for _, s := range stmts {
			e, ok := s.(*ast.ExprStmt)
			if !ok {
				return false
			}
			if c, ok := e.X.(*ast.CallExpr); !ok || !p.isLogCall(c) {
				return false
			}
		}
		return len(stmts) > 0
	}
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}
	for _, s := range fn.Body.List {
		if i, ok := s.(*ast.IfStmt); ok && i.Else == nil && logs(i.Body.List) {
			continue
		}
		if !logs([]ast.Stmt{s}) {
			return false
		}
	}
	return true
}

func returnsError(ft *ast.FuncType) bool {
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return false
	}
	id, ok := ft.Results.List[len(ft.Results.List)-1].Type.(*ast.Ident)
	return ok && id.Name == "error"
}

// inspectArm walks n without entering a function literal, whose code runs on
// another path.
func inspectArm(n ast.Node, visit func(ast.Node)) {
	ast.Inspect(n, func(m ast.Node) bool {
		if _, ok := m.(*ast.FuncLit); ok && m != n {
			return false
		}
		if m != nil {
			visit(m)
		}
		return true
	})
}

func callsSurface(n ast.Node) bool {
	found := false
	inspectArm(n, func(m ast.Node) {
		if c, ok := m.(*ast.CallExpr); ok && isSurface(c) {
			found = true
		}
	})
	return found
}

// returnsNonNil is whether n, in a function returning an error, returns one
// that is not nil, or, given names, returns one of those identifiers.
func returnsNonNil(n ast.Node, ft *ast.FuncType, names []string) bool {
	if !returnsError(ft) {
		return false
	}
	found := false
	inspectArm(n, func(m ast.Node) {
		r, ok := m.(*ast.ReturnStmt)
		if !ok || len(r.Results) == 0 {
			return
		}
		last := r.Results[len(r.Results)-1]
		if names == nil {
			if id, ok := last.(*ast.Ident); !ok || id.Name != "nil" {
				found = true
			}
			return
		}
		if id, ok := last.(*ast.Ident); ok {
			for _, name := range names {
				found = found || id.Name == name
			}
		}
	})
	return found
}

// surfacesItsErrors is a package function each of whose error returns sits in
// a block that calls a surface, so a caller's log of that error is beside one.
func (p logPkg) surfacesItsErrors(name string) bool {
	decls := p.funcs[name]
	for _, fn := range decls {
		if fn.Body == nil || !returnsError(fn.Type) {
			return false
		}
		some, all := false, true
		var stack []ast.Node
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			stack = append(stack, n)
			r, ok := n.(*ast.ReturnStmt)
			if !ok || len(r.Results) == 0 {
				return true
			}
			if id, ok := r.Results[len(r.Results)-1].(*ast.Ident); ok && id.Name == "nil" {
				return true
			}
			some = true
			all = all && callsSurface(innermostArm(stack))
			return true
		})
		if !some || !all {
			return false
		}
	}
	return len(decls) > 0
}

// innermostArm is the innermost block, case or select arm on the stack.
func innermostArm(stack []ast.Node) ast.Node {
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i].(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause:
			return stack[i]
		}
	}
	return nil
}

// fromSurfacingCall is whether one of names was assigned, in the if whose
// body arm is or in the statement just before that if, from a call to a
// package function that surfaces its errors.
func (p logPkg) fromSurfacingCall(stack []ast.Node, arm ast.Node, names []string) bool {
	var assigns []ast.Stmt
	for i := len(stack) - 1; i > 0; i-- {
		if stack[i] != arm {
			continue
		}
		ifs, ok := stack[i-1].(*ast.IfStmt)
		if !ok || ifs.Body != arm {
			return false
		}
		assigns = append(assigns, ifs.Init)
		if i >= 2 {
			if parent, ok := stack[i-2].(*ast.BlockStmt); ok {
				for j, s := range parent.List {
					if s == ifs && j > 0 {
						assigns = append(assigns, parent.List[j-1])
					}
				}
			}
		}
		break
	}
	for _, s := range assigns {
		as, ok := s.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		_, callee := calleeName(call)
		for _, l := range as.Lhs {
			if name, ok := isErrIdent(l); ok {
				for _, want := range names {
					if name == want && p.surfacesItsErrors(callee) {
						return true
					}
				}
			}
		}
	}
	return false
}

// logOnlyArms are the production tree's log-only arms.
func logOnlyArms(t *testing.T, root string) []timerSite {
	type file struct {
		rel   string
		fset  *token.FileSet
		f     *ast.File
		lines []string
	}
	byDir := map[string][]file{}
	walkRepo(t, root, func(rel, path string, data []byte) {
		if !strings.HasSuffix(rel, ".go") || isTestFile(rel) || !isProduction(rel) {
			return
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		byDir[filepath.Dir(rel)] = append(byDir[filepath.Dir(rel)], file{rel, fset, f, strings.Split(string(data), "\n")})
	})
	var out []timerSite
	for _, files := range byDir {
		p := logPkg{funcs: map[string][]*ast.FuncDecl{}, helpers: map[string]bool{}}
		for _, fl := range files {
			for _, d := range fl.f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok {
					p.funcs[fn.Name.Name] = append(p.funcs[fn.Name.Name], fn)
				}
			}
		}
		for _, fl := range files {
			for _, d := range fl.f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && p.isLogHelper(fn) {
					p.helpers[fn.Name.Name] = true
				}
			}
		}
		for _, fl := range files {
			for _, d := range fl.f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Recv == nil && p.helpers[fn.Name.Name] {
					continue
				}
				var stack []ast.Node
				ast.Inspect(fn, func(n ast.Node) bool {
					if n == nil {
						stack = stack[:len(stack)-1]
						return true
					}
					stack = append(stack, n)
					call, ok := n.(*ast.CallExpr)
					if !ok || !p.isLogCall(call) {
						return true
					}
					names, hasErr := errArgs(call)
					if !hasErr {
						return true
					}
					var ft *ast.FuncType
					var body ast.Node
					for i := len(stack) - 1; i >= 0 && ft == nil; i-- {
						switch f := stack[i].(type) {
						case *ast.FuncLit:
							ft, body = f.Type, f.Body
						case *ast.FuncDecl:
							ft, body = f.Type, f.Body
						}
					}
					arm := innermostArm(stack)
					if callsSurface(arm) || returnsNonNil(arm, ft, nil) ||
						len(names) > 0 && returnsNonNil(body, ft, names) ||
						p.fromSurfacingCall(stack, arm, names) {
						return true
					}
					line := fl.fset.Position(call.Pos()).Line
					near := fl.lines[line-1]
					if line > 1 {
						near = fl.lines[line-2] + "\n" + near
					}
					out = append(out, timerSite{fl.rel, line, fn.Name.Name, near})
					return true
				})
			}
		}
	}
	return out
}

type allowedLog struct {
	at, rel, anchor, owner string
}

func allowedLogs(t *testing.T, root string) []allowedLog {
	t.Helper()
	var out []allowedLog
	for _, l := range allowLines(t, filepath.Join(root, "scripts", "allowed-logs.txt")) {
		fields := strings.Split(l, "|")
		if len(fields) != 3 {
			t.Errorf("scripts/allowed-logs.txt: %q: want path:anchor | owner | reason", l)
			continue
		}
		where := strings.TrimSpace(fields[0])
		i := strings.LastIndex(where, ":")
		switch {
		case i < 0:
			t.Errorf("scripts/allowed-logs.txt: %q names no anchor", where)
			continue
		case strings.TrimSpace(fields[1]) == "":
			t.Errorf("scripts/allowed-logs.txt: %s names no owner", where)
		case strings.TrimSpace(fields[2]) == "":
			t.Errorf("scripts/allowed-logs.txt: %s gives no reason", where)
		}
		out = append(out, allowedLog{where, filepath.FromSlash(where[:i]), where[i+1:], strings.TrimSpace(fields[1])})
	}
	return out
}

// logHoles are the listed arms whose owner is hole: a failure the user can
// see that only logs.
func logHoles(t *testing.T, root string) int {
	n := 0
	for _, a := range allowedLogs(t, root) {
		if a.owner == "hole" {
			n++
		}
	}
	return n
}

// A failure that only logs looks to the user like it just disappeared, so
// every log-only arm names who reclaims it, or is counted as a hole.
func TestEveryLogOnlyArmIsOwned(t *testing.T) {
	root := repoRoot(t)
	allowed := allowedLogs(t, root)
	for _, s := range logOnlyArms(t, root) {
		listed := false
		for _, a := range allowed {
			listed = listed || a.rel == s.rel && s.anchoredBy(a.anchor)
		}
		if !listed {
			t.Errorf("%s:%d logs an error and nothing reaches the user: route it through a surface, or list it in scripts/allowed-logs.txt as path:anchor | owner | reason", filepath.ToSlash(s.rel), s.line)
		}
	}
}

// An allowed log names an arm that still only logs, so a surfaced or deleted
// arm's entry cannot vouch for the next one written beside it.
func TestAllowedLogsAreLive(t *testing.T) {
	root := repoRoot(t)
	sites := logOnlyArms(t, root)
	for _, a := range allowedLogs(t, root) {
		live := false
		for _, s := range sites {
			live = live || a.rel == s.rel && s.anchoredBy(a.anchor)
		}
		if !live {
			t.Errorf("scripts/allowed-logs.txt: %s anchors no log-only arm; delete the entry", a.at)
		}
	}
}
