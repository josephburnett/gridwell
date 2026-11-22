package shellconn

// Local echo shows a typed key's guess at once on a slow link, after mosh's
// speculative local echo, and the node's output always wins: a guess is
// drawn over the terminal, never written into it. A guess is shown only
// once the node has confirmed one of its epoch, so a program that does not
// echo, a password prompt among them, never shows what was typed.
const (
	// LocalEchoOnMs and LocalEchoOffMs are mosh's adaptive thresholds on the
	// smoothed key-to-echo delay: lag is felt from about 30 ms, and the gap
	// keeps a link hovering near it from flickering. The delay is the whole
	// path, tmux and the page's own parse included, so a loaded host can
	// cross it with no network at all, and then the echo is slow.
	LocalEchoOnMs  = 30
	LocalEchoOffMs = 20
	// LocalEchoGlitchMs is mosh's: a guess shown this long unconfirmed is
	// underlined.
	LocalEchoGlitchMs = 250
	// LocalEchoExpireMs is how long a guess waits for the node before it is
	// wrong.
	LocalEchoExpireMs = 3000
	// LocalEchoSummaryEvery is how many guesses a trace summary covers.
	LocalEchoSummaryEvery = 200
)

// LocalEchoTerm is what local echo reads of the terminal. The guesses'
// row is the one the cursor was on when the first pending guess was made.
// No terminal mode gates a guess: tmux holds every shell on the alternate
// screen with mouse tracking on, and neither moves where a typed character
// lands; a full-screen program is the epoch rule's.
type LocalEchoTerm interface {
	Cols() int
	// Cursor is the cursor's column and whether it is on the guesses' row.
	Cursor() (col int, onRow bool)
	// Cell is the guesses' row's cell at col, "" for blank, and false when
	// the row is gone.
	Cell(col int) (string, bool)
}

type guess struct {
	col                       int
	text                      string // "" erases
	cursorBefore, cursorAfter int
	at                        float64
	epoch                     int
}

// LocalEcho is one live attachment's guesses. Not safe for concurrent use.
type LocalEcho struct {
	guesses []guess
	// before is each column a pending guess touches as it was before the
	// oldest of them.
	before                map[int]string
	epoch, confirmedEpoch int
	// blind is a key no guess explains: no guess until output resolves it.
	blind bool
	// runStart is the column this epoch's guesses began at, -1 for none; an
	// erase never reaches left of it.
	runStart int
	delay    float64
	showing  bool
	stats    LocalEchoStats
	flipped  bool
	reported int
}

// LocalEchoStats is what a trace says of an attachment's local echo.
type LocalEchoStats struct {
	Made, Confirmed, Wrong int
	DelayMs                float64
	Showing                bool
}

// LocalEchoCell is one drawn cell; Text "" draws it blank.
type LocalEchoCell struct {
	Col       int
	Text      string
	Underline bool
}

// LocalEchoOverlay is what to draw over the guesses' row: nothing when
// Cells is empty, else the cells and the guessed cursor's column.
type LocalEchoOverlay struct {
	Cells  []LocalEchoCell
	Cursor int
}

func NewLocalEcho() *LocalEcho {
	return &LocalEcho{before: map[int]string{}, epoch: 1, runStart: -1}
}

// Pending is how many guesses wait for the node.
func (e *LocalEcho) Pending() int { return len(e.guesses) }

// Key is one key the terminal sent: a printable one-cell character typed at
// the end of the row, short of the last column, is guessed, and a backspace
// over this epoch's guesses; anything else starts a new epoch.
func (e *LocalEcho) Key(data string, now float64, t LocalEchoTerm) {
	e.Tick(now)
	col, onRow := t.Cursor()
	pos := col
	if n := len(e.guesses); n > 0 {
		pos = e.guesses[n-1].cursorAfter
	}
	g := guess{cursorBefore: pos, at: now, epoch: e.epoch}
	switch {
	case e.blind || !onRow:
		e.boundary()
		return
	case data == "\x7f" || data == "\b":
		if e.runStart < 0 || pos <= e.runStart {
			e.boundary()
			return
		}
		g.col, g.cursorAfter = pos-1, pos-1
	default:
		r := []rune(data)
		if len(r) != 1 || !oneCell(r[0]) || pos+1 >= t.Cols() || !e.blankFrom(pos, t) {
			e.boundary()
			return
		}
		if e.runStart < 0 {
			e.runStart = pos
		}
		g.col, g.text, g.cursorAfter = pos, data, pos+1
	}
	if _, ok := e.before[g.col]; !ok {
		e.before[g.col], _ = t.Cell(g.col)
	}
	e.guesses = append(e.guesses, g)
	e.stats.Made++
}

// oneCell is a character whose width is surely one cell; a wide or
// combining one is not guessed.
func oneCell(r rune) bool {
	return r >= 0x20 && r <= 0x7e || r >= 0xa0 && r <= 0x24f && r != 0xad
}

// blankFrom is whether the row is blank from col on as the guesses leave
// it, so a typed character appends rather than inserts.
func (e *LocalEcho) blankFrom(col int, t LocalEchoTerm) bool {
	for c := col; c < t.Cols(); c++ {
		v, ok := e.predicted(c)
		if !ok {
			v, ok = t.Cell(c)
		}
		if !ok || !blank(v) {
			return false
		}
	}
	return true
}

func (e *LocalEcho) predicted(col int) (string, bool) {
	for i := len(e.guesses) - 1; i >= 0; i-- {
		if e.guesses[i].col == col {
			return e.guesses[i].text, true
		}
	}
	return "", false
}

