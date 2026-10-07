// Package errsurface owns the client's queue of user-visible failure notices.
// A failure that only reaches the console looks to the user like it just
// disappeared, so every layer that detects one reports here and only the
// render layer reads. It is js-free; the wasm shell contributes pixels and
// timers.
package errsurface

import (
	"fmt"
	"strings"
	"time"
)

// Severity is Error for something the user asked for that did not happen and
// Info for an expected reconciliation. There is no debug tier.
type Severity int

const (
	Info Severity = iota
	Error
)

type Notice struct {
	// ID is stable for the life of the notice, coalesced re-reports included.
	ID int
	// Source is the stable key of the failure site, such as "rpc:MoveTile".
	// One notice exists per source, so a retry loop updates its row in place
	// rather than scrolling the strip.
	Source   string
	Message  string
	Severity Severity
	Count    int
	// deadline is zero for sticky sources.
	deadline time.Time
}

// ExpireAfter is how long a non-sticky notice outlives its last report, so a
// recurring failure stays up while it recurs.
const ExpireAfter = 10 * time.Second

// pluginHealthPrefix keys a namespace's health notice; PluginHealthSource
// spells it and Sticky reads it.
const pluginHealthPrefix = "plugin:"

// PluginHealthSource is the source a namespace's health notice lives under,
// one per uuid, so a flapping source updates its row in place.
func PluginHealthSource(uuid string) string { return pluginHealthPrefix + uuid }

const liveUpdatesPrefix = "live:"

// LiveUpdatesSource is the source a namespace's live-updates-off notice lives
// under, apart from its health notice so the two clear independently.
func LiveUpdatesSource(uuid string) string { return liveUpdatesPrefix + uuid }

// BuildSource keys the notice that the node runs another build than this
// page (client/nodebuild); BuildStuckSource the one that a reload cannot
// fix, apart so only the first carries Reload.
const (
	BuildSource      = "build"
	BuildStuckSource = "build:stuck"
)

// Sticky names an ongoing condition, reported once on the transition, that
// would otherwise expire while still true. Plugin health and live updates
// resolve on the event that ends them; the backend notice can only be
// dismissed; the build notices last as long as the page. This table is the
// one owner; report sites do not choose.
func Sticky(source string) bool {
	return source == "electron:backend" || source == BuildSource || source == BuildStuckSource ||
		strings.HasPrefix(source, pluginHealthPrefix) ||
		strings.HasPrefix(source, liveUpdatesPrefix)
}

// Action is what a notice's button does. A notice with one is not dismissed
// by a press on its row: it stays until the button is pressed, and it never
// folds into the overflow.
type Action int

const (
	NoAction Action = iota
	// Reload loads the page again, through the unload path every reload
	// takes.
	Reload
)

// ActionOf is the one table of which notices carry a button.
func ActionOf(source string) Action {
	if source == BuildSource {
		return Reload
	}
	return NoAction
}

// ButtonLabel is the button's text, "" for a notice with none.
func ButtonLabel(a Action) string {
	if a == Reload {
		return "Reload"
	}
	return ""
}

// maxNotices is a safety valve against an unattended failure loop, not a
// display rule.
const maxNotices = 50

// Surface is the notice queue; use New. Like every client-side store it is not
// safe for concurrent use, the wasm client being single-threaded.
type Surface struct {
	notices []Notice // index 0 is the newest
	nextID  int
}

func New() *Surface { return &Surface{nextID: 1} }

// Report adds a notice or refreshes the one for the same source, keeping its
// ID and restarting its expiry. The caller passes now, so the package is
// clock-free.
func (s *Surface) Report(sev Severity, source, message string, now time.Time) {
	deadline := now.Add(ExpireAfter)
	if Sticky(source) {
		deadline = time.Time{}
	}
	for i := range s.notices {
		if s.notices[i].Source == source {
			n := s.notices[i]
			n.Message = message
			n.Severity = sev
			n.Count++
			n.deadline = deadline
			s.notices = append(s.notices[:i], s.notices[i+1:]...)
			s.notices = append([]Notice{n}, s.notices...)
			return
		}
	}
	n := Notice{ID: s.nextID, Source: source, Message: message, Severity: sev, Count: 1, deadline: deadline}
	s.nextID++
	s.notices = append([]Notice{n}, s.notices...)
	for i := len(s.notices) - 1; len(s.notices) > maxNotices && i >= 0; i-- {
		if ActionOf(s.notices[i].Source) == NoAction {
			s.notices = append(s.notices[:i], s.notices[i+1:]...)
		}
	}
}

// Expire is an explicit mutation on the caller's clock tick, because reading
// never mutates. It reports whether anything changed.
func (s *Surface) Expire(now time.Time) bool {
	kept := s.notices[:0]
	for _, n := range s.notices {
		if n.deadline.IsZero() || n.deadline.After(now) {
			kept = append(kept, n)
		}
	}
	changed := len(kept) != len(s.notices)
	s.notices = kept
	return changed
}

