package events

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
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
		{"a changed grid clears its latch and refetches",
			&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "n/1"}}},
			Plan{ClearLatch: "n/1", Fetch: "n/1"}},
		{"a changed tile clears the failure latch on its own body",
			&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: &pb.Tile{Id: "n/4", GridId: "n/1"}}}},
			Plan{ClearContent: "n/4"}},
		{"a changed link clears the latch on the body it now shows",
			&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: &pb.Tile{Id: "n/4", GridId: "n/1", LinkTargetId: "p/~Zm9v"}}}},
			Plan{ClearContent: "p/~Zm9v"}},
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
	h := &pb.EventPluginHealth{PluginUuid: "n/c", Healthy: false, Detail: "gone"}
	if got := Route(&pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: h}}); got.Health != h {
		t.Errorf("health rides the plan by value: %+v", got)
	}
}

// Both directions resync exactly the source named, and the notice key is one
// errsurface.Sticky recognizes, so a down notice stays up until the recovery
// takes it down rather than expiring while still true.
func TestReactHealthBothDirectionsResync(t *testing.T) {
	down := ReactHealth(&pb.EventPluginHealth{PluginUuid: "n/c", Healthy: false, Detail: "gone"})
	if !down.Report || down.Resolve || down.Resync != "n/c" || down.Source != "plugin:n/c" {
		t.Errorf("down: %+v", down)
	}
	up := ReactHealth(&pb.EventPluginHealth{PluginUuid: "n/c", Healthy: true})
	if !up.Resolve || up.Report || up.Resync != "n/c" || up.Source != down.Source {
		t.Errorf("up: %+v", up)
	}
	if !errsurface.Sticky(down.Source) {
		t.Errorf("%q must be a sticky source", down.Source)
	}
}
