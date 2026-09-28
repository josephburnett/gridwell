package rpc

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

func TestEventKey(t *testing.T) {
	cases := []struct {
		name string
		ev   *pb.Event
		want string
	}{{
		name: "grid_changed",
		ev:   &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "g1"}}},
		want: "g/g1",
	}, {
		name: "tile_changed",
		ev:   &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: &pb.Tile{Id: "t1", GridId: "g1"}}}},
		want: "t/t1",
	}, {
		name: "tile_removed",
		ev:   &pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{GridId: "g1", TileId: "t1"}}},
		want: "r/g1/t1",
	}, {
		name: "plugin_health",
		ev:   &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{PluginUuid: "u1"}}},
		want: "h/u1",
	}, {
		name: "grid_framing_changed",
		ev:   &pb.Event{Payload: &pb.Event_GridFramingChanged{GridFramingChanged: &pb.GridFramingChanged{GridId: "g1", ViewZoom: 2}}},
		want: "f/g1",
	}, {
		name: "no payload is unkeyable",
		ev:   &pb.Event{},
		want: "",
	}, {
		name: "nil event is unkeyable",
		ev:   nil,
		want: "",
	}}
	for _, c := range cases {
		if got := EventKey(c.ev); got != c.want {
			t.Errorf("%s: EventKey = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEventKeyKeysEveryPayloadArm fails when the Event oneof grows an arm
// EventKey does not key: an unkeyed arm never coalesces, so a stalled
// consumer's queue grows without bound in the entity it is about.
func TestEventKeyKeysEveryPayloadArm(t *testing.T) {
	m := (&pb.Event{}).ProtoReflect()
	arms := m.Descriptor().Oneofs().ByName("payload").Fields()
	seen := map[string]string{}
	for i := 0; i < arms.Len(); i++ {
		fd := arms.Get(i)
		ev := &pb.Event{}
		r := ev.ProtoReflect()
		r.Set(fd, r.NewField(fd))
		key := EventKey(ev)
		if key == "" {
			t.Errorf("payload arm %s is unkeyable", fd.Name())
			continue
		}
		if prev, dup := seen[key]; dup {
			t.Errorf("payload arms %s and %s share key %q", prev, fd.Name(), key)
		}
		seen[key] = string(fd.Name())
	}
}