// Output is the terminal having parsed the node's output. Output with no
// guess pending is something nobody typed, so it starts a new epoch: a
// prompt that turns echo off is output.
func (e *LocalEcho) Output(now float64, t LocalEchoTerm) {
	if len(e.guesses) == 0 {
		e.epoch++
		e.runStart = -1
		e.blind = false
		return
	}
	e.Tick(now)
	k := len(e.guesses)
	for ; k >= 0 && !e.consistent(k, t); k-- {
	}
	if k < 0 {
		e.wrong()
	} else {
		e.confirm(k, now)
	}
	if len(e.guesses) == 0 {
		e.blind = false
	}
}

// consistent is whether the screen is the first k guesses landed and the
// rest not yet. Off the guesses' row, only all of them landed explains it.
func (e *LocalEcho) consistent(k int, t LocalEchoTerm) bool {
	col, onRow := t.Cursor()
	if !onRow && k != len(e.guesses) {
		return false
	}
	if onRow && col != e.cursorAt(k) {
		return false
	}
	for c := range e.before {
		v, ok := t.Cell(c)
		if !ok || !sameCell(v, e.expected(k, c)) {
			return false
		}
	}
	return true
}

func (e *LocalEcho) cursorAt(k int) int {
	if k == 0 {
		return e.guesses[0].cursorBefore
	}
	return e.guesses[k-1].cursorAfter
}

func (e *LocalEcho) expected(k, col int) string {
	v := e.before[col]
	for _, g := range e.guesses[:k] {
		if g.col == col {
			v = g.text
		}
	}
	return v
}

// confirm takes the first k guesses as landed. Only a landing that changed
// the screen is the node's word: an echo that is off changes nothing, so a
// character and its erase landing together confirm no epoch.
func (e *LocalEcho) confirm(k int, now float64) {
	if k == 0 {
		return
	}
	changed := e.cursorAt(k) != e.cursorAt(0)
	for c, v := range e.before {
		changed = changed || !sameCell(v, e.expected(k, c))
	}
	last := e.guesses[k-1]
	if changed {
		e.confirmedEpoch = max(e.confirmedEpoch, last.epoch)
		e.sample(now - last.at)
	}
	for _, g := range e.guesses[:k] {
		e.before[g.col] = g.text
	}
	e.guesses = e.guesses[k:]
	if len(e.guesses) == 0 {
		clear(e.before)
	}
	e.stats.Confirmed += k
}

func (e *LocalEcho) sample(ms float64) {
	if e.delay == 0 {
		e.delay = ms
	} else {
		e.delay += (ms - e.delay) / 8
	}
	if on := e.showing && e.delay >= LocalEchoOffMs || !e.showing && e.delay > LocalEchoOnMs; on != e.showing {
		e.showing, e.flipped = on, true
	}
}

func (e *LocalEcho) wrong() {
	e.stats.Wrong += len(e.guesses)
	e.Reset()
}

// Reset drops every guess and starts a new epoch: a resize, a move, a park
// or an end leaves no guess standing.
func (e *LocalEcho) Reset() {
	e.guesses = nil
	clear(e.before)
	e.epoch++
	e.runStart = -1
}

func (e *LocalEcho) boundary() {
	e.epoch++
	e.blind = true
	e.runStart = -1
}

// Tick makes the guesses wrong once the oldest has waited out
// LocalEchoExpireMs.
func (e *LocalEcho) Tick(now float64) {
	if len(e.guesses) > 0 && now-e.guesses[0].at > LocalEchoExpireMs {
		e.wrong()
	}
}

// Overlay is what to draw now: the shown guesses, those of a confirmed
// epoch while the link is slow, applied in order.
func (e *LocalEcho) Overlay(now float64) LocalEchoOverlay {
	ov := LocalEchoOverlay{}
	if !e.showing {
		return ov
	}
	at := map[int]int{}
	for _, g := range e.guesses {
		if g.epoch > e.confirmedEpoch {
			break
		}
		c := LocalEchoCell{Col: g.col, Text: g.text, Underline: now-g.at > LocalEchoGlitchMs}
		if i, ok := at[g.col]; ok {
			ov.Cells[i] = c
		} else {
			at[g.col] = len(ov.Cells)
			ov.Cells = append(ov.Cells, c)
		}
		ov.Cursor = g.cursorAfter
	}
	return ov
}

// Next is when the overlay or the guesses next change with no key and no
// output: an underline or an expiry.
func (e *LocalEcho) Next(now float64) (float64, bool) {
	if len(e.guesses) == 0 {
		return 0, false
	}
	due := e.guesses[0].at + LocalEchoExpireMs + 1
	for _, g := range e.guesses {
		if e.showing && g.epoch <= e.confirmedEpoch && now-g.at <= LocalEchoGlitchMs {
			due = min(due, g.at+LocalEchoGlitchMs+1)
		}
	}
	return due, true
}

func (e *LocalEcho) Stats() LocalEchoStats {
	s := e.stats
	s.DelayMs, s.Showing = e.delay, e.showing
	return s
}

// Owed is what the trace is owed since the last call: a flip of showing,
// and a summary every LocalEchoSummaryEvery guesses made.
func (e *LocalEcho) Owed() (flip, summary bool) {
	flip, e.flipped = e.flipped, false
	if e.stats.Made-e.reported >= LocalEchoSummaryEvery {
		e.reported, summary = e.stats.Made, true
	}
	return flip, summary
}

func blank(s string) bool { return s == "" || s == " " }

func sameCell(a, b string) bool { return a == b || blank(a) && blank(b) }
