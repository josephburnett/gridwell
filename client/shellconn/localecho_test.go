package shellconn

import (
	"strings"
	"testing"
)

// screen is one terminal row the node writes, its cursor on it unless
// moved off.
type screen struct {
	cells        []string
	col          int
	offRow, gone bool
}

func newScreen(prompt string, cols int) *screen {
	s := &screen{cells: make([]string, cols)}
	s.write(prompt)
	return s
}

func (s *screen) Cols() int                 { return len(s.cells) }
func (s *screen) Cursor() (int, bool)       { return s.col, !s.offRow }
func (s *screen) Cell(c int) (string, bool) { return s.cells[c], !s.gone }

// write is the node's output: printable runes land at the cursor, \b moves
// it left.
func (s *screen) write(out string) {
	for _, r := range out {
		if r == '\b' {
			s.col--
			continue
		}
		s.cells[s.col] = string(r)
		s.col++
	}
}

type rig struct {
	t   *testing.T
	e   *LocalEcho
	s   *screen
	now float64
}

func newRig(t *testing.T) *rig {
	return &rig{t: t, e: NewLocalEcho(), s: newScreen("$ ", 20)}
}

func (r *rig) key(data string) { r.e.Key(data, r.now, r.s) }

// node is the node's output arriving after ms.
func (r *rig) node(ms float64, out string) {
	r.now += ms
	r.s.write(out)
	r.e.Output(r.now, r.s)
}

func (r *rig) shown() string {
	var b strings.Builder
	for _, c := range r.e.Overlay(r.now).Cells {
		if c.Text == "" {
			b.WriteString("_")
		}
		b.WriteString(c.Text)
	}
	return b.String()
}

// slow confirms an epoch over a link of ms: one key and its echo.
func (r *rig) slow(ms float64, ch string) {
	r.key(ch)
	r.node(ms, ch)
	if !r.e.Stats().Showing {
		r.t.Fatalf("a %v ms echo: not showing, delay %v", ms, r.e.Stats().DelayMs)
	}
}

