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

// Sticky names an ongoing condition, reported once on the transition, that
// would otherwise expire while still true. Plugin health resolves on the
// recovery event; the backend notice can only be dismissed. This table is the
// one owner; report sites do not choose.
func Sticky(source string) bool {
	return source == "electron:backend" || strings.HasPrefix(source, pluginHealthPrefix)
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
	if len(s.notices) > maxNotices {
		s.notices = s.notices[:maxNotices]
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

func (s *Surface) Dismiss(id int) {
	for i := range s.notices {
		if s.notices[i].ID == id {
			s.notices = append(s.notices[:i], s.notices[i+1:]...)
			return
		}
	}
}

// Resolve is how a cleared condition takes its own notice down.
func (s *Surface) Resolve(source string) {
	for i := range s.notices {
		if s.notices[i].Source == source {
			s.notices = append(s.notices[:i], s.notices[i+1:]...)
			return
		}
	}
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

// Rows lays out top down, newest first. Render and hit-testing both read it,
// so they cannot disagree.
func Rows(notices []Notice, stripTop float64) []Row {
	n := len(notices)
	if n == 0 {
		return nil
	}
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

func Label(n Notice) string {
	if n.Count > 1 {
		return fmt.Sprintf("%s ×%d", n.Message, n.Count)
	}
	return n.Message
}

// DismissAt takes the whole row as the target, with no separate close box. The
// caller has already established that y is at or below stripTop.
func (s *Surface) DismissAt(y, stripTop float64) bool {
	rows := Rows(s.notices, stripTop)
	for _, r := range rows {
		if y >= r.Y && y < r.Y+RowH {
			s.Dismiss(r.Notice.ID)
			return true
		}
	}
	return false
}
