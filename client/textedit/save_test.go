package textedit

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

func v(n int64) rpc.ContentBasis { return rpc.ContentBasis{Version: n} }

func TestSaveClaim(t *testing.T) {
	cases := []struct {
		name           string
		rowOwnsContent bool
		row            rpc.ContentBasis
		basis          rpc.ContentBasis
		haveBasis      bool
		want           rpc.ContentBasis
	}{
		{"owner row, basis", true, v(7), v(5), true, v(5)},
		{"link row, basis", false, v(7), v(5), true, v(5)},
		// With no basis the row snapshot stands in, and only when that row
		// owns the bytes.
		{"owner row, no basis", true, v(7), v(0), false, v(7)},
		{"link row, no basis", false, v(7), v(0), false, v(0)},
		{"no row, no basis", false, v(0), v(0), false, v(0)},
		// A source body claims the stamp it was read under, never the row's.
		{"source row, basis", true, rpc.ContentBasis{Stamp: "row"}, rpc.ContentBasis{Stamp: "read"}, true,
			rpc.ContentBasis{Stamp: "read"}},
	}
	for _, c := range cases {
		if got := SaveClaim(c.rowOwnsContent, c.row, c.basis, c.haveBasis); got != c.want {
			t.Errorf("%s: SaveClaim = %+v, want %+v", c.name, got, c.want)
		}
	}
}
