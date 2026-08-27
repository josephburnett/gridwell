package todos

import (
	"time"

	cpv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
)

// RootEntries derives the landing grid: one well per week, newest
// first, hinted as a calendar — a row per month anchored by HintEpoch
// (this month at y=0, newer up, older down), the month's weeks left to
// right (WeekCell). Labels carry the counts, so a week's face changes
// as its todos complete.
func RootEntries(weeks []WeekSummary) []*cpv1.Entry {
	out := make([]*cpv1.Entry, 0, len(weeks))
	for _, w := range weeks {
		key := WeekKey(w.Start)
		x, y := WeekCell(w.Start)
		out = append(out, &cpv1.Entry{
			Key:           key,
			Kind:          "well",
			Label:         WeekLabel(w.Start, w.Open, w.Done),
			ChildContext:  key,
			PlacementHint: &cpv1.PlacementHint{X: x, Y: y, W: 1, H: 1},
		})
	}
	return out
}

// TodoTileW is a todo tile's hinted width: two cells, so the label
// reads; weekday columns are spaced to match.
const TodoTileW = 2

// WeekEntries derives one week's grid: every todo created that week as
// a page tile (its content is the served HTML), hinted like a calendar
// — weekday columns Monday..Sunday, rows in creation order within the
// day. The hint seeds first placement only; the user's arrangement
// wins from then on (the layout engine's rule).
func WeekEntries(start time.Time, todos []Todo) []*cpv1.Entry {
	perDay := map[int]int64{}
	out := make([]*cpv1.Entry, 0, len(todos))
	for i := range todos {
		t := &todos[i]
		day := int(t.CreatedAt.UTC().Sub(start).Hours() / 24)
		if day < 0 || day > 6 {
			continue // not this week's — the caller filtered, but a hint must never land off-grid
		}
		row := perDay[day]
		perDay[day] = row + 1
		out = append(out, &cpv1.Entry{
			Key:          t.Key(),
			Kind:         "text",
			Label:        t.Label(),
			ServesPage:   true,
			StatusDetail: t.State,
			PreviewStamp: t.PreviewStamp(),
			PlacementHint: &cpv1.PlacementHint{
				X: int64(day) * TodoTileW, Y: row, W: TodoTileW, H: 1,
			},
		})
	}
	return out
}