// NextDeadline lets the wasm shell arm one timer instead of polling. The
// duration can be zero or negative; false means nothing expires.
func (s *Surface) NextDeadline(now time.Time) (time.Duration, bool) {
	var soonest time.Time
	for _, n := range s.notices {
		if n.deadline.IsZero() {
			continue
		}
		if soonest.IsZero() || n.deadline.Before(soonest) {
			soonest = n.deadline
		}
	}
	if soonest.IsZero() {
		return 0, false
	}
	return soonest.Sub(now), true
}

// Notices copies the queue, newest first.
func (s *Surface) Notices() []Notice {
	out := make([]Notice, len(s.notices))
	copy(out, s.notices)
	return out
}

func (s *Surface) Len() int { return len(s.notices) }

// Dismiss reports whether it removed a notice, like every mutation here: the
// render layer repaints on the verdict, not on the call.
func (s *Surface) Dismiss(id int) bool {
	return s.remove(func(n Notice) bool { return n.ID == id })
}

// Resolve is how a cleared condition takes its own notice down. Most calls
// come from a call that simply worked and never raised one, so the verdict is
// what keeps a healthy read from repainting the screen.
func (s *Surface) Resolve(source string) bool {
	return s.remove(func(n Notice) bool { return n.Source == source })
}

func (s *Surface) remove(match func(Notice) bool) bool {
	for i := range s.notices {
		if match(s.notices[i]) {
			s.notices = append(s.notices[:i], s.notices[i+1:]...)
			return true
		}
	}
	return false
}

// The strip is reserved layout, not an overlay: the pane tree is laid out into
// the canvas height minus StripHeight, so a WebContentsView tracking pane
// rects can never cover it.

// RowH is one row's height in CSS pixels.
const RowH = 24.0

// MaxRows caps the visible notices; the last row's OverflowCount holds the
// rest.
const MaxRows = 3

func StripHeight(count int) float64 {
	if count <= 0 {
		return 0
	}
	if count > MaxRows {
		count = MaxRows
	}
	return float64(count) * RowH
}

type Row struct {
	Notice        Notice
	Y             float64
	OverflowCount int
}

// Rows lays out top down, notices with a button first, then newest first.
// Render and hit-testing both read it, so they cannot disagree.
func Rows(notices []Notice, stripTop float64) []Row {
	n := len(notices)
	if n == 0 {
		return nil
	}
	ordered := make([]Notice, 0, n)
	for _, pass := range []bool{true, false} {
		for _, nt := range notices {
			if (ActionOf(nt.Source) != NoAction) == pass {
				ordered = append(ordered, nt)
			}
		}
	}
	notices = ordered
	vis := n
	if vis > MaxRows {
		vis = MaxRows
	}
	rows := make([]Row, vis)
	for i := 0; i < vis; i++ {
		rows[i] = Row{Notice: notices[i], Y: stripTop + float64(i)*RowH}
	}
	rows[vis-1].OverflowCount = n - vis
	return rows
}

// Label counts the reports of a notice without a button; one with a button is
// a standing condition whose message is restated as it changes.
func Label(n Notice) string {
	if n.Count > 1 && ActionOf(n.Source) == NoAction {
		return fmt.Sprintf("%s ×%d", n.Message, n.Count)
	}
	return n.Message
}

// Button geometry: inset from the row's right end, the strip spanning width.
const (
	ButtonW     = 64.0
	ButtonInset = 3.0
)

// ButtonRect is where r's button is drawn and pressed; ok is false for a
// notice with none.
func ButtonRect(r Row, width float64) (x, y, w, h float64, ok bool) {
	if ActionOf(r.Notice.Source) == NoAction {
		return 0, 0, 0, 0, false
	}
	return width - ButtonW - ButtonInset, r.Y + ButtonInset, ButtonW, RowH - 2*ButtonInset, true
}

// Press is what a press in the strip did: dismissed a notice, or pressed a
// button whose Action the caller runs.
type Press struct {
	Dismissed bool
	Action    Action
}

// PressAt takes a press at (x, y) in a strip of the given width. A row press
// dismisses its notice unless the notice has a button, which is then the only
// target. The caller has already established that y is at or below stripTop.
func (s *Surface) PressAt(x, y, stripTop, width float64) Press {
	for _, r := range Rows(s.notices, stripTop) {
		if y < r.Y || y >= r.Y+RowH {
			continue
		}
		if a := ActionOf(r.Notice.Source); a != NoAction {
			bx, by, bw, bh, _ := ButtonRect(r, width)
			if x >= bx && x < bx+bw && y >= by && y < by+bh {
				return Press{Action: a}
			}
			return Press{}
		}
		return Press{Dismissed: s.Dismiss(r.Notice.ID)}
	}
	return Press{}
}
