package wsbar

import (
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

func TestBandFocus(t *testing.T) {
	single := map[string]pane.Rect{"p1": {X: 0, Y: 0, W: 800, H: 600}}
	sideBySide := map[string]pane.Rect{
		"L": {X: 0, Y: 0, W: 400, H: 600},
		"R": {X: 400, Y: 0, W: 400, H: 600},
	}
	stacked := map[string]pane.Rect{
		"L": {X: 0, Y: 0, W: 400, H: 600},
		"T": {X: 400, Y: 0, W: 400, H: 300},
		"B": {X: 400, Y: 300, W: 400, H: 300},
	}
	three := map[string]pane.Rect{
		"L":  {X: 0, Y: 0, W: 400, H: 600},
		"R1": {X: 400, Y: 0, W: 400, H: 200},
		"R2": {X: 400, Y: 200, W: 400, H: 200},
		"R3": {X: 400, Y: 400, W: 400, H: 200},
	}
	cases := []struct {
		name    string
		rects   map[string]pane.Rect
		recency []string
		x       float64
		want    string
		wantOK  bool
	}{
		{"a single pane takes any x", single, []string{"p1"}, 700, "p1", true},
		{"side by side, the pane under x", sideBySide, []string{"L", "R"}, 500, "R", true},
		{"side by side, the left edge is the left pane's", sideBySide, []string{"R", "L"}, 0, "L", true},
		{"a column of two, the most recent wins", stacked, []string{"L", "B", "T"}, 600, "B", true},
		{"a column of two, recency flipped", stacked, []string{"L", "T", "B"}, 600, "T", true},
		{"a column of three, R2 most recent", three, []string{"L", "R2", "R3", "R1"}, 600, "R2", true},
		{"a column of three, R3 most recent", three, []string{"L", "R3", "R1", "R2"}, 600, "R3", true},
		{"a column of three, R1 most recent", three, []string{"L", "R1", "R2", "R3"}, 600, "R1", true},
		{"a column never focused falls to its lowest pane", three, []string{"L"}, 600, "R3", true},
		{"a closed pane in recency is skipped", stacked, []string{"gone", "T"}, 600, "T", true},
		{"outside every column, nothing", sideBySide, []string{"L", "R"}, 900, "", false},
		{"left of every column, nothing", sideBySide, []string{"L", "R"}, -1, "", false},
		{"no panes, nothing", map[string]pane.Rect{}, nil, 10, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := BandFocus(tc.rects, tc.recency, tc.x)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("BandFocus(x=%v) = %q, %v; want %q, %v", tc.x, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
