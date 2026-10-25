package urlview

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

const page = "https://host/content/tok/p1/"

// The page arm before the row's own address is the whole point: a served page
// is a url tile whose url_string is empty, so the obvious order opens nothing.
func TestAddress(t *testing.T) {
	cases := []struct {
		name string
		tile *pb.Tile
		want string
	}{
		{"a served page opens at the content API", &pb.Tile{Kind: rpc.KindURL, ServesPage: true}, page},
		{"a url tile opens at its own address", &pb.Tile{Kind: rpc.KindURL, UrlString: "https://a.test/"}, "https://a.test/"},
		{"a presented tile opens at its plugin's page for its address",
			&pb.Tile{Kind: rpc.KindURL, UrlString: "http://example.test/a?x=1", PresentedBy: "pre0001"}, page + "example.test/a?x=1"},
		{"an address-less url tile opens nothing", &pb.Tile{Kind: rpc.KindURL}, ""},
	}
	for _, c := range cases {
		if got := Address(c.tile, page); got != c.want {
			t.Errorf("%s: Address = %q, want %q", c.name, got, c.want)
		}
	}
}

// A presented tile's view lands on its page, and the address it names is the
// site's; anywhere outside the page names none, so nothing is written.
func TestLanded(t *testing.T) {
	const stored = "http://example.test/a"
	cases := []struct {
		name, presented, location string
		want                      string
		ok                        bool
	}{
		{"a plain url lands where it is", "", "https://b.test/p", "https://b.test/p", true},
		{"a presented page names its site address", page, page + "example.test/b?y=2", "http://example.test/b?y=2", true},
		{"another tile's page names nothing", page, "https://host/content/tok/p2/example.test/b", "", false},
		{"the plugin's assets name nothing", page, page + "_/app.js", "", false},
		{"another origin names nothing", page, "https://example.test/b", "", false},
	}
	for _, c := range cases {
		got, ok := Landed(c.presented, stored, c.location)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Landed = %q %v, want %q %v", c.name, got, ok, c.want, c.ok)
		}
		if ok && c.presented != "" && !WriteAddress(false, got, stored) {
			t.Errorf("%s: the site address %q is not written", c.name, got)
		}
	}
}
