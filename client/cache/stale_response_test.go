package cache

import (
	"testing"

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
	c.UpdateTile("1", row(2, oldY))

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
