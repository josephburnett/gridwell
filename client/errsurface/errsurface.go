// Package errsurface owns the client's queue of user-visible failure
// notices — the single answer to "what has gone wrong that the user has not
// yet seen." Charter §6: a failure that only reaches the console presents to
// the user as "it just disappeared." Every layer that detects a failure
// (wasm RPC dispatch, stream clients, the Electron host via its error IPC
// event, server health events) *reports* here; only the render layer *reads*
// here. No other code holds or draws error state.
//
// The package is js-free and pure so the whole policy — coalescing, ordering,
// capacity, strip geometry, dismiss hit-testing — is table-testable without a
// browser. The wasm shell contributes only pixels and event plumbing.
package errsurface

import "fmt"

// Severity classifies a notice for display. There are exactly two: Error is
// an unexpected failure (something the user asked for did not happen), Info
// is an expected-but-noteworthy reconciliation (a lost version race that was
// resolved by refetching). No Debug tier — that is what the console is for.
type Severity int

const (
	Info Severity = iota
	Error
)

// Notice is one row on the surface.
type Notice struct {
	// ID is a monotonically-assigned handle for dismissal. Stable for the
	// life of the notice, including across coalesced re-reports.
	ID int
	// Source is the stable key of the failure site, e.g. "rpc:MoveTile",
	// "events", "electron:webview". One notice exists per source: a retry
	// loop re-reporting the same source updates its row in place rather
	// than scrolling the strip.
	Source string
	// Message is the most recent human-readable failure text for Source.
	Message string
	Severity Severity
	// Count is how many times Source has reported since it was last
	// dismissed/resolved. Rendered as a "×N" suffix when > 1.
	Count int
}

// maxNotices bounds the queue so an unattended failure loop cannot grow
// memory; oldest notices fall off. Far above MaxRows — the bound is a
// safety valve, not a display rule.
const maxNotices = 50

// Surface is the notice queue. Zero value is not ready; use New.
// Not safe for concurrent use — the wasm client is single-threaded
// (goroutines interleave, never run in parallel), matching every other
// client-side store.
type Surface struct {
	notices []Notice // index 0 = newest
	nextID  int
}

func New() *Surface { return &Surface{nextID: 1} }

// Report adds a notice, or refreshes the existing notice for the same
// source: latest message and severity win, Count increments, and the row
// moves to the top (newest) position keeping its ID.
func (s *Surface) Report(sev Severity, source, message string) {
	for i := range s.notices {
		if s.notices[i].Source == source {
			n := s.notices[i]
			n.Message = message
			n.Severity = sev
			n.Count++
			s.notices = append(s.notices[:i], s.notices[i+1:]...)
			s.notices = append([]Notice{n}, s.notices...)
			return
		}
	}
	n := Notice{ID: s.nextID, Source: source, Message: message, Severity: sev, Count: 1}
	s.nextID++
	s.notices = append([]Notice{n}, s.notices...)
	if len(s.notices) > maxNotices {
		s.notices = s.notices[:maxNotices]
	}
}

// Notices returns the queue, newest first. The slice is a copy; mutating it
// does not affect the surface.
func (s *Surface) Notices() []Notice {
	out := make([]Notice, len(s.notices))
	copy(out, s.notices)
	return out
}

func (s *Surface) Len() int { return len(s.notices) }

// Dismiss removes the notice with the given ID; unknown IDs are a no-op.
func (s *Surface) Dismiss(id int) {
	for i := range s.notices {
		if s.notices[i].ID == id {
			s.notices = append(s.notices[:i], s.notices[i+1:]...)
			return
		}
	}
}

// Resolve removes the notice for source, if any. The "condition cleared"
// path: e.g. the SSE stream reconnecting resolves its own disconnect notice
// so a healed failure does not linger as stale bad news.
func (s *Surface) Resolve(source string) {
	for i := range s.notices {
		if s.notices[i].Source == source {
			s.notices = append(s.notices[:i], s.notices[i+1:]...)
			return
		}
	}
}

// ── strip geometry ───────────────────────────────────────────────────────────
//
// The strip is *reserved layout*, not an overlay: the pane tree is laid out
// into (canvas height − StripHeight), so the strip can never be covered by a
// native WebContentsView (those track pane rects). An error is only surfaced
// if it owns pixels nothing else can paint over.

// RowH is the height of one notice row in CSS pixels.
const RowH = 24.0

// MaxRows caps how many notices are visible at once; older ones are
// summarized by the OverflowCount of the last visible row.
const MaxRows = 3

// StripHeight is the canvas height to reserve for count pending notices.
func StripHeight(count int) float64 {
	if count <= 0 {
		return 0
	}
	if count > MaxRows {
		count = MaxRows
	}
	return float64(count) * RowH
}

// Row is one rendered line of the strip: which notice, its top edge in
// canvas coordinates, and how many further notices are hidden behind it
// (non-zero only on the last visible row).
type Row struct {
	Notice        Notice
	Y             float64
	OverflowCount int
}

// Rows lays out the visible rows top-down (newest first) given the strip's
// top edge in canvas coordinates. Render and hit-testing both read this, so
// they cannot disagree.
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

// Label is the display text for a notice: "message" or "message ×N".
func Label(n Notice) string {
	if n.Count > 1 {
		return fmt.Sprintf("%s ×%d", n.Message, n.Count)
	}
	return n.Message
}

// DismissAt handles a click at canvas y within the strip (caller has already
// established x is on the canvas and y >= stripTop): it dismisses the notice
// whose row contains y and reports whether anything changed. Clicking a row
// is the dismiss gesture — the whole row is the target, no tiny close box.
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