func TestLocalEchoShowsOnlyOnceTheEpochIsConfirmed(t *testing.T) {
	r := newRig(t)
	r.key("l")
	if got := r.shown(); got != "" {
		t.Fatalf("an unmeasured link and a tentative epoch showed %q", got)
	}
	r.node(100, "l")
	r.key("s")
	if got := r.shown(); got != "s" {
		t.Fatalf("confirmed epoch on a slow link: shown %q, want s", got)
	}
	if ov := r.e.Overlay(r.now); ov.Cursor != 4 || ov.Cells[0].Col != 3 {
		t.Fatalf("overlay %+v, want s at column 3 and the cursor at 4", ov)
	}
	r.node(100, "s")
	if got := r.shown(); got != "" || r.e.Pending() != 0 {
		t.Fatalf("after the echo: shown %q, pending %d", got, r.e.Pending())
	}
	if s := r.e.Stats(); s.Made != 2 || s.Confirmed != 2 || s.Wrong != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestLocalEchoFastLinkShowsNothing(t *testing.T) {
	r := newRig(t)
	for _, ch := range "echo fast" {
		r.key(string(ch))
		if got := r.shown(); got != "" {
			t.Fatalf("a 2 ms link showed %q", got)
		}
		r.node(2, string(ch))
	}
	if s := r.e.Stats(); s.Showing || s.Confirmed != 9 {
		t.Fatalf("stats %+v: the fast link measured but did not show", s)
	}
}

// The epoch rule: a key no guess explains, Enter here, makes what follows
// tentative, and with echo off nothing confirms it.
func TestLocalEchoPasswordIsNeverShown(t *testing.T) {
	r := newRig(t)
	r.slow(100, "s")
	r.key("\r")
	r.s.offRow = true
	r.node(100, "")
	r.s = newScreen("Password: ", 30)
	r.node(0, "")
	for _, ch := range "hunter2" {
		r.key(string(ch))
		r.now += 50
		if got := r.shown(); got != "" {
			t.Fatalf("a password key showed %q", got)
		}
		r.e.Output(r.now, r.s) // the node says nothing back
	}
	r.now += LocalEchoExpireMs + 10
	r.e.Tick(r.now)
	if r.e.Pending() != 0 || r.e.Stats().Wrong != 7 {
		t.Fatalf("unconfirmed guesses did not expire: %+v", r.e.Stats())
	}
	// A character and its erase landing together change nothing, so they
	// confirm no epoch.
	r.key("x")
	r.key("\x7f")
	r.e.Output(r.now+100, r.s)
	r.key("y")
	if got := r.shown(); got != "" {
		t.Fatalf("after an erase that changed nothing: shown %q", got)
	}
}

func TestLocalEchoOutputNobodyTypedStartsAnEpoch(t *testing.T) {
	r := newRig(t)
	r.slow(100, "a")
	r.node(500, "") // a prompt redrawn, a program's line
	r.key("b")
	if got := r.shown(); got != "" {
		t.Fatalf("after output nobody typed: shown %q", got)
	}
}

func TestLocalEchoWrongGuessDiscardsAll(t *testing.T) {
	r := newRig(t)
	r.slow(100, "a")
	r.key("b")
	r.key("c")
	r.node(100, "X")
	if r.e.Pending() != 0 || r.e.Stats().Wrong != 2 {
		t.Fatalf("a wrong cell: pending %d, %+v", r.e.Pending(), r.e.Stats())
	}
	r.key("d")
	if got := r.shown(); got != "" {
		t.Fatalf("after a wrong guess the epoch is tentative, shown %q", got)
	}
}

func TestLocalEchoUnexplainedCursorIsWrong(t *testing.T) {
	r := newRig(t)
	r.slow(100, "a")
	r.key("b")
	r.now += 100
	r.s.col = 10
	r.e.Output(r.now, r.s)
	if r.e.Pending() != 0 || r.e.Stats().Wrong != 1 {
		t.Fatalf("a cursor the guesses do not explain: pending %d, %+v", r.e.Pending(), r.e.Stats())
	}
}

func TestLocalEchoPartialEchoKeepsTheRest(t *testing.T) {
	r := newRig(t)
	r.slow(100, "a")
	r.key("b")
	r.key("c")
	r.node(100, "b")
	if got := r.shown(); got != "c" {
		t.Fatalf("one of two echoed: shown %q, want c", got)
	}
}

func TestLocalEchoBackspace(t *testing.T) {
	r := newRig(t)
	r.slow(100, "a")
	r.key("b")
	r.key("\x7f")
	if got := r.shown(); got != "_" {
		t.Fatalf("b then backspace: shown %q, want one blank cell", got)
	}
	if ov := r.e.Overlay(r.now); ov.Cursor != 3 {
		t.Fatalf("cursor %d, want 3", ov.Cursor)
	}
	r.node(100, "b")
	r.node(0, "\b \b")
	if r.e.Pending() != 0 || r.e.Stats().Wrong != 0 {
		t.Fatalf("the erase's echo: pending %d, %+v", r.e.Pending(), r.e.Stats())
	}
	// The run began at column 2: an erase there would eat the prompt.
	r.key("\x7f")
	r.key("\x7f")
	if r.e.Pending() != 1 {
		t.Fatalf("an erase left of the run was guessed: pending %d", r.e.Pending())
	}
}

func TestLocalEchoStopsShortOfTheLastColumn(t *testing.T) {
	r := newRig(t)
	r.s = newScreen(strings.Repeat("x", 17), 20)
	r.slow(100, "a") // column 17
	r.key("b")       // column 18, the cursor to 19
	r.key("c")       // column 19 would wrap
	if r.e.Pending() != 1 || r.shown() != "b" {
		t.Fatalf("pending %d shown %q: the last column is not guessed", r.e.Pending(), r.shown())
	}
}

func TestLocalEchoWideAndCombiningAreNotGuessed(t *testing.T) {
	for _, ch := range []string{"中", "😀", "́", "ab", "\x1b[D", "\t"} {
		r := newRig(t)
		r.slow(100, "a")
		r.key(ch)
		r.key("b")
		if r.e.Pending() != 0 {
			t.Errorf("%q was guessed, or did not make the next key wait", ch)
		}
	}
	r := newRig(t)
	r.slow(100, "a")
	r.key("é")
	if got := r.shown(); got != "é" {
		t.Errorf("a one-cell Latin letter: shown %q", got)
	}
}

func TestLocalEchoInsertAndGoneRow(t *testing.T) {
	r := newRig(t)
	r.slow(100, "a")
	r.s.cells[10] = "z" // text right of the cursor: a key inserts
	r.key("b")
	if r.e.Pending() != 0 {
		t.Fatal("a key before text on the row was guessed")
	}
	r = newRig(t)
	r.slow(100, "a")
	r.key("b")
	r.s.gone = true
	r.node(100, "")
	if r.e.Pending() != 0 || r.e.Stats().Wrong != 1 {
		t.Fatal("a guess on a row scrolled away stood")
	}
}

func TestLocalEchoOffTheRowConfirmsWhatLanded(t *testing.T) {
	r := newRig(t)
	r.slow(100, "l")
	r.key("s")
	r.key("\r")
	r.s.write("s")
	r.s.offRow = true
	r.node(100, "")
	if r.e.Pending() != 0 || r.e.Stats().Wrong != 0 {
		t.Fatalf("ls then Enter echoed: pending %d %+v", r.e.Pending(), r.e.Stats())
	}
}

func TestLocalEchoHysteresis(t *testing.T) {
	r := newRig(t)
	r.s = newScreen("$ ", 1000)
	r.slow(2*LocalEchoOnMs, "a")
	r.e.Owed()
	for i, s := range []struct {
		ms      float64
		showing bool
		flips   int
	}{{LocalEchoOffMs + 5, true, 0}, {LocalEchoOffMs / 4, false, 1}, {LocalEchoOnMs - 5, false, 0}, {2 * LocalEchoOnMs, true, 1}} {
		flips := 0
		for range 40 {
			r.key("b")
			r.node(s.ms, "b")
			if f, _ := r.e.Owed(); f {
				flips++
			}
		}
		if got := r.e.Stats().Showing; got != s.showing || flips != s.flips {
			t.Fatalf("phase %d (%v ms): showing %v after %d flips, delay %v", i, s.ms, got, flips, r.e.Stats().DelayMs)
		}
	}
}

func TestLocalEchoUnderlineAndNext(t *testing.T) {
	r := newRig(t)
	r.slow(100, "a")
	r.key("b")
	due, ok := r.e.Next(r.now)
	if !ok || due != r.now+LocalEchoGlitchMs+1 {
		t.Fatalf("next %v %v, want the underline", due, ok)
	}
	r.now = due
	if ov := r.e.Overlay(r.now); !ov.Cells[0].Underline {
		t.Fatal("a guess past the glitch is not underlined")
	}
	if due, _ := r.e.Next(r.now); due <= r.now {
		t.Fatalf("next %v is not after now %v: an underlined guess is due again", due, r.now)
	}
}

func TestLocalEchoReset(t *testing.T) {
	r := newRig(t)
	r.slow(100, "a")
	r.key("b")
	r.e.Reset()
	if r.e.Pending() != 0 || r.shown() != "" {
		t.Fatal("a reset left a guess")
	}
}

func TestLocalEchoSummaryIsOwedEveryHundreds(t *testing.T) {
	r := newRig(t)
	n := 0
	for i := 0; i < 2*LocalEchoSummaryEvery; i++ {
		r.key("a")
		r.e.Reset()
		if _, s := r.e.Owed(); s {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d summaries over %d guesses", n, 2*LocalEchoSummaryEvery)
	}
}
