package gwerr

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		err  error
		want ErrorClass
	}{
		{ErrNotFound, ClassNotFound},
		{ErrInvalidArgument, ClassInvalidArgument},
		{ErrInvalidPath, ClassInvalidArgument},
		{ErrNotURLTile, ClassInvalidArgument},
		{ErrNotTextTile, ClassInvalidArgument},
		{ErrNotWellTile, ClassInvalidArgument},
		{ErrNotShellTile, ClassInvalidArgument},
		{ErrOverlap, ClassConflict},
		{ErrVersionConflict, ClassConflict},
		{ErrSchemaDivergence, ClassInternal},
		{nil, ClassInternal},
		{errors.New("anything else"), ClassInternal},
		{fmt.Errorf("moving tile 7: %w", ErrOverlap), ClassConflict},
		{fmt.Errorf("resolving path: %w", ErrNotFound), ClassNotFound},
	}
	for _, c := range cases {
		if got := ClassifyError(c.err); got != c.want {
			t.Errorf("ClassifyError(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// An unclassified sentinel degrades to Internal.
func TestEverySentinelIsClassified(t *testing.T) {
	declared := declaredSentinelNames(t)
	if len(declared) == 0 {
		t.Fatal("found no Err* sentinel declarations; the source scan is broken")
	}
	classified := make(map[string]bool, len(sentinelClasses))
	for _, s := range sentinelClasses {
		classified[s.Err.Error()] = true
	}
	for name, msg := range declared {
		if !classified[msg] {
			t.Errorf("sentinel %s is declared but missing from sentinelClasses — assign it a class in classify.go (ClassInternal is a valid, deliberate choice)", name)
		}
	}
	if len(declared) != len(sentinelClasses) {
		t.Errorf("declared %d sentinels but sentinelClasses has %d entries", len(declared), len(sentinelClasses))
	}
}

// declaredSentinelNames reads the package source, so a new sentinel is
// caught without being listed here too.
func declaredSentinelNames(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Err") || !ast.IsExported(name.Name) || i >= len(vs.Values) {
						continue
					}
					call, ok := vs.Values[i].(*ast.CallExpr)
					if !ok {
						continue
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "New" || len(call.Args) != 1 {
						continue
					}
					if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						out[name.Name] = strings.Trim(lit.Value, `"`)
					}
				}
			}
		}
	}
	return out
}

// A coded answer is never a transport failure.
func TestIsTransportPinsWireCodes(t *testing.T) {
	for _, c := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.Canceled} {
		if !IsTransport(status.Error(c, "x")) {
			t.Errorf("%v must be transport", c)
		}
	}
	for _, c := range []codes.Code{codes.NotFound, codes.InvalidArgument, codes.FailedPrecondition, codes.Unimplemented, codes.Internal, codes.OK} {
		if IsTransport(status.Error(c, "x")) {
			t.Errorf("%v must be an answer", c)
		}
	}
	if IsTransport(nil) || IsTransport(errors.New("plain")) {
		t.Error("nil and non-status errors are not transport failures")
	}
}

// The class table is total and ToStatus is what both hops read: a sentinel
// becomes its class's code, a status error is not re-coded, and an
// unclassified error passes through as its own Unknown.
func TestStatusCodeIsTotal(t *testing.T) {
	for _, c := range []ErrorClass{ClassInternal, ClassNotFound, ClassInvalidArgument, ClassConflict} {
		if _, ok := classCodes[c]; !ok {
			t.Errorf("class %d has no status code", c)
		}
	}
	if StatusCode(ErrorClass(99)) != codes.Internal {
		t.Error("an unknown class must read as Internal")
	}
	for _, s := range sentinelClasses {
		want := StatusCode(s.Class)
		if s.Class == ClassInternal {
			want = codes.Unknown
		}
		if got := status.Code(ToStatus(fmt.Errorf("tile 7: %w", s.Err))); got != want {
			t.Errorf("ToStatus(%v) code = %v, want %v", s.Err, got, want)
		}
	}
	own := status.Error(codes.Unavailable, "dark")
	if ToStatus(own) != own {
		t.Error("a status error must pass through untouched")
	}
	if ToStatus(nil) != nil {
		t.Error("nil must stay nil")
	}
	if plain := errors.New("boom"); ToStatus(plain) != plain {
		t.Error("an unclassified error must pass through as itself")
	}
}
