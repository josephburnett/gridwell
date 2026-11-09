package touchgest_test

// The device axis: every surface a press can land on, times every gesture,
// made with a mouse and with a finger. The finger's gesture is the machine's
// output, the mouse's the events a mouse sends, and each is fed to the pure
// verdict that routes the surface's presses, which must give touch exactly
// the one action it gives the mouse, or the other one a decision on differs
// names. A cell no pure verdict can run, or one
// whose events carry Select on a surface whose Select arm is
// client/wasm/touch.go's, is not same. The count, the cells not same, the mouse gestures with no touch form
// and no decision, and the surfaces and bar actions no row names, is
// recorded by test/boundary off TestDeviceAxis/count.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
	tg "github.com/josephburnett/gridwell/client/touchgest"
	"github.com/josephburnett/gridwell/client/wsbar"
)

var at = tg.Point{X: 100, Y: 100}

// deviceGesture is one gesture: the events a mouse sends for it, and the
// finger's form of it played into a fresh machine.
type deviceGesture struct {
	name  string
	mouse []tg.Action
	touch func(m *tg.Machine) []tg.Action
}

func down(b int) tg.Action { return tg.Action{Kind: tg.MouseDown, Pos: at, Button: b} }
func move(b int) tg.Action {
	return tg.Action{Kind: tg.MouseMove, Pos: tg.Point{X: 130, Y: 100}, Button: b}
}
func up(b int) tg.Action { return tg.Action{Kind: tg.MouseUp, Pos: at, Button: b} }
func wheel(dy float64) tg.Action {
	return tg.Action{Kind: tg.Wheel, Pos: at, DeltaY: dy}
}

func plays(steps ...func(m *tg.Machine) []tg.Action) func(m *tg.Machine) []tg.Action {
	return func(m *tg.Machine) []tg.Action {
		var out []tg.Action
		for _, s := range steps {
			out = append(out, s(m)...)
		}
		return out
	}
}

func press(pts ...tg.Point) func(m *tg.Machine) []tg.Action {
	return func(m *tg.Machine) []tg.Action { return m.Start(pts, 0) }
}
func hold(m *tg.Machine) []tg.Action { return m.Timer(tg.HoldMs + 5) }
func drag(pts ...tg.Point) func(m *tg.Machine) []tg.Action {
	return func(m *tg.Machine) []tg.Action { return m.Move(pts, tg.HoldMs+50) }
}
func lift(m *tg.Machine) []tg.Action      { return m.End(nil, tg.HoldMs+100) }
func quickLift(m *tg.Machine) []tg.Action { return m.End(nil, tg.TwoTapMs/2) }

var (
	left  = tg.Point{X: 90, Y: 100}
	right = tg.Point{X: 110, Y: 100}
	away  = tg.Point{X: 130, Y: 100}
)

var deviceGestures = []deviceGesture{
	{"click", []tg.Action{down(0), up(0)}, plays(press(at), quickLift)},
	{"drag", []tg.Action{down(0), move(0), up(0)}, plays(press(at), drag(away), lift)},
	{"right-click", []tg.Action{down(2), up(2)}, plays(press(at), hold, lift)},
	{"right-drag", []tg.Action{down(2), move(2), up(2)}, plays(press(at), hold, drag(away), lift)},
	{"middle-click", []tg.Action{down(1), up(1)}, plays(press(left, right), quickLift)},
	{"wheel in", []tg.Action{wheel(-1)}, plays(press(left, right),
		drag(tg.Point{X: 80, Y: 100}, tg.Point{X: 120, Y: 100}), quickLift)},
	{"wheel out", []tg.Action{wheel(1)}, plays(press(left, right),
		drag(tg.Point{X: 90, Y: 80}, tg.Point{X: 110, Y: 80}), quickLift)},
}

