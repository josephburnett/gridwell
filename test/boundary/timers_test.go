package boundary

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// goTimers are the time package's calls that wait on a clock.
var goTimers = map[string]bool{"NewTicker": true, "Tick": true, "After": true, "NewTimer": true, "AfterFunc": true, "Sleep": true}

var tsTimer = regexp.MustCompile(`\b(?:setTimeout|setInterval)[\t ]*\(`)

// timerClasses are why a clock may run: a debounce coalesces bursts, a
// backoff paces a retry, a bound caps a wait that something else ends, and a
// poll asks a source that cannot tell.
var timerClasses = map[string]bool{"debounce": true, "backoff": true, "bound": true, "poll": true}

type timerSite struct {
	rel  string
	line int
	fn   string // the enclosing Go function, "" in TS
	near string // the site's line and the one above it
}

// anchoredBy reports whether an allowed-timers anchor names this site: its
// line number, its enclosing Go function, or a word on its line or the line
// above, so an entry survives edits elsewhere in the file.
func (s timerSite) anchoredBy(anchor string) bool {
	if n, err := strconv.Atoi(anchor); err == nil {
		return n == s.line
	}
	if anchor == s.fn {
		return true
	}
	for _, w := range identifier.FindAllString(s.near, -1) {
		if w == anchor {
			return true
		}
	}
	return false
}

func timerSites(t *testing.T, root string) []timerSite {
	var out []timerSite
	walkRepo(t, root, func(rel, path string, data []byte) {
		if isTestFile(rel) || !isProduction(rel) {
			return
		}
		lines := strings.Split(string(data), "\n")
		site := func(line int, fn string) timerSite {
			near := lines[line-1]
			if line > 1 {
				near = lines[line-2] + "\n" + near
			}
			return timerSite{rel, line, fn, near}
		}
		if strings.HasSuffix(rel, ".ts") {
			for i, l := range strings.Split(string(stripTSComments(data)), "\n") {
				if tsTimer.MatchString(l) {
					out = append(out, site(i+1, ""))
				}
			}
			return
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if ok && isTimerCall(call) {
					out = append(out, site(fset.Position(call.Pos()).Line, fn.Name.Name))
				}
				return true
			})
		}
	})
	return out
}

// isTimerCall is a time package wait, or the shim handing the browser a
// setTimeout or setInterval by name.
func isTimerCall(call *ast.CallExpr) bool {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "time" && goTimers[sel.Sel.Name] {
			return true
		}
	}
	if len(call.Args) > 0 {
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			v, _ := strconv.Unquote(lit.Value)
			return v == "setTimeout" || v == "setInterval"
		}
	}
	return false
}

type allowedTimer struct {
	at, rel, anchor, class string
}

func allowedTimers(t *testing.T, root string) []allowedTimer {
	t.Helper()
	var out []allowedTimer
	for _, l := range allowLines(t, filepath.Join(root, "scripts", "allowed-timers.txt")) {
		fields := strings.Split(l, "|")
		if len(fields) != 3 {
			t.Errorf("scripts/allowed-timers.txt: %q: want path:anchor | class | reason", l)
			continue
		}
		where := strings.TrimSpace(fields[0])
		i := strings.LastIndex(where, ":")
		class := strings.TrimSpace(fields[1])
		switch {
		case i < 0:
			t.Errorf("scripts/allowed-timers.txt: %q names no anchor", where)
			continue
		case !timerClasses[class]:
			t.Errorf("scripts/allowed-timers.txt: %s: class %q is not debounce, backoff, bound or poll", where, class)
		case strings.TrimSpace(fields[2]) == "":
			t.Errorf("scripts/allowed-timers.txt: %s gives no reason", where)
		}
		out = append(out, allowedTimer{where, filepath.FromSlash(where[:i]), where[i+1:], class})
	}
	return out
}

// unlistedTimers are the timer sites scripts/allowed-timers.txt does not name.
func unlistedTimers(t *testing.T, root string) []string {
	allowed := allowedTimers(t, root)
	var out []string
	for _, s := range timerSites(t, root) {
		listed := false
		for _, a := range allowed {
			if a.rel == s.rel && s.anchoredBy(a.anchor) {
				listed = true
				break
			}
		}
		if !listed {
			out = append(out, fmt.Sprintf("%s:%d", filepath.ToSlash(s.rel), s.line))
		}
	}
	return out
}

// A clock runs only for a reason someone wrote down: the node learns of a
// change by being told, so a timer that is none of debounce, backoff, bound
// or poll-of-a-source-that-cannot-tell is the clock the promise forbids.
func TestEveryTimerIsListed(t *testing.T) {
	for _, at := range unlistedTimers(t, repoRoot(t)) {
		t.Errorf("%s starts a timer scripts/allowed-timers.txt does not list: list it as path:anchor | class | reason, or remove it", at)
	}
}

// An allowed timer names a site that still exists, so a deleted timer's entry
// cannot vouch for the next one written beside it.
func TestAllowedTimersAreLive(t *testing.T) {
	root := repoRoot(t)
	sites := timerSites(t, root)
	for _, a := range allowedTimers(t, root) {
		live := false
		for _, s := range sites {
			if a.rel == s.rel && s.anchoredBy(a.anchor) {
				live = true
				break
			}
		}
		if !live {
			t.Errorf("scripts/allowed-timers.txt: %s anchors no timer; delete the entry", a.at)
		}
	}
}
