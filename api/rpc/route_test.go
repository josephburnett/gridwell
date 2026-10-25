package rpc

import (
	"strings"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
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
	routes := []*pb.Route{
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
		r      *pb.Route
		refuse string
	}{
		{&pb.Route{Match: "https://*", Plugin: "p"}, ""},
		{&pb.Route{Match: "https://*", Outside: true}, ""},
		{&pb.Route{Match: "https://*", Plugin: "p", Outside: true}, "not both"},
		{&pb.Route{Match: "https://*"}, "with neither"},
		{&pb.Route{Plugin: "p"}, "match is required"},
	}
	for _, c := range cases {
		err := CheckRoute(c.r)
		if c.refuse == "" && err != nil {
			t.Errorf("%+v: refused %v", c.r, err)
		}
		if c.refuse != "" && (err == nil || !strings.Contains(err.Error(), c.refuse)) {
			t.Errorf("%+v: got %v, want a refusal saying %q", c.r, err, c.refuse)
		}
	}
}

// The handshake carries the routes in order and each plugin's presents, so
// the client's verdict is the node's.
func TestRoutingReadsTheHandshake(t *testing.T) {
	routes := []*pb.Route{{Match: "https://gitlab.com/*/-/merge_requests/*", Plugin: "lab0001"}, {Match: "https://gitlab.com/*", Outside: true}}
	h := &pb.HandshakeResponse{Routes: routes, Plugins: []*pb.PluginInfo{
		{Uuid: "home0001"}, {Uuid: "lab0001", Presents: []string{"https://gitlab.com/*/-/merge_requests/*"}}}}
	gotRoutes, presents := Routing(h)
	if len(gotRoutes) != 2 || gotRoutes[0] != routes[0] || gotRoutes[1] != routes[1] {
		t.Fatalf("routes = %v, want %v in order", gotRoutes, routes)
	}
	if len(presents) != 1 || len(presents["lab0001"]) != 1 {
		t.Fatalf("presents = %v, want lab0001's alone", presents)
	}
	if v := Decide("https://gitlab.com/g/p/-/merge_requests/7", gotRoutes, presents, MatchAddress); v.Plugin != "lab0001" {
		t.Errorf("a merge request: %+v, want lab0001 before the outside rule", v)
	}
	if v := Decide("https://gitlab.com/g/p/-/issues/7", gotRoutes, presents, MatchAddress); !v.Outside {
		t.Errorf("an issue: %+v, want outside", v)
	}
}
