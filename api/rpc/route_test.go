package rpc

import (
	"strings"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

func TestDecideTakesTheFirstRouteThatTakesTheAddress(t *testing.T) {
	declared := map[string]Declared{
		"labcode": {Presents: []string{"https://gitlab.com/x/-/merge_requests/*"}},
		"pages":   {Presents: []string{"http://example.test/*"}},
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
		if got := Decide(c.address, routes, declared); got != c.want {
			t.Errorf("%s: Decide(%q) = %+v, want %+v", c.name, c.address, got, c.want)
		}
	}
	if got := Decide("https://gitlab.com/x", nil, declared); got != (Verdict{}) {
		t.Errorf("no routes: Decide = %+v, want a plain url tile", got)
	}
}

// A route to a plugin whose declaration is unknown takes every address its
// glob matches, broken with the reason: what the plugin would present is
// unknown, so neither the next rule nor the site stands in for it. A plugin
// that answered and does not present the address still falls through.
func TestDecideReadsAnUnknownDeclarationAsBroken(t *testing.T) {
	routes := []*pb.Route{
		{Match: "https://gitlab.com/*", Plugin: "labcode"},
		{Match: "http://example.test/*", Plugin: "pages"},
		{Match: "https://*", Outside: true},
		{Match: "http://*", Plugin: "pages"},
	}
	declared := map[string]Declared{
		"labcode": {Unknown: "plugin not responding: dial"},
		"pages":   {Presents: []string{"http://example.test/a"}},
	}
	cases := []struct {
		name, address string
		want          Verdict
	}{
		{"a route to a plugin that does not answer is broken", "https://gitlab.com/x/-/issues/7", Verdict{Plugin: "labcode", Broken: "plugin not responding: dial"}},
		{"its broken verdict comes before a later outside rule", "https://gitlab.com/x", Verdict{Plugin: "labcode", Broken: "plugin not responding: dial"}},
		{"an answering plugin presents what it declares", "http://example.test/a", Verdict{Plugin: "pages"}},
		{"and what it does not declare falls through", "http://example.test/b", Verdict{}},
		{"a route its glob does not match is never asked", "https://elsewhere.test/a", Verdict{Outside: true}},
	}
	for _, c := range cases {
		if got := Decide(c.address, routes, declared); got != c.want {
			t.Errorf("%s: Decide(%q) = %+v, want %+v", c.name, c.address, got, c.want)
		}
	}
}

// A plugin that does not answer now declares nothing whatever it last
// declared, and an Info refusal is unknown; a row with neither declares
// what it presents.
func TestDeclaredByReadsWhetherThePluginAnswersBeforeTheDeclaration(t *testing.T) {
	cases := []struct {
		name string
		row  *pb.PluginInfo
		want Declared
	}{
		{"answered", &pb.PluginInfo{Presents: []string{"a"}}, Declared{Presents: []string{"a"}}},
		{"unanswered, a declaration held", &pb.PluginInfo{Presents: []string{"a"}, Unanswered: "plugin not responding: context deadline exceeded"}, Declared{Presents: []string{"a"}, Unknown: "plugin not responding: context deadline exceeded"}},
		{"refused", &pb.PluginInfo{InfoError: "no root"}, Declared{Unknown: "no root"}},
	}
	for _, c := range cases {
		if got := DeclaredBy(c.row); got.Unknown != c.want.Unknown || len(got.Presents) != len(c.want.Presents) {
			t.Errorf("%s: DeclaredBy = %+v, want %+v", c.name, got, c.want)
		}
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
		{Uuid: "home0001"}, {Uuid: "lab0001", Presents: []string{"https://gitlab.com/*/-/merge_requests/*"}},
		{Uuid: "off0001", Unanswered: "off is disabled until the node restarts"}}}
	gotRoutes, declared := Routing(h)
	if len(gotRoutes) != 2 || gotRoutes[0] != routes[0] || gotRoutes[1] != routes[1] {
		t.Fatalf("routes = %v, want %v in order", gotRoutes, routes)
	}
	if len(declared) != 2 || len(declared["lab0001"].Presents) != 1 || declared["off0001"].Unknown == "" {
		t.Fatalf("declared = %v, want lab0001's presents and off0001 unknown", declared)
	}
	if v := Decide("https://gitlab.com/g/p/-/merge_requests/7", gotRoutes, declared); v.Plugin != "lab0001" {
		t.Errorf("a merge request: %+v, want lab0001 before the outside rule", v)
	}
	if v := Decide("https://gitlab.com/g/p/-/issues/7", gotRoutes, declared); !v.Outside {
		t.Errorf("an issue: %+v, want outside", v)
	}
}
