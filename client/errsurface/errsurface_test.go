package errsurface

import (
	"fmt"
	"testing"
	"time"
)

// t0 is an arbitrary fixed instant for tests that don't care about time.
var t0 = time.Unix(1_000_000, 0)

func TestReportOrderNewestFirst(t *testing.T) {
	s := New()
	s.Report(Error, "a", "first", t0)
	s.Report(Error, "b", "second", t0)
	got := s.Notices()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Source != "b" || got[1].Source != "a" {
		t.Errorf("order = %s,%s want b,a (newest first)", got[0].Source, got[1].Source)
	}
}

func TestReportCoalescesBySource(t *testing.T) {
	s := New()
	s.Report(Error, "events", "disconnected", t0)
	id := s.Notices()[0].ID
	s.Report(Error, "other", "x", t0)
	s.Report(Error, "events", "still disconnected", t0)

	got := s.Notices()
	if len(got) != 2 {
		t.Fatalf("retry loop stacked rows: len = %d, want 2", len(got))
	}
	n := got[0]
	if n.Source != "events" {
		t.Errorf("re-report did not move row to top: got %q", n.Source)
	}
	if n.Message != "still disconnected" {
		t.Errorf("message not updated: %q", n.Message)
	}
	if n.Count != 2 {
		t.Errorf("Count = %d, want 2", n.Count)
	}
	if n.ID != id {
		t.Errorf("coalesce changed ID %d → %d; must stay stable", id, n.ID)
	}
}

func TestCoalesceCanRaiseSeverity(t *testing.T) {
	s := New()
	s.Report(Info, "conflict:UpdateText", "reloaded", t0)
	s.Report(Error, "conflict:UpdateText", "save failed", t0)
	if got := s.Notices()[0].Severity; got != Error {
		t.Errorf("severity = %v, want Error (latest wins)", got)
	}
}

func TestDismissAndResolve(t *testing.T) {
	s := New()
	s.Report(Error, "a", "x", t0)
	s.Report(Error, "b", "y", t0)
	s.Dismiss(s.Notices()[1].ID) // dismiss "a"
	if s.Len() != 1 || s.Notices()[0].Source != "b" {
		t.Fatalf("dismiss removed wrong row: %+v", s.Notices())
	}
	s.Dismiss(999) // unknown: no-op
	if s.Len() != 1 {
		t.Fatalf("unknown dismiss mutated queue")
	}
	s.Resolve("b")
	if s.Len() != 0 {
		t.Fatalf("resolve did not clear source b")
	}
	s.Resolve("b") // absent: no-op
}

func TestDismissedSourceReportsFreshCount(t *testing.T) {
	s := New()
	s.Report(Error, "a", "x", t0)
	s.Report(Error, "a", "x", t0)
	s.Resolve("a")
	s.Report(Error, "a", "x", t0)
	if got := s.Notices()[0].Count; got != 1 {
		t.Errorf("Count after resolve+report = %d, want 1 (count is since last dismissal)", got)
	}
}

func TestCapacityDropsOldest(t *testing.T) {
	s := New()
	for i := 0; i < maxNotices+10; i++ {
		s.Report(Error, fmt.Sprintf("src%d", i), "m", t0)
	}
	got := s.Notices()
	if len(got) != maxNotices {
		t.Fatalf("len = %d, want cap %d", len(got), maxNotices)
	}
	if got[0].Source != fmt.Sprintf("src%d", maxNotices+9) {
		t.Errorf("newest not retained: %q", got[0].Source)
	}
	if got[len(got)-1].Source == "src0" {
		t.Errorf("oldest should have been dropped")
	}
}

func TestStripHeight(t *testing.T) {
	cases := []struct {
		count int
		want  float64
	}{
		{0, 0}, {1, RowH}, {2, 2 * RowH}, {MaxRows, MaxRows * RowH},
		{MaxRows + 5, MaxRows * RowH}, // display is capped; height must not grow unbounded
	}
	for _, c := range cases {
		if got := StripHeight(c.count); got != c.want {
			t.Errorf("StripHeight(%d) = %v, want %v", c.count, got, c.want)
		}
	}
}

func TestRowsLayoutAndOverflow(t *testing.T) {
	s := New()
	for i := 0; i < MaxRows+2; i++ {
		s.Report(Error, fmt.Sprintf("s%d", i), "m", t0)
	}
	rows := Rows(s.Notices(), 700)
	if len(rows) != MaxRows {
		t.Fatalf("rows = %d, want %d", len(rows), MaxRows)
	}
	for i, r := range rows {
		want := 700 + float64(i)*RowH
		if r.Y != want {
			t.Errorf("row %d Y = %v, want %v", i, r.Y, want)
		}
	}
	if rows[MaxRows-1].OverflowCount != 2 {
		t.Errorf("overflow = %d, want 2", rows[MaxRows-1].OverflowCount)
	}
	if rows[0].OverflowCount != 0 {
		t.Errorf("overflow marker must sit only on the last row")
	}
	if Rows(nil, 0) != nil {
		t.Errorf("Rows(nil) should be nil")
	}
}

