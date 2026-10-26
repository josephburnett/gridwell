package urlview

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func presenterOf(site string) string {
	if site == "http://example.test/b" {
		return "pre0001"
	}
	return ""
}

// A landing keeps the view's taker in step with where its page is: the site
// outside the page, and the routes' presenter for a page it came back to.
func TestViewLand(t *testing.T) {
	const stored = "http://example.test/a"
	in := View{Page: page, Site: stored, Shows: "pre0001", Moving: "http://plain.test/x"}
	v, site, ok := in.Land(stored, page+"example.test/b", presenterOf)
	if !ok || site != "http://example.test/b" || v != (View{Page: page, Site: site, Shows: "pre0001"}) {
		t.Errorf("an in-page landing = %+v %q %v", v, site, ok)
	}
	v, site, ok = v.Land(stored, "http://plain.test/x", presenterOf)
	if !ok || v != (View{Page: page, Site: "http://plain.test/x"}) || site != "http://plain.test/x" {
		t.Errorf("a landing on the site = %+v %q %v, want it showing the site", v, site, ok)
	}
	v, _, _ = v.Land(stored, page+"example.test/b", presenterOf)
	if v.Shows != "pre0001" {
		t.Errorf("back into the page shows %q, want the routes' presenter", v.Shows)
	}
	if _, _, ok := v.Land(stored, page+"_/app.js", presenterOf); ok {
		t.Error("the plugin's assets landed a site address")
	}
}

// A view follows its row when the row names where a routed navigation sends
// it, or names where it is with another taker; any other row leaves it be.
func TestViewFollow(t *testing.T) {
	presented := &pb.Tile{Kind: rpc.KindURL, UrlString: "http://example.test/b", PresentedBy: "pre0001"}
	plain := &pb.Tile{Kind: rpc.KindURL, UrlString: "http://plain.test/x"}
	cases := []struct {
		name string
		v    View
		row  *pb.Tile
		load string
	}{
		{"a move to a presented address loads its page", View{Page: page, Site: "http://plain.test/a", Moving: "http://example.test/b"}, presented, page + "example.test/b"},
		{"a move to the site loads the site", View{Page: page, Site: "http://example.test/a", Shows: "pre0001", Moving: "http://plain.test/x"}, plain, "http://plain.test/x"},
		{"a row older than the move waits", View{Page: page, Site: "http://example.test/a", Shows: "pre0001", Moving: "http://plain.test/x"}, presented, ""},
		{"the row's taker changed under the view", View{Page: page, Site: "http://example.test/b"}, presented, page + "example.test/b"},
		{"the row agrees with the view", View{Page: page, Site: "http://example.test/b", Shows: "pre0001"}, presented, ""},
		{"a row behind where the view is", View{Page: page, Site: "http://plain.test/y"}, plain, ""},
		{"a served page follows nothing", View{Site: "http://example.test/b"}, presented, ""},
		{"a served row follows nothing", View{Page: page, Moving: page}, &pb.Tile{Kind: rpc.KindURL, ServesPage: true}, ""},
	}
	// A row that reads broken where the view is, or where it is moving, loads
	// nothing: the view closes, and the site never stands in.
	broken := &pb.Tile{Kind: rpc.KindURL, UrlString: "http://example.test/b", PresentedBy: "pre0001", BrokenReason: "plugin not responding"}
	for _, v := range []View{
		{Page: page, Site: "http://example.test/b", Shows: "pre0001"},
		{Page: page, Site: "http://plain.test/a", Moving: "http://example.test/b"},
	} {
		if after, load, ok := v.Follow(broken); !ok || load != "" || after != (View{Page: page, Site: broken.UrlString, Shows: "pre0001"}) {
			t.Errorf("%+v following a broken row: %+v loads %q %v, want closed", v, after, load, ok)
		}
	}
	if _, _, ok := (View{Page: page, Site: "http://plain.test/a", Moving: "http://plain.test/x"}).Follow(broken); ok {
		t.Error("a broken row older than the move closed the view")
	}
	for _, c := range cases {
		after, load, ok := c.v.Follow(c.row)
		if ok != (c.load != "") || load != c.load {
			t.Errorf("%s: Follow loads %q %v, want %q", c.name, load, ok, c.load)
			continue
		}
		if ok && after != (View{Page: page, Site: c.row.UrlString, Shows: c.row.PresentedBy}) {
			t.Errorf("%s: after the follow the view is %+v", c.name, after)
		}
	}
}

// A url tile that reads broken opens nothing and says why, named as the tile
// is; any other opens.
func TestOpensRefusesABrokenTile(t *testing.T) {
	if _, ok := Opens(&pb.Tile{Kind: rpc.KindURL, UrlString: "http://example.test/a", PresentedBy: "pre0001"}); !ok {
		t.Error("a presented tile does not open")
	}
	notice, ok := Opens(&pb.Tile{Kind: rpc.KindURL, AltText: "Hello", UrlString: "http://example.test/a", PresentedBy: "pre0001", BrokenReason: "site is disabled until the node restarts"})
	if ok || notice != "Hello: site is disabled until the node restarts" {
		t.Errorf("a broken tile: Opens = %q %v, want refused with its name and the reason", notice, ok)
	}
	if notice, _ := Opens(&pb.Tile{Kind: rpc.KindURL, UrlString: "http://example.test/a", BrokenReason: "down"}); notice != "http://example.test/a: down" {
		t.Errorf("an unnamed broken tile says %q, want its address", notice)
	}
}
