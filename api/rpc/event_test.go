package rpc

import (
	"slices"
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
		name: "tile_changed with its bytes, apart from a framing change",
		ev: &pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{
			Tile: &pb.Tile{Id: "t1", GridId: "g1"}, ContentChanged: true}}},
		want: "c/t1",
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

// A select adds, then removes, so a tile named in both ends out; the set it
// leaves is sorted with each id once, and the input is not changed.
func TestApplySelect(t *testing.T) {
	set := []string{"n/4", "n/2"}
	for _, c := range []struct {
		name        string
		add, remove []string
		want        []string
	}{
		{"add one", []string{"n/5"}, nil, []string{"n/2", "n/4", "n/5"}},
		{"add one held", []string{"n/4"}, nil, []string{"n/2", "n/4"}},
		{"remove one", nil, []string{"n/2"}, []string{"n/4"}},
		{"remove one not held", nil, []string{"n/9"}, []string{"n/2", "n/4"}},
		{"in both ends out", []string{"n/5"}, []string{"n/5", "n/4"}, []string{"n/2"}},
		{"clear", nil, []string{"n/2", "n/4"}, nil},
	} {
		if got := ApplySelect(set, c.add, c.remove); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if !slices.Equal(set, []string{"n/4", "n/2"}) {
		t.Errorf("the input changed: %v", set)
	}
}

// A select names its grid unless it is the all-clear, and the all-clear names
// nothing else; recurse is only for a clear.
func TestSelectRefusal(t *testing.T) {
	for _, c := range []struct {
		name string
		req  *pb.SetSelectionRequest
		bad  bool
	}{
		{"add", &pb.SetSelectionRequest{GridId: "n/1", Add: []string{"n/2"}}, false},
		{"clear recurse", &pb.SetSelectionRequest{GridId: "n/1", Clear: true, Recurse: true}, false},
		{"recurse alone", &pb.SetSelectionRequest{GridId: "n/1", Recurse: true}, true},
		{"no grid", &pb.SetSelectionRequest{Clear: true}, true},
		{"all", &pb.SetSelectionRequest{Clear: true, All: true}, false},
		{"all without clear", &pb.SetSelectionRequest{All: true}, true},
		{"all with a grid", &pb.SetSelectionRequest{GridId: "n/1", Clear: true, All: true}, true},
		{"all with add", &pb.SetSelectionRequest{Clear: true, All: true, Add: []string{"n/2"}}, true},
		{"all with remove", &pb.SetSelectionRequest{Clear: true, All: true, Remove: []string{"n/2"}}, true},
		{"all with recurse", &pb.SetSelectionRequest{Clear: true, All: true, Recurse: true}, true},
	} {
		if got := SelectRefusal(c.req); (got != "") != c.bad {
			t.Errorf("%s: SelectRefusal = %q, want refused %v", c.name, got, c.bad)
		}
	}
}
