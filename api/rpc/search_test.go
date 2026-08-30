package rpc

import "testing"

// The one selector grammar: id: is exact, everything else is
// plugin-interpreted text. New selectors extend the parser, never a
// plugin.
func TestParseSearchQuery(t *testing.T) {
	cases := []struct {
		in       string
		id, text string
	}{
		{"id:aabb/7", "aabb/7", ""},
		{"  id: aabb/7 ", "aabb/7", ""},
		{"gopher meeting", "", "gopher meeting"},
		{"identity crisis", "", "identity crisis"}, // no accidental prefix match
		{"", "", ""},
	}
	for _, c := range cases {
		got := ParseSearchQuery(c.in)
		if got.ID != c.id || got.Text != c.text {
			t.Errorf("ParseSearchQuery(%q) = %+v, want id=%q text=%q", c.in, got, c.id, c.text)
		}
	}
}
