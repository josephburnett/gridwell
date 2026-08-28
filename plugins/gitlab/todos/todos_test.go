package todos

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWeekStartIsMondayUTC(t *testing.T) {
	cases := map[string]string{
		"2026-08-24T00:00:00Z":      "2026-08-24", // a Monday
		"2026-08-30T23:59:59Z":      "2026-08-24", // the Sunday after
		"2026-08-23T23:59:59Z":      "2026-08-17", // the Sunday before
		"2026-08-24T03:00:00+05:00": "2026-08-17", // 22:00 UTC the Sunday before
	}
	for in, want := range cases {
		got := WeekKey(WeekStart(at(in)))
		if got != WeekPrefix+want {
			t.Errorf("WeekStart(%s) = %s, want %s", in, got, want)
		}
	}
	if _, ok := ParseWeekKey("week:2026-08-25"); ok {
		t.Error("a Tuesday parsed as a week key")
	}
	if s, ok := ParseWeekKey("week:2026-08-24"); !ok || !s.Equal(at("2026-08-24T00:00:00Z")) {
		t.Errorf("ParseWeekKey = %v, %v", s, ok)
	}
}

func TestWeekCellIsACalendarPage(t *testing.T) {
	cell := func(s string) [2]int64 {
		x, y := WeekCell(at(s))
		return [2]int64{x, y}
	}
	// August 2026 is row 0: Mondays the 3rd, 10th, 17th, 24th, 31st → x 0..4.
	if cell("2026-08-03T00:00:00Z") != [2]int64{0, 0} || cell("2026-08-24T00:00:00Z") != [2]int64{3, 0} || cell("2026-08-31T00:00:00Z") != [2]int64{4, 0} {
		t.Errorf("august cells: %v %v %v", cell("2026-08-03T00:00:00Z"), cell("2026-08-24T00:00:00Z"), cell("2026-08-31T00:00:00Z"))
	}
	// September climbs, July descends; the year boundary keeps counting.
	if cell("2026-09-07T00:00:00Z") != [2]int64{0, -1} || cell("2026-07-27T00:00:00Z") != [2]int64{3, 1} || cell("2025-12-29T00:00:00Z") != [2]int64{4, 8} {
		t.Errorf("month rows: %v %v %v", cell("2026-09-07T00:00:00Z"), cell("2026-07-27T00:00:00Z"), cell("2025-12-29T00:00:00Z"))
	}
}

func TestLabelAndRef(t *testing.T) {
	var mr Todo
	mr.TargetType, mr.Target.IID, mr.Target.Title, mr.State = "MergeRequest", 42, "Fix it", StatePending
	if mr.Label() != "!42 Fix it" {
		t.Errorf("label = %q", mr.Label())
	}
	mr.State = StateDone
	if mr.Label() != "✓ !42 Fix it" {
		t.Errorf("done label = %q", mr.Label())
	}
	var commit Todo
	commit.TargetType, commit.ActionName, commit.Body = "Commit", "build_failed", "pipeline exploded\nmore"
	if commit.Label() != "pipeline exploded" {
		t.Errorf("commit label = %q", commit.Label())
	}
	commit.Body = ""
	if commit.Label() != "build failed Commit" {
		t.Errorf("bare label = %q", commit.Label())
	}
	if id, ok := ParseKey("todo:7"); !ok || id != 7 {
		t.Errorf("ParseKey = %d %v", id, ok)
	}
	if _, ok := ParseKey("week:2026-08-24"); ok {
		t.Error("a week key parsed as a todo key")
	}
}

// fakeSource is a paged GitLab: pages of at most per, newest first
// unless ascending, recording every page it served.
type fakeSource struct {
	pending, done []Todo
	per           int
	ascending     bool
	calls         []string
	err           error
}

