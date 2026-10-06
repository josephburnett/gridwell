package cache

import (
	"testing"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/textedit"
)

// Trace 2026-10-04 seq 966-986, tile 1552: a content save and a scroll
// settle race. The node writes WriteContent (v2, old framing) then
// SetTextView (v2, new framing); the client applies both echoes, then the
// WriteContent response, a snapshot from before the SetTextView, folds in at
// the same version and puts the old framing back. The next settle diffs the
// pane against that row and re-sends a framing the node already holds.
func TestWriteResponseDoesNotRevertALaterSameVersionFraming(t *testing.T) {
	row := func(v, y int64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "1552", GridId: "1", Kind: rpc.KindText, Version: v,
			TextY: y, TextW: 600, TextH: 400, TextMode: rpc.TextModeText}
	}
	echo := func(t *gridwellv1.Tile) *gridwellv1.Event {
		return &gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
			TileChanged: &gridwellv1.TileChanged{Tile: t}}}
	}
	const oldY, newY = 900, 0

	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{row(1, oldY)})
	// persistTextScroll's optimistic patch.
	g, _ := c.Grid("1")
	c.PatchTile(g.Tiles["1552"], func(t *gridwellv1.Tile) { t.TextY = newY })
	// The node's two echoes, in its write order.
	c.Apply(echo(row(2, oldY))) // WriteContent
	c.Apply(echo(row(2, newY))) // SetTextView
	// postWriteContent's response, read by the node before the SetTextView.
	c.PutWriteResponse("1", row(2, oldY), WroteBody)

	g, _ = c.Grid("1")
	got := g.Tiles["1552"]
	if got.TextY != newY {
		t.Errorf("cached TextY = %d after the WriteContent response, want %d: the node holds %d, "+
			"so the cache reverted a later same-version framing write", got.TextY, newY, newY)
	}
	pane := textedit.Framing{Y: newY, W: 600, H: 400, Mode: rpc.TextModeText}
	if textedit.Reframes(textedit.FramingOf(got), pane, false) {
		t.Errorf("the next settle re-sends SetTextView %+v, which the node already holds", pane)
	}
}

// A write's response contributes what the write set and, for a content
// write, the version it claimed; every other field stays as the cache has it.
func TestAWriteResponseContributesOnlyWhatItWrote(t *testing.T) {
	// The cached row: v3, scrolled to TextY 0 by an echo the response
	// predates.
	cached := func() *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "t", GridId: "g", Kind: rpc.KindURL, Version: 3,
			TextY: 0, BlobId: 7, AltText: "cached", UrlString: "https://cached", UrlFrozen: false}
	}
	// What the node answered: its row at version v, from before the scroll.
	resp := func(v int64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "t", GridId: "g", Kind: rpc.KindURL, Version: v,
			TextY: 900, BlobId: 8, AltText: "written", UrlString: "https://written", UrlFrozen: true}
	}
	cases := []struct {
		name string
		w    Wrote
		v    int64
		want func(*gridwellv1.Tile)
	}{
		{"body", WroteBody, 4, func(t *gridwellv1.Tile) { t.Version, t.BlobId, t.AltText = 4, 8, "written" }},
		{"body, same version", WroteBody, 3, func(t *gridwellv1.Tile) { t.BlobId, t.AltText = 8, "written" }},
		{"address", WroteAddress, 4, func(t *gridwellv1.Tile) { t.Version, t.UrlString = 4, "https://written" }},
		{"name", WroteName, 4, func(t *gridwellv1.Tile) { t.Version, t.AltText = 4, "written" }},
		{"content older than the cache", WroteBody, 2, func(*gridwellv1.Tile) {}},
		// Framing claims no version, so the answer's version is not taken
		// whichever way it points.
		{"freeze", WroteFrozen, 5, func(t *gridwellv1.Tile) { t.UrlFrozen = true }},
		{"freeze from an older row", WroteFrozen, 1, func(t *gridwellv1.Tile) { t.UrlFrozen = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			c.PutGrid(&gridwellv1.Grid{Id: "g"}, []*gridwellv1.Tile{cached()})
			c.PutWriteResponse("g", resp(tc.v), tc.w)
			want := cached()
			tc.want(want)
			g, _ := c.Grid("g")
			if got := g.Tiles["t"]; !proto.Equal(got, want) {
				t.Errorf("cached row\n got %v\nwant %v", got, want)
			}
		})
	}
	// A write through a link answers a row this cache does not hold: nothing
	// is planted.
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "g"}, nil)
	c.PutWriteResponse("g", resp(4), WroteBody)
	if g, _ := c.Grid("g"); len(g.Tiles) != 0 {
		t.Errorf("a response for an uncached row was inserted: %v", g.Tiles)
	}
}
