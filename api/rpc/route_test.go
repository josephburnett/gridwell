package rpc

import (
	"strings"
	"testing"
)

// prefixGlob stands in for the presents matcher: a trailing * matches any
// rest, anything else matches only itself.
func prefixGlob(pattern, address string) bool {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(address, p)
	}
	return pattern == address
}

func TestDecideTakesTheFirstRouteThatTakesTheAddress(t *testing.T) {
	presents := map[string][]string{
		"labcode": {"https://gitlab.com/x/-/merge_requests/*"},
		"pages":   {"http://example.test/*"},
	}
	routes := []Route{
		{Match: "https://gitlab.com/*", Plugin: "labcode"},
		{Match: "https://gitlab.com/*", Outside: true},
		{Match: "http://example.test/*", Plugin: "pages"},
		{Match: "http://example.test/*", Plugin: "labcode"},
		{Match: "https://elsewhere.test/*", Plugin: "undeclared"},
		{Match: "https://*", Outside: true},
	}
	cases := []struct {
		name, address string
		want          Verdict
	}{
		{"a plugin route over an address its plugin presents", "https://gitlab.com/x/-/merge_requests/7", Verdict{Plugin: "labcode"}},
		{"a plugin route its plugin does not present falls through", "https://gitlab.com/x/-/issues/7", Verdict{Outside: true}},
		{"the first plugin route that presents wins", "http://example.test/a", Verdict{Plugin: "pages"}},
		{"a route to a plugin that declares nothing falls through", "https://elsewhere.test/a", Verdict{Outside: true}},
		{"no route matches: a plain url tile", "http://plain.test/a", Verdict{}},
		{"the glob is over the whole address", "ftp://gitlab.com/x", Verdict{}},
	}
	for _, c := range cases {
		if got := Decide(c.address, routes, presents, prefixGlob); got != c.want {
			t.Errorf("%s: Decide(%q) = %+v, want %+v", c.name, c.address, got, c.want)
		}
	}
	if got := Decide("https://gitlab.com/x", nil, presents, prefixGlob); got != (Verdict{}) {
		t.Errorf("no routes: Decide = %+v, want a plain url tile", got)
	}
}

func TestCheckRefusesARouteWithBothOrNeither(t *testing.T) {
	cases := []struct {
		r      Route
		refuse string
	}{
		{Route{Match: "https://*", Plugin: "p"}, ""},
		{Route{Match: "https://*", Outside: true}, ""},
		{Route{Match: "https://*", Plugin: "p", Outside: true}, "not both"},
		{Route{Match: "https://*"}, "with neither"},
		{Route{Plugin: "p"}, "match is required"},
	}
	for _, c := range cases {
		err := c.r.Check()
		if c.refuse == "" && err != nil {
			t.Errorf("%+v: refused %v", c.r, err)
		}
		if c.refuse != "" && (err == nil || !strings.Contains(err.Error(), c.refuse)) {
			t.Errorf("%+v: got %v, want a refusal saying %q", c.r, err, c.refuse)
		}
	}
}
