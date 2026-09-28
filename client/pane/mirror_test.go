package pane

import (
	"reflect"
	"testing"
)

func TestMirrored(t *testing.T) {
	cases := []struct {
		name  string
		live  []Holder
		shown []Face
		want  []string
	}{
		{"nothing live", nil, []Face{{"p1", "u/7"}}, nil},
		{"one live pane, shown nowhere else", []Holder{{"p1", "u/7"}},
			[]Face{{"p1", "u/7"}}, nil},
		{"its own descent is not a mirror", []Holder{{"p1", "u/7"}},
			[]Face{{"p1", "u/7"}, {"p2", "u/9"}}, nil},
		{"another pane's grid holds the tile", []Holder{{"p1", "u/7"}},
			[]Face{{"p1", "u/7"}, {"p2", "u/7"}, {"p2", "u/9"}}, []string{"p1"}},
		{"another pane descended into it, frozen after the takeover", []Holder{{"p2", "u/7"}},
			[]Face{{"p1", "u/7"}, {"p2", "u/7"}}, []string{"p2"}},
		{"a link elsewhere names its target", []Holder{{"p1", "u/7"}},
			[]Face{{"p2", "u/7"}}, []string{"p1"}},
		{"a parked outer level shown by the level in front", []Holder{{"w1:p1", "u/7"}},
			[]Face{{"w2:p1", "u/7"}}, []string{"w1:p1"}},
		{"only the shown ones, sorted, once", []Holder{{"p3", "s/1"}, {"p1", "u/7"}, {"p2", "u/8"}},
			[]Face{{"p9", "u/7"}, {"p8", "u/7"}, {"p9", "s/1"}}, []string{"p1", "p3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Mirrored(c.live, c.shown); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Mirrored = %v, want %v", got, c.want)
			}
		})
	}
}
