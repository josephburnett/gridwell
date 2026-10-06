package events

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
)

func TestRouteTable(t *testing.T) {
	cases := []struct {
		name string
		ev   *pb.Event
		want Plan
	}{
		{"a removed tile drops its previews",
			&pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{GridId: "n/1", TileId: "n/4"}}},
			Plan{DropPreviews: "n/4"}},
		{"a changed grid clears its latch, refetches, and revives its namespace's dead keys",
			&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "n/1"}}},
			Plan{ClearLatch: "n/1", Fetch: "n/1", Revive: "n"}},
		{"a changed grid behind a connection revives only that chain",
			&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "me/toa/p/~Zm9v"}}},
			Plan{ClearLatch: "me/toa/p/~Zm9v", Fetch: "me/toa/p/~Zm9v", Revive: "me/toa/p"}},
		{"a changed tile clears the failure latch on its own body",
			&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: &pb.Tile{Id: "n/4", GridId: "n/1"}}}},
			Plan{ClearContent: "n/4"}},
		{"a changed link clears the latch on the body it now shows",
			&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: &pb.Tile{Id: "n/4", GridId: "n/1", LinkTargetId: "p/~Zm9v"}}}},
			Plan{ClearContent: "p/~Zm9v"}},
		{"a served page that moved reloads its live views and drops the capture the node retired",
			&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{ContentChanged: true,
				Tile: &pb.Tile{Id: "p/~dA", GridId: "p/~Yw", Kind: rpc.KindURL, ServesPage: true}}}},
			Plan{ClearContent: "p/~dA", Reload: "p/~dA", DropPreviews: "p/~dA"}},
		{"a frozen one keeps its face and has no live view",
			&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{ContentChanged: true,
				Tile: &pb.Tile{Id: "p/~dA", GridId: "p/~Yw", Kind: rpc.KindURL, ServesPage: true, PreviewBlobId: 7, UrlFrozen: true}}}},
			Plan{ClearContent: "p/~dA"}},
		{"a changed event with no row asks for nothing",
			&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{}}},
			Plan{}},
		{"an empty event asks for nothing", &pb.Event{}, Plan{}},
	}
	for _, c := range cases {
		if got := Route(c.ev); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	// A root's framing is the whole fact: applied where it lands, it clears
	// no latch and fetches nothing, since the listing did not change.
	f := &pb.GridFramingChanged{GridId: "n/1", ViewCx: 1, ViewCy: 2, ViewZoom: 3}
	if got := Route(&pb.Event{Payload: &pb.Event_GridFramingChanged{GridFramingChanged: f}}); got != (Plan{Reframe: f}) {
		t.Errorf("a framed grid: got %+v, want only the framing to apply", got)
	}
	h := &pb.EventPluginHealth{PluginUuid: "n/c", Healthy: false, Detail: "gone"}
	if got := Route(&pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: h}}); got.Health != h {
		t.Errorf("health rides the plan by value: %+v", got)
	}
}

// A health event carries two facts with two notices: the source's darkness and
// whether it can tell the node of its changes (live_updates_off). Each notice
// follows its own field, so they come and go independently, and only a move of
// the healthy bit resyncs: a source that lists but cannot watch still answers.
func TestReactHealthTable(t *testing.T) {
	const (
		dark = "plugin:n/c"
		live = "live:n/c"
	)
	cases := []struct {
		name    string
		wasDark bool
		h       *pb.EventPluginHealth
		want    HealthReaction
	}{
		{"going dark says so and resyncs", false,
			&pb.EventPluginHealth{PluginUuid: "n/c", Detail: "gone"},
			HealthReaction{
				Dark:    StickyNotice{Source: dark, Message: "fs: live updates stopped — gone"},
				LiveOff: StickyNotice{Source: live},
				Resync:  "n/c"}},
		{"coming back takes the notice down and resyncs", true,
			&pb.EventPluginHealth{PluginUuid: "n/c", Healthy: true},
			HealthReaction{Dark: StickyNotice{Source: dark}, LiveOff: StickyNotice{Source: live}, Resync: "n/c"}},
		{"a refused watch on a light source is its own notice and no resync", false,
			&pb.EventPluginHealth{PluginUuid: "n/c", Healthy: true, LiveUpdatesOff: "too many watches"},
			HealthReaction{
				Dark:    StickyNotice{Source: dark},
				LiveOff: StickyNotice{Source: live, Message: "fs: live updates off — too many watches"}}},
		{"the watch opening takes that notice down and resyncs nothing", false,
			&pb.EventPluginHealth{PluginUuid: "n/c", Healthy: true},
			HealthReaction{Dark: StickyNotice{Source: dark}, LiveOff: StickyNotice{Source: live}}},
		{"coming back with the watch still refused keeps that notice and resyncs", true,
			&pb.EventPluginHealth{PluginUuid: "n/c", Healthy: true, LiveUpdatesOff: "too many watches"},
			HealthReaction{
				Dark:    StickyNotice{Source: dark},
				LiveOff: StickyNotice{Source: live, Message: "fs: live updates off — too many watches"},
				Resync:  "n/c"}},
		{"a dark source with its watch refused shows both", false,
			&pb.EventPluginHealth{PluginUuid: "n/c", Detail: "gone", LiveUpdatesOff: "too many watches"},
			HealthReaction{
				Dark:    StickyNotice{Source: dark, Message: "fs: live updates stopped — gone"},
				LiveOff: StickyNotice{Source: live, Message: "fs: live updates off — too many watches"},
				Resync:  "n/c"}},
		{"a down repeated while dark is not a move", true,
			&pb.EventPluginHealth{PluginUuid: "n/c", Detail: "still gone"},
			HealthReaction{
				Dark:    StickyNotice{Source: dark, Message: "fs: live updates stopped — still gone"},
				LiveOff: StickyNotice{Source: live}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReactHealth(c.h, "fs", c.wasDark); got != c.want {
				t.Errorf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}
	for _, src := range []string{dark, live} {
		if !errsurface.Sticky(src) {
			t.Errorf("%q must be sticky: the condition holds until an event says otherwise", src)
		}
	}
}

// A tile event names the grid whose read in flight may predate it, folded or
// not: an answer taken before the event would install over the fold.
func TestOweNamesTheGridOfATileEvent(t *testing.T) {
	cases := []struct {
		name string
		ev   *pb.Event
		want string
	}{
		{"a TileChanged owes its grid",
			&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: &pb.Tile{Id: "n/4", GridId: "n/1"}}}}, "n/1"},
		{"a TileRemoved owes its grid",
			&pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{GridId: "n/1", TileId: "n/4"}}}, "n/1"},
		{"a GridChanged owes nothing here; Route fetches it",
			&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "n/1"}}}, ""},
	}
	for _, c := range cases {
		if got := Owe(c.ev); got != c.want {
			t.Errorf("%s: Owe = %q, want %q", c.name, got, c.want)
		}
	}
}