// mouseOnly are the mouse gestures a modifier key makes, with the touch form
// CLAUDE.md decides for each, "" where it decides none.
var mouseOnly = []struct{ gesture, decision string }{
	{"shift + click on a tile toggles it", `"on touch a long-press lifted in place toggles" (2026-10-15)`},
	{"shift + left-drag on the background sweeps a band", ""},
	{"ctrl or cmd + click descends in a split", ""},
	{"ctrl + right-drag links", ""},
}

// record is what a verdict reads of a gesture: the button of its press,
// whether shift was held and whether it moved, or the direction of its wheel,
// and whether a finger made it.
type record struct {
	button  int // -1 for a wheel
	shift   bool
	moved   bool
	zoomOut bool
	selects bool
	touch   bool
}

func recordOf(as []tg.Action) (record, error) {
	r := record{button: -1}
	if len(as) == 0 {
		return r, fmt.Errorf("no events")
	}
	pressed, released := false, false
	for _, a := range as {
		switch a.Kind {
		case tg.MouseDown:
			if pressed {
				return r, fmt.Errorf("two presses")
			}
			pressed, r.button = true, a.Button
		case tg.MouseUp:
			released = true
		case tg.MouseMove:
			r.moved = true
		case tg.Wheel:
			r.zoomOut = a.DeltaY > 0
		case tg.Select:
			r.selects = true
		}
	}
	if pressed != released {
		return r, fmt.Errorf("a press with no release")
	}
	return r, nil
}

// verdict is a surface's pure routing of one record, ok false where only the
// shim routes it.
type verdict func(r record) (action string, ok bool)

// surface is one place a press lands: listener is the client/wasm file whose
// listener takes it, selects the CLAUDE.md line deciding what Select does
// there, if any, and selectRouted whether the shim hands touch's Select to
// route as the mouse's shift + left press.
type surface struct {
	name         string
	listener     string
	route        verdict
	selects      string
	selectRouted bool
}

// differs is the CLAUDE.md line, per surface, deciding that its pure verdict
// gives a finger another action than the mouse's.
var differs = map[string]string{
	"door tile": `"a finger does not move or delete a file" (2026-11-08)`,
}

func shimOnly(record) (string, bool) { return "", false }

// barRegion routes through wsbar.RouteClick at x on a 400-wide bar holding a
// workspace tile crumb, two chain crumbs, the last one an ephemeral visit, and
// a title at 200..280; a wheel in the band is gesture.ClassifyWheel's. Every
// bar surface's Select is routed (bottomBarSelect).
func barRegion(zone wsbar.Zone, x float64) verdict {
	return func(r record) (string, bool) {
		if r.button < 0 {
			return fmt.Sprint("wheel ", gesture.ClassifyWheel(gesture.WheelInput{Zone: zone})), true
		}
		h := wsbar.RouteClick(wsbar.Click{
			Button: r.button, Shift: r.shift, X: x, Zone: zone, BarW: 400,
			Segments: []wsbar.Segment{
				{Index: 0, X: 0, W: wsbar.BoundaryW},
				{Index: 1, X: wsbar.BoundaryW, W: wsbar.RowH},
				{Index: 2, X: wsbar.BoundaryW + wsbar.RowH, W: wsbar.RowH},
			},
			Chain: []pane.NavCrumb{
				{Workspace: true, WsLevel: 1, TileID: "ws", GridID: "g/0"},
				{Crumb: pane.Crumb{TileID: "w1", ParentAnchor: "g/1"}},
				{Crumb: pane.Crumb{TileID: "w2", ParentAnchor: "g/1", ParentPath: []string{"w1"}}},
			},
			TitleX: 200, TitleW: 80, TitleOK: true, SlotMenu: true, Promote: true,
		})
		barActions[h.Action] = true
		return fmt.Sprint("bar ", h.Action), true
	}
}

var barActions = map[wsbar.Action]bool{}

