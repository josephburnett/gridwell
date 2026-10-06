package urlview

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func TestPageMoved(t *testing.T) {
	page := func(key int64, frozen bool) *gridwellv1.Tile {
		return &gridwellv1.Tile{Kind: rpc.KindURL, ServesPage: true, PreviewBlobId: key, UrlFrozen: frozen}
	}
	for _, c := range []struct {
		name    string
		t       *gridwellv1.Tile
		changed bool
		want    Moved
	}{
		{"a served page whose screenshot the node retired", page(0, false), true, Moved{Reload: true, DropCapture: true}},
		{"one whose face is now its plugin's picture", page(-5, false), true, Moved{Reload: true, DropCapture: true}},
		{"a frozen one keeps the user's screenshot", page(7, true), true, Moved{}},
		{"a frozen one with no screenshot yet", page(0, true), true, Moved{}},
		{"a capture or framing write on the row", page(7, false), false, Moved{}},
		{"a page on the web", &gridwellv1.Tile{Kind: rpc.KindURL, UrlString: "https://example.com/"}, true, Moved{}},
		{"a text body", &gridwellv1.Tile{Kind: rpc.KindText}, true, Moved{}},
	} {
		if got := PageMoved(c.t, c.changed); got != c.want {
			t.Errorf("%s: PageMoved = %+v, want %+v", c.name, got, c.want)
		}
	}
}