func (f *fakeSource) Page(_ context.Context, state string, page int) ([]Todo, bool, error) {
	f.calls = append(f.calls, state+"/"+itoa(page))
	if f.err != nil {
		return nil, false, f.err
	}
	src := f.pending
	if state == StateDone {
		src = f.done
	}
	ordered := make([]Todo, len(src))
	copy(ordered, src)
	// newest first by default
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			if (ordered[j].CreatedAt.After(ordered[i].CreatedAt)) != f.ascending {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}
	start := (page - 1) * f.per
	if start >= len(ordered) {
		return nil, false, nil
	}
	end := start + f.per
	if end > len(ordered) {
		end = len(ordered)
	}
	return ordered[start:end], end < len(ordered), nil
}

func itoa(i int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+i))) }

func mk(id int64, created string, state string) Todo {
	var t Todo
	t.ID, t.CreatedAt, t.State = id, at(created), state
	t.TargetType, t.Target.IID, t.Target.Title = "Issue", id, "t"+itoa(int(id))
	return t
}

func TestSyncAllWalksPendingFullyAndDoneUntilNothingNew(t *testing.T) {
	src := &fakeSource{per: 2,
		pending: []Todo{mk(1, "2026-08-10T10:00:00Z", StatePending), mk(3, "2026-08-18T10:00:00Z", StatePending), mk(5, "2026-08-25T10:00:00Z", StatePending)},
		done:    []Todo{mk(2, "2026-08-11T10:00:00Z", StateDone), mk(4, "2026-08-19T10:00:00Z", StateDone), mk(6, "2026-08-25T11:00:00Z", StateDone)},
	}
	m := NewMemory()
	if err := m.Sync(context.Background(), src, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(m.All()) != 6 {
		t.Fatalf("remembered %d, want 6", len(m.All()))
	}
	// The outset walks everything: 2 pending pages, 2 done pages.
	if got := strings.Join(src.calls, " "); got != "pending/1 pending/2 done/1 done/2" {
		t.Errorf("calls = %s", got)
	}
	// A second full sync: pending again in full, done stops at page 1
	// (nothing unknown there).
	src.calls = nil
	if err := m.Sync(context.Background(), src, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(src.calls, " "); got != "pending/1 pending/2 done/1" {
		t.Errorf("resync calls = %s", got)
	}
}

func TestSyncDerivesDoneFromAbsenceInPending(t *testing.T) {
	src := &fakeSource{per: 10, pending: []Todo{mk(1, "2026-08-10T10:00:00Z", StatePending), mk(2, "2026-08-24T10:00:00Z", StatePending)}}
	m := NewMemory()
	if err := m.Sync(context.Background(), src, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// Todo 1 is marked done in GitLab AND vanishes (its target deleted):
	// it is in neither list any more.
	src.pending = src.pending[1:]
	if err := m.Sync(context.Background(), src, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Get(1)
	if !got.Done() {
		t.Errorf("todo 1 state = %s, want done", got.State)
	}
	if two, _ := m.Get(2); two.Done() {
		t.Error("todo 2 flipped without cause")
	}
	// Restored in GitLab: GitLab's record wins again.
	src.pending = append(src.pending, mk(1, "2026-08-10T10:00:00Z", StatePending))
	_ = m.Sync(context.Background(), src, time.Time{})
	if one, _ := m.Get(1); one.Done() {
		t.Error("a restored todo stayed done")
	}
}

func TestSyncSinceStopsAtTheWeekAndFlipsOnlyWithinCoverage(t *testing.T) {
	week := at("2026-08-17T00:00:00Z")
	src := &fakeSource{per: 1,
		pending: []Todo{
			mk(10, "2026-06-01T10:00:00Z", StatePending),
			mk(1, "2026-07-01T10:00:00Z", StatePending), mk(2, "2026-07-08T10:00:00Z", StatePending),
			mk(3, "2026-08-18T10:00:00Z", StatePending), mk(4, "2026-08-20T10:00:00Z", StatePending),
			mk(5, "2026-08-25T10:00:00Z", StatePending),
		},
		done: []Todo{mk(6, "2026-07-02T10:00:00Z", StateDone), mk(7, "2026-08-19T10:00:00Z", StateDone), mk(8, "2026-08-26T10:00:00Z", StateDone)},
	}
	m := NewMemory()
	if err := m.Sync(context.Background(), src, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// Todo 3 (in the week) and todo 1 (before the week) both leave pending.
	src.pending = []Todo{src.pending[0], src.pending[2], src.pending[4], src.pending[5]}
	src.calls = nil
	if err := m.Sync(context.Background(), src, week); err != nil {
		t.Fatal(err)
	}
	// Pending: pages {5}, {4}, {2} — 2 is before the week, stop with
	// pages left. Done: page 1 = {8}, nothing unknown, stop.
	if got := strings.Join(src.calls, " "); got != "pending/1 pending/2 pending/3 done/1" {
		t.Errorf("targeted calls = %s", got)
	}
	if three, _ := m.Get(3); !three.Done() {
		t.Error("todo 3 (inside the walked window, absent from pending) must be done")
	}
	if one, _ := m.Get(1); one.Done() {
		t.Error("todo 1 is outside the walk's coverage: the targeted sync must not judge it")
	}
}

func TestSyncSinceRefusesEarlyStopOnUnorderedPages(t *testing.T) {
	week := at("2026-08-17T00:00:00Z")
	src := &fakeSource{per: 2, ascending: true,
		pending: []Todo{mk(1, "2026-07-01T10:00:00Z", StatePending), mk(2, "2026-07-08T10:00:00Z", StatePending), mk(3, "2026-08-18T10:00:00Z", StatePending), mk(4, "2026-08-20T10:00:00Z", StatePending)},
	}
	m := NewMemory()
	if err := m.Sync(context.Background(), src, week); err != nil {
		t.Fatal(err)
	}
	// Ascending pages: page 1's last item is older than the week, but
	// the early stop must not fire — the walk runs to the end.
	if got := strings.Join(src.calls, " "); got != "pending/1 pending/2 done/1" {
		t.Errorf("calls = %s", got)
	}
	for _, id := range []int64{1, 2, 3, 4} {
		if got, ok := m.Get(id); !ok || got.Done() {
			t.Errorf("todo %d: ok=%v done=%v — a live todo was judged done", id, ok, got.Done())
		}
	}
}

func TestSyncErrorLeavesMemoryUntouched(t *testing.T) {
	src := &fakeSource{per: 10, pending: []Todo{mk(1, "2026-08-10T10:00:00Z", StatePending)}}
	m := NewMemory()
	_ = m.Sync(context.Background(), src, time.Time{})
	src.err = errors.New("boom")
	if err := m.Sync(context.Background(), src, time.Time{}); err == nil {
		t.Fatal("expected the source error")
	}
	if one, ok := m.Get(1); !ok || one.Done() {
		t.Error("a failed walk must not flip anything")
	}
}

func TestWeeksAndEntries(t *testing.T) {
	m := NewMemory()
	m.absorb([]Todo{
		mk(1, "2026-08-18T10:00:00Z", StatePending), // Tue, week of 08-17
		mk(2, "2026-08-18T12:00:00Z", StateDone),    // Tue, same day, second row
		mk(3, "2026-08-23T10:00:00Z", StateDone),    // Sun
		mk(4, "2026-08-25T10:00:00Z", StatePending), // week of 08-24
	})
	weeks := m.Weeks()
	if len(weeks) != 2 || !weeks[0].Start.Equal(at("2026-08-24T00:00:00Z")) || weeks[1].Open != 1 || weeks[1].Done != 2 {
		t.Fatalf("weeks = %+v", weeks)
	}
	root := RootEntries(weeks)
	if root[0].Key != "week:2026-08-24" || root[0].ChildContext != root[0].Key || root[0].PlacementHint.X != 3 || root[0].PlacementHint.Y != 0 || root[1].PlacementHint.X != 2 || root[1].PlacementHint.Y != 0 {
		t.Errorf("root entries = %v", root)
	}
	if root[1].Label != "2026-08-17 · 1 open · 2 done" {
		t.Errorf("week label = %q", root[1].Label)
	}
	wk := WeekEntries(at("2026-08-17T00:00:00Z"), m.Week(at("2026-08-17T00:00:00Z")))
	if len(wk) != 3 {
		t.Fatalf("week entries = %d", len(wk))
	}
	if h := wk[0].PlacementHint; h.X != 1*TodoTileW || h.Y != 0 || h.W != TodoTileW {
		t.Errorf("Tuesday first hint = %+v", h)
	}
	if h := wk[1].PlacementHint; h.X != 1*TodoTileW || h.Y != 1 {
		t.Errorf("Tuesday second hint = %+v", h)
	}
	if h := wk[2].PlacementHint; h.X != 6*TodoTileW || h.Y != 0 {
		t.Errorf("Sunday hint = %+v", h)
	}
	if wk[0].ServesPage || wk[0].Kind != "text" || wk[1].Label != "✓ #2 t2" || wk[1].StatusDetail != StateDone {
		t.Errorf("entry facts = %v", wk[1])
	}
}

func TestMarkdownCarriesTheEssentials(t *testing.T) {
	td := mk(9, "2026-08-18T10:00:00Z", StatePending)
	td.Target.Title = "Fix the widget"
	td.ActionName = "review_requested"
	td.Author.Name, td.Author.Username = "Ada Lovelace", "ada"
	td.Project.PathWithNamespace = "g/p"
	td.Body = "Could you   look at\nthis one?   " + strings.Repeat("x", 400)
	td.TargetURL = "https://gitlab.example/g/p/-/issues/9"
	got := string(Markdown(&td))
	for _, want := range []string{
		"# #9 Fix the widget",
		"review requested — from Ada Lovelace (@ada) · g/p · 2026-08-18",
		"> Could you look at this one? xxx",
		"[Open #9 in GitLab](https://gitlab.example/g/p/-/issues/9)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "…") || len([]rune(td.Snippet())) > SnippetRunes+1 {
		t.Errorf("the snippet must be bounded: %d runes", len([]rune(td.Snippet())))
	}
	if td.Label() != "Ada Lovelace: #9 Fix the widget" {
		t.Errorf("label = %q", td.Label())
	}
	td.State = StateDone
	if got := string(Markdown(&td)); !strings.HasPrefix(got, "# ✓ #9") || !strings.Contains(got, "· done") {
		t.Errorf("done must show in the heading and the line:\n%s", got)
	}
	if !strings.Contains(string(GoneMarkdown("todo:<1>")), "todo:<1>") {
		t.Error("the gone notice names the key")
	}
}

// A targeted week walk absorbs one done page PAST the week boundary
// (the page that proves the boundary was crossed). A later FULL walk
// then found done page 1 fully known and stopped — "nothing unknown
// means every older one is known" is only true once a walk has reached
// the END of the done list, and no walk had.
func TestSyncFullAfterTargetedWalksDoneToTheEnd(t *testing.T) {
	week := at("2026-08-17T00:00:00Z")
	src := &fakeSource{per: 2,
		pending: []Todo{mk(1, "2026-08-18T10:00:00Z", StatePending)},
		done: []Todo{
			mk(9, "2026-08-19T10:00:00Z", StateDone), mk(8, "2026-08-18T12:00:00Z", StateDone), // in the week
			mk(7, "2026-08-10T10:00:00Z", StateDone), mk(6, "2026-08-09T10:00:00Z", StateDone), // the page past the boundary
			mk(5, "2026-07-01T10:00:00Z", StateDone), mk(4, "2026-06-01T10:00:00Z", StateDone), // never walked by the week
		},
	}
	m := NewMemory()
	if err := m.Sync(context.Background(), src, week); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(src.calls, " "); got != "pending/1 done/1 done/2" {
		t.Fatalf("targeted calls = %s", got)
	}
	if n := len(m.All()); n != 5 {
		t.Fatalf("after the week walk remembered %d, want 5", n)
	}
	src.calls = nil
	if err := m.Sync(context.Background(), src, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if n := len(m.All()); n != 7 {
		t.Errorf("after the full walk remembered %d, want 7 (calls %s)", n, strings.Join(src.calls, " "))
	}
	// Now the done list HAS been walked to its end: the next full walk
	// may stop at the first fully-known page.
	src.calls = nil
	_ = m.Sync(context.Background(), src, time.Time{})
	if got := strings.Join(src.calls, " "); got != "pending/1 done/1" {
		t.Errorf("resync calls = %s", got)
	}
}
