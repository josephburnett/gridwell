package rpc

import "testing"

func TestViewRectContains(t *testing.T) {
	cases := []struct {
		name string
		r    ViewRect
		x, y int64
		w, h int64
		want bool
	}{
		{"exact overlap", ViewRect{0, 0, 10, 10}, 0, 0, 10, 10, true},
		{"strictly inside", ViewRect{0, 0, 10, 10}, 2, 3, 4, 5, true},
		{"flush right edge", ViewRect{0, 0, 10, 10}, 5, 5, 5, 5, true},
		{"one cell over right", ViewRect{0, 0, 10, 10}, 6, 5, 5, 5, false},
		{"left of frame", ViewRect{0, 0, 10, 10}, -1, 0, 1, 1, false},
		{"above frame", ViewRect{0, 0, 10, 10}, 0, -1, 1, 1, false},
		{"flush bottom edge", ViewRect{0, 0, 10, 10}, 0, 9, 10, 1, true},
		{"one cell over bottom", ViewRect{0, 0, 10, 10}, 0, 10, 10, 1, false},
		{"negative origin frame", ViewRect{-5, -5, 10, 10}, -5, -5, 1, 1, true},
		{"empty rect at origin", ViewRect{0, 0, 0, 0}, 0, 0, 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.r.Contains(c.x, c.y, c.w, c.h)
			if got != c.want {
				t.Errorf("Contains(%d,%d,%d,%d) = %v, want %v", c.x, c.y, c.w, c.h, got, c.want)
			}
		})
	}
}
