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
		{"an address-less url tile opens nothing", &pb.Tile{Kind: rpc.KindURL}, ""},
	}
	for _, c := range cases {
		if got := Address(c.tile, page); got != c.want {
			t.Errorf("%s: Address = %q, want %q", c.name, got, c.want)
		}
	}
}

// A view's location in the node's content API names no address, since its
// path holds the token, and any other names itself.
func TestLanded(t *testing.T) {
	const stored = "http://example.test/a"
	cases := []struct {
		name, location string
		want           string
		ok             bool
	}{
		{"a plain url lands where it is", "https://b.test/p", "https://b.test/p", true},
		{"its own page names nothing", page, "", false},
		{"another tile's page names nothing", "https://host/content/tok/p2", "", false},
		{"the node outside its content API is a site", "https://host/wasm_exec.js?x", "https://host/wasm_exec.js?x", true},
	}
	for _, c := range cases {
		got, ok := Landed(page, c.location)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Landed = %q %v, want %q %v", c.name, got, ok, c.want, c.ok)
		}
		if ok && !WriteAddress(false, got, stored) {
			t.Errorf("%s: the address %q is not written", c.name, got)
		}
	}
}