func TestLabel(t *testing.T) {
	if got := Label(Notice{Message: "m", Count: 1}); got != "m" {
		t.Errorf("Label = %q", got)
	}
	if got := Label(Notice{Message: "m", Count: 3}); got != "m ×3" {
		t.Errorf("Label = %q", got)
	}
}

func TestSticky(t *testing.T) {
	cases := []struct {
		source string
		want   bool
	}{
		// Ongoing conditions with an explicit heal signal: stay until resolved.
		{"plugin:0b6f3a", true},
		{"electron:backend", true},
		// One-shot events: fade once they stop recurring.
		{"rpc:MoveTile", false},
		{"events", false}, // the SSE retry loop re-reports every second while down
		{"electron:webview", false},
		{"electron:session", false},
		{"conflict:UpdateText", false},
		{"textedit", false},
	}
	for _, c := range cases {
		if got := Sticky(c.source); got != c.want {
			t.Errorf("Sticky(%q) = %v, want %v", c.source, got, c.want)
		}
	}
}

func TestNonStickyNoticeExpires(t *testing.T) {
	s := New()
	s.Report(Error, "rpc:MoveTile", "move failed", t0)
	if s.Expire(t0.Add(ExpireAfter - time.Millisecond)) {
		t.Fatalf("notice expired before its deadline")
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1 before deadline", s.Len())
	}
	if !s.Expire(t0.Add(ExpireAfter)) {
		t.Fatalf("Expire at deadline reported no change")
	}
	if s.Len() != 0 {
		t.Fatalf("len = %d, want 0 after deadline", s.Len())
	}
}

func TestReReportRefreshesDeadline(t *testing.T) {
	// A failure that keeps happening must stay visible: each re-report
	// pushes the deadline out, so only ExpireAfter of *silence* clears it.
	s := New()
	s.Report(Error, "rpc:UpdateText", "save failed", t0)
	t1 := t0.Add(ExpireAfter / 2)
	s.Report(Error, "rpc:UpdateText", "save failed", t1)
	if s.Expire(t0.Add(ExpireAfter)) {
		t.Fatalf("recurring notice expired on its stale first deadline")
	}
	if !s.Expire(t1.Add(ExpireAfter)) {
		t.Fatalf("notice did not expire after silence")
	}
}

func TestStickySourcesNeverExpire(t *testing.T) {
	for _, src := range []string{"plugin:abc", "electron:backend"} {
		s := New()
		s.Report(Error, src, "down", t0)
		if s.Expire(t0.Add(100 * ExpireAfter)) {
			t.Errorf("sticky source %q expired; must persist until Resolve/Dismiss", src)
		}
		s.Resolve(src)
		if s.Len() != 0 {
			t.Errorf("Resolve did not clear sticky source %q", src)
		}
	}
}

func TestExpireRemovesOnlyExpired(t *testing.T) {
	s := New()
	s.Report(Error, "plugin:abc", "down", t0) // sticky
	s.Report(Error, "old", "x", t0)
	s.Report(Error, "fresh", "y", t0.Add(ExpireAfter/2))
	if !s.Expire(t0.Add(ExpireAfter)) {
		t.Fatalf("Expire reported no change")
	}
	got := s.Notices()
	if len(got) != 2 || got[0].Source != "fresh" || got[1].Source != "plugin:abc" {
		t.Fatalf("wrong survivors: %+v", got)
	}
}

func TestNextDeadline(t *testing.T) {
	s := New()
	if _, ok := s.NextDeadline(t0); ok {
		t.Fatalf("empty surface has no deadline")
	}
	s.Report(Error, "plugin:abc", "down", t0) // sticky: no deadline
	if _, ok := s.NextDeadline(t0); ok {
		t.Fatalf("sticky-only surface has no deadline")
	}
	s.Report(Error, "later", "x", t0.Add(time.Second))
	s.Report(Error, "sooner", "y", t0)
	d, ok := s.NextDeadline(t0.Add(time.Second))
	if !ok {
		t.Fatalf("no deadline with two expiring notices")
	}
	if want := ExpireAfter - time.Second; d != want {
		t.Errorf("NextDeadline = %v, want %v (the soonest deadline)", d, want)
	}
}

func TestDismissAt(t *testing.T) {
	s := New()
	s.Report(Error, "a", "x", t0) // will be row 1 (older)
	s.Report(Error, "b", "y", t0) // row 0 (newest)
	top := 500.0

	// Click in the second row dismisses the older notice "a".
	if !s.DismissAt(top+RowH+1, top) {
		t.Fatalf("click in row 1 did not dismiss")
	}
	if s.Len() != 1 || s.Notices()[0].Source != "b" {
		t.Fatalf("wrong notice dismissed: %+v", s.Notices())
	}
	// Click below the populated rows: no-op.
	if s.DismissAt(top+RowH+1, top) {
		t.Errorf("click below last row must not dismiss")
	}
	if !s.DismissAt(top+1, top) {
		t.Fatalf("click in row 0 did not dismiss")
	}
	if s.Len() != 0 {
		t.Fatalf("queue not empty: %+v", s.Notices())
	}
}