// inGrid routes the right press through gesture.Classify, a left press that
// does not move through over (a click verdict) and a wheel through
// gesture.ClassifyWheel; a left drag and the middle button are the shim's.
func inGrid(in gesture.Input, click func() (string, bool), wheel gesture.WheelInput) verdict {
	return func(r record) (string, bool) {
		switch {
		case r.button < 0:
			wheel.ZoomOut = r.zoomOut
			return fmt.Sprint("wheel ", gesture.ClassifyWheel(wheel)), true
		case r.button == 2:
			return fmt.Sprint("right ", gesture.Classify(in)), true
		case r.button == 0 && !r.moved && click != nil:
			return click()
		}
		return "", false
	}
}

// tileDrag routes a left drag of a tile through dragdrop.DecideDrop: a
// Gridwell door and a directory's door, each onto another directory's grid
// and onto the trashcan.
func tileDrag(next verdict) verdict {
	return func(r record) (string, bool) {
		if r.button != 0 || !r.moved {
			return next(r)
		}
		var out []string
		for _, file := range []bool{false, true} {
			for _, trash := range []bool{false, true} {
				out = append(out, fmt.Sprint(dragdrop.DecideDrop(dragdrop.DropInput{Started: true, TileID: "7",
					HasTarget: !trash, OverDelete: trash, Touch: r.touch, CarriesFile: file})))
			}
		}
		return "drag " + strings.Join(out, "/"), true
	}
}

func deviceSurfaces() []surface {
	door := gesture.WheelInput{OverEnterableDoor: true, DoorCoverage: 0.1}
	return []surface{
		{"notice row", "input.go", shimOnly, "", false},
		{"bar: + circle", "input.go", barRegion(wsbar.ZoneBar, 380), "", true},
		{"bar: title", "input.go", barRegion(wsbar.ZoneBar, 210), "", true},
		{"bar: name beside the title", "input.go", barRegion(wsbar.ZoneBar, 190), "", true},
		{"bar: workspace tile crumb", "input.go", barRegion(wsbar.ZoneBar, 10), "", true},
		{"bar: crumb", "input.go", barRegion(wsbar.ZoneBar, wsbar.BoundaryW+2), "", true},
		{"bar: ephemeral visit crumb", "input.go", barRegion(wsbar.ZoneBar, wsbar.BoundaryW+wsbar.RowH+2), "", true},
		{"band beside the bar", "input.go", barRegion(wsbar.ZoneBand, 0), "", true},
		{"+ menu", "input.go", shimOnly, "", false},
		{"pane edge", "input.go", inGrid(gesture.Input{InGridView: true, Region: pane.RegionSplitTop},
			func() (string, bool) {
				arm, _ := gesture.ResizeAffordance(pane.DividerGrab{HasVert: true})
				return fmt.Sprint("resize ", arm), true
			}, gesture.WheelInput{}), "", false},
		{"door tile", "input.go", tileDrag(inGrid(gesture.Input{InGridView: true, OverTile: true, InTileCenter: true},
			func() (string, bool) {
				return fmt.Sprint("click ", gesture.DecideTileClick(gesture.ClickInput{Door: true})), true
			}, door)), `"on touch a long-press lifted in place toggles" (2026-10-15)`, false},
		{"grid background", "input.go", inGrid(gesture.Input{InGridView: true}, nil, gesture.WheelInput{}), "", false},
		{"live content", "input.go", func(r record) (string, bool) {
			switch {
			case r.button < 0:
				return fmt.Sprint("wheel ", gesture.ClassifyWheel(gesture.WheelInput{TextFocused: true, InContentBox: true, ZoomOut: r.zoomOut})), true
			case r.button == 0 && !r.moved:
				return fmt.Sprint("land ", gesture.Land(gesture.SurfaceRawText, true)), true
			}
			return "", false
		}, "", false},
		{"circle menu", "circle_menu.go", shimOnly, "", false},
		{"editing textarea", "overlay_mouse.go", shimOnly, "", false},
		{"text mode toggle", "text_overlay.go", shimOnly, "", false},
		{"rendered view", "rendered_overlay.go", shimOnly, "", false},
		{"shell terminal", "shell_stream_client.go", shimOnly, "", false},
		{"url prompt", "url_modal.go", shimOnly, "", false},
	}
}

