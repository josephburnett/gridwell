package pluginhost

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// A screenshot yields to its plugin's picture when the page it pictures moved,
// except the one the user froze.
func TestPageFaceStale(t *testing.T) {
	page := &pluginv1.Entry{Key: "t", Kind: "url", ServesPage: true}
	web := &pluginv1.Entry{Key: "t", Kind: "url", UrlString: "https://example.com/"}
	link := &pluginv1.Entry{Key: "t", Kind: "url", ServesPage: true, LinkTarget: &pluginv1.EntryRef{Context: "c", Key: "t"}}
	for _, c := range []struct {
		name string
		e    *pluginv1.Entry
		row  *gridwellv1.Tile
		want bool
	}{
		{"a served page's screenshot", page, &gridwellv1.Tile{PreviewBlobId: 7}, true},
		{"a frozen one", page, &gridwellv1.Tile{PreviewBlobId: 7, UrlFrozen: true}, false},
		{"no screenshot", page, &gridwellv1.Tile{}, false},
		{"a page on the web, which nothing tells", web, &gridwellv1.Tile{PreviewBlobId: 7}, false},
		{"a link, whose face is its target's", link, &gridwellv1.Tile{}, false},
	} {
		if got := pageFaceStale(c.e, c.row); got != c.want {
			t.Errorf("%s: pageFaceStale = %v, want %v", c.name, got, c.want)
		}
	}
}
