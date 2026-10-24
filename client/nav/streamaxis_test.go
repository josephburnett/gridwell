package nav

// The stream axis: every read the client decides, run with this client's
// event stream open and with it down. A held grid is current only while the
// stream tells its changes (CLAUDE.md, Decisions: "Navigation asks for a grid
// or a row only when the cache does not hold it current"), so with the stream
// open a read of a held grid asks nothing and with it down it asks again. The
// count is the read decisions found in the code that no row runs, which
// test/boundary records off TestStreamAxis/count.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
)

// streamRow is one read decision: fn is the planner function that makes it,
// hold fills the cache as the read finds it, and asked reports that the plan
// asks the node.
type streamRow struct {
	name  string
	fn    string
	hold  func(c *cache.Cache)
	plan  func(t *testing.T, current map[string]bool) Plan
	asked func(Plan) bool
}

func holdGrids(ids ...string) func(*cache.Cache) {
	return func(c *cache.Cache) {
		for _, id := range ids {
			c.PutGrid(&gridwellv1.Grid{Id: id}, nil)
		}
	}
}

func awaits(kind RequestKind) func(Plan) bool {
	return func(p Plan) bool {
		return slices.ContainsFunc(p.Effects, func(e Effect) bool { return e.Kind == EffAwait && e.Request.Kind == kind })
	}
}

func asksGrid(p Plan) bool { return gridFetches(p) > 0 }

func streamAxis() []streamRow {
	var rows []streamRow
	for _, c := range currencyCases {
		rows = append(rows, streamRow{name: c.name, fn: c.fn, hold: holdGrids(c.grid), plan: c.plan, asked: asksGrid})
	}
	level := arranged(workspace())
	return append(rows, streamRow{
		name: "descending into a held body",
		fn:   "descendContent",
		hold: func(c *cache.Cache) {
			c.PutGrid(&gridwellv1.Grid{Id: "g1"}, []*gridwellv1.Tile{currencyText})
			c.PutFetchedContent("t1", []byte("held"), rpc.ContentBasis{}, c.AskContent("t1"))
		},
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(gridPane("pane1", "g1"))
			w.Door = &DoorWorld{}
			w.Current = current
			return New().Do(descendGesture("pane1", currencyText), w)
		},
		asked: func(p Plan) bool {
			return slices.ContainsFunc(p.Effects, func(e Effect) bool { return e.Kind == EffReadTileContent })
		},
	}, streamRow{
		name: "re-engaging a held row",
		fn:   "reEngage",
		hold: holdGrids("g1"),
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(engagedPane("pane1"))
			w.Engage = &EngageWorld{Tile: urlRowIn("g1")}
			w.Current = current
			return New().Do(reEngageGesture("pane1", "r1"), w)
		},
		asked: awaits(RequestGetTile),
	}, streamRow{
		name: "entering a held workspace tile",
		fn:   "enterLevel",
		hold: holdGrids("g1"),
		plan: func(t *testing.T, current map[string]bool) Plan {
			w := baseWorld(gridPane("pane1", "g1"))
			w.Level = &LevelWorld{Tile: level}
			w.Current = current
			return New().Do(enterGesture("pane1", level), w)
		},
		asked: awaits(RequestGetTile),
	})
}

// shimExecutors run a plan's effects, so the verdict their reads carry is
// the planner's (CLAUDE.md: "The planner decides, never the executor").
var shimExecutors = []string{"runNavEffect", "navAwait"}

// shimReads are the shim's read entry points: each asks the node.
var shimReads = []string{"fetchGrid", "refetchGrid", "fetchTileContent", "fetchTileByID", "fetchLinkTarget", "readTileByID", "fetchURLPreview"}

// shimHeld are the shim's lookups of one held grid, row or body.
var shimHeld = []string{"Grid", "TileContent", "cachedTileByID"}

// unrunReads are the read decisions no row runs: a planner function that
// asks for a grid, a row or a body by the currency predicate with no row, a
// read of World.Current outside World.current, and a function of client/wasm
// that looks up what the cache holds and asks the node, deciding between
// serving one and asking with a verdict of its own that no pure test can run.
func unrunReads(t *testing.T, rows []streamRow) []string {
	t.Helper()
	covered := map[string]bool{}
	for _, r := range rows {
		covered[r.fn] = true
	}
	var out []string
	eachFunc(t, ".", func(at string, fd *ast.FuncDecl) {
		decides, indexes := false, false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && slices.Contains([]string{"current", "fetchGrid", "fetchPaneGrid"}, sel.Sel.Name) {
					decides = true
				}
			case *ast.SelectorExpr:
				indexes = indexes || n.Sel.Name == "Current"
			}
			return true
		})
		switch {
		case slices.Contains([]string{"current", "fetchGrid", "fetchPaneGrid"}, fd.Name.Name):
		case indexes:
			out = append(out, at+": reads World.Current outside World.current")
		case decides && !covered[fd.Name.Name]:
			out = append(out, at+": a planner read with no row")
		}
	})
	eachFunc(t, filepath.Join("..", "wasm"), func(at string, fd *ast.FuncDecl) {
		if slices.Contains(shimExecutors, fd.Name.Name) {
			return
		}
		asks, held := slices.Contains(shimReads, fd.Name.Name), false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				asks = asks || slices.Contains(shimReads, sel.Sel.Name)
				held = held || slices.Contains(shimHeld, sel.Sel.Name)
			}
			return true
		})
		if asks && held {
			out = append(out, at+": the shim decides between a held copy and asking")
		}
	})
	sort.Strings(out)
	return out
}

// eachFunc visits every function of the package in dir's non-test files.
func eachFunc(t *testing.T, dir string, visit func(at string, fd *ast.FuncDecl)) {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(abs, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				visit(fmt.Sprintf("client/%s/%s:%d %s", filepath.Base(abs), filepath.Base(path), fset.Position(fd.Pos()).Line, fd.Name.Name), fd)
			}
		}
	}
}

// TestStreamAxis runs each read with the stream open, where a held grid is
// current and nothing is asked, and with it down, where it is asked again.
func TestStreamAxis(t *testing.T) {
	rows := streamAxis()
	t.Run("count", func(t *testing.T) {
		unrun := unrunReads(t, rows)
		for _, at := range unrun {
			t.Log(at)
		}
		t.Logf("stream axis count: %d", len(unrun))
	})
	for _, r := range rows {
		for _, open := range []bool{true, false} {
			state := map[bool]string{true: "open", false: "gap"}[open]
			t.Run(r.name+": "+state, func(t *testing.T) {
				c := cache.New()
				r.hold(c)
				c.NoteStream(open)
				if got := r.asked(r.plan(t, c.Current())); got == open {
					t.Fatalf("%s with the stream %s: asked = %v, want %v", r.fn, state, got, !open)
				}
			})
		}
	}
}
