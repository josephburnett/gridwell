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