// cell is the verdict on one gesture over one surface: "" when touch yields
// the mouse's one action through the same pure verdict.
func cell(s surface, g deviceGesture) string {
	mr, err := recordOf(g.mouse)
	if err != nil {
		return "the mouse's events: " + err.Error()
	}
	tr, err := recordOf(g.touch(tg.New()))
	if err != nil {
		return "the finger's events: " + err.Error()
	}
	tr.touch = true
	if tr.selects && s.selectRouted {
		// The Select is the mouse's shift + left press, through one verdict;
		// what is left of the gesture is compared as any other.
		if _, ok := s.route(record{button: 0, shift: true}); !ok {
			return "touch's Select reaches no verdict"
		}
		tr.selects = false
	}
	if tr.selects {
		if s.selects != "" {
			return "touch toggles the tile, decided by " + s.selects + ", in an arm only client/wasm/touch.go runs"
		}
		return "touch adds Select, whose arm only client/wasm/touch.go runs"
	}
	ma, mok := s.route(mr)
	ta, tok := s.route(tr)
	switch {
	case !mok || !tok:
		return "only the shim routes it"
	case ma != ta && differs[s.name] == "":
		return fmt.Sprintf("the mouse does %s, touch %s", ma, ta)
	}
	return ""
}

// touchDiffers reports whether some gesture's pure verdict over s gives a
// finger another action than the mouse's.
func touchDiffers(s surface) bool {
	for _, g := range deviceGestures {
		mr, _ := recordOf(g.mouse)
		tr, _ := recordOf(g.touch(tg.New()))
		tr.touch = true
		ma, mok := s.route(mr)
		ta, tok := s.route(tr)
		if mok && tok && ma != ta {
			return true
		}
	}
	return false
}

// listenerFiles are the client/wasm files that listen for a press or a click,
// by addEventListener or the shim's listen.
func listenerFiles(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "wasm")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "touch.go" {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return !found
			}
			event := call.Args[1]
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Value == `"addEventListener"` && len(call.Args) > 2 {
				event = call.Args[1]
			} else if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "listen" {
				return !found
			}
			if lit, ok := event.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, _ := strconv.Unquote(lit.Value); s == "mousedown" || s == "click" {
					found = true
				}
			}
			return !found
		})
		if found {
			out = append(out, filepath.Base(path))
		}
	}
	return out
}

// TestDeviceAxis runs every cell; a cell not same is counted, not failed,
// since the count is the ledger's.
func TestDeviceAxis(t *testing.T) {
	surfaces := deviceSurfaces()
	var holes []string
	for _, s := range surfaces {
		for _, g := range deviceGestures {
			if why := cell(s, g); why != "" {
				holes = append(holes, fmt.Sprintf("%s, %s: %s", s.name, g.name, why))
			}
		}
	}
	for _, s := range surfaces {
		if differs[s.name] != "" && !touchDiffers(s) {
			holes = append(holes, s.name+": decided to differ on touch, and no gesture does")
		}
	}
	for _, m := range mouseOnly {
		if m.decision == "" {
			holes = append(holes, m.gesture+": no touch form and no decision")
		}
	}
	for _, file := range listenerFiles(t) {
		if !slices.ContainsFunc(surfaces, func(s surface) bool { return s.listener == file }) {
			holes = append(holes, "client/wasm/"+file+": a press listener no surface names")
		}
	}
	for a := wsbar.ActionNone; a <= wsbar.ActionSelect; a++ {
		// Pass hands the press to the surfaces under the bar, which are rows.
		if a != wsbar.ActionPass && !barActions[a] {
			holes = append(holes, fmt.Sprintf("wsbar action %d: no cell reaches it", a))
		}
	}
	sort.Strings(holes)
	t.Run("count", func(t *testing.T) {
		for _, h := range holes {
			t.Log(h)
		}
		t.Logf("device axis count: %d", len(holes))
	})
}
