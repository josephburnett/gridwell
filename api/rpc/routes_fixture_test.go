package rpc

import (
	"encoding/json"
	"os"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// routesFixture is testdata/routes.json, which the desktop's copy of these
// rules (apps/desktop/src/main/routes.ts) runs too.
type routesFixture struct {
	Match []struct {
		Pattern, Address, Why string
		Want                  bool
	}
	Decide struct {
		Routes []struct {
			Match, Plugin string
			Outside       bool
		}
		Presents map[string][]string
		Cases    []struct {
			Address, Plugin, Why string
			Outside              bool
		}
	}
	Site []struct {
		Page, Site, Location, Want, Why string
		Ok                              bool
	}
}

func readRoutesFixture(t *testing.T) routesFixture {
	t.Helper()
	b, err := os.ReadFile("testdata/routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var f routesFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Match) == 0 || len(f.Decide.Cases) == 0 || len(f.Site) == 0 {
		t.Fatal("testdata/routes.json read no cases")
	}
	return f
}

func TestRoutesFixture(t *testing.T) {
	f := readRoutesFixture(t)
	for _, c := range f.Match {
		if got := MatchAddress(c.Pattern, c.Address); got != c.Want {
			t.Errorf("MatchAddress(%q, %q) = %v, want %v (%s)", c.Pattern, c.Address, got, c.Want, c.Why)
		}
	}
	var routes []*pb.Route
	for _, r := range f.Decide.Routes {
		routes = append(routes, &pb.Route{Match: r.Match, Plugin: r.Plugin, Outside: r.Outside})
	}
	for _, c := range f.Decide.Cases {
		want := Verdict{Plugin: c.Plugin, Outside: c.Outside}
		if got := Decide(c.Address, routes, f.Decide.Presents, MatchAddress); got != want {
			t.Errorf("%s: Decide(%q) = %+v, want %+v", c.Why, c.Address, got, want)
		}
	}
	for _, c := range f.Site {
		got, ok := PresentedSite(c.Page, c.Site, c.Location)
		if got != c.Want || ok != c.Ok {
			t.Errorf("%s: PresentedSite(%q, %q, %q) = %q %v, want %q %v", c.Why, c.Page, c.Site, c.Location, got, ok, c.Want, c.Ok)
		}
	}
}
