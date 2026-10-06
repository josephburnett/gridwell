package rpc

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The transit grid rule lives once, in TransitQualifyGrid, so a new
// id-bearing Grid field cannot reach one hop and miss the other. Ids gain
// one segment, the far node's stamped facts ride verbatim, and the input is
// never mutated.
func TestTransitQualifyGrid(t *testing.T) {
	in := &pb.Grid{
		Id:            "far/7",
		ScratchGridId: "far/9",
		NodeNs:        "farnode",
		Writable:      true,
		HostContent:   true,
		Glyph:         "folder",
		SourceLabel:   "/srv/docs",
		MenuEntries:   []*pb.MenuEntry{{Id: "search", GridId: "far/7"}},
	}
	out := TransitQualifyGrid("hop", in)
	if out.Id != "hop/far/7" || out.ScratchGridId != "hop/far/9" || out.NodeNs != "hop/farnode" {
		t.Fatalf("ids not prepended one segment: %+v", out)
	}
	if !out.Writable || !out.HostContent || out.Glyph != "folder" || out.SourceLabel != "/srv/docs" {
		t.Fatalf("stamped facts must ride verbatim: %+v", out)
	}
	if len(out.MenuEntries) != 1 || out.MenuEntries[0].GridId != "hop/far/7" {
		t.Fatalf("menu entry roots not prepended: %+v", out.MenuEntries)
	}
	if in.Id != "far/7" || in.MenuEntries[0].GridId != "far/7" {
		t.Fatalf("input mutated: %+v", in)
	}
	if TransitQualifyGrid("hop", nil) != nil {
		t.Fatal("nil grid must stay nil")
	}
	if got := TransitQualifyGrid("hop", &pb.Grid{Id: "far/7"}); got.ScratchGridId != "" {
		t.Fatalf("empty scratch id must stay empty, got %q", got.ScratchGridId)
	}
}

// A node built before the connections list was retired still answers with
// one. The transit hop is its one reader: every row it carries joins plugins
// in ConnectionRow's shape, qualified like the rows beside it, after them.
func TestTransitQualifyPluginListFoldsARetiredConnectionsList(t *testing.T) {
	out := TransitQualifyPluginList("hop", &pb.HandshakeResponse{
		Plugins: []*pb.PluginInfo{{Uuid: "n1", Kind: "home", RootGridId: "n1/1"}},
		Connections: []*pb.ConnectionInfo{
			{Uuid: "n1/rtb", Label: "rtb", RootGridId: "n1/rtb/r/1", RootViewCx: 2, RootViewCy: 3, RootViewZoom: 0.5},
			{Uuid: "n1/far", Label: "far", StatusDetail: "dial refused"},
		},
	})
	if len(out.Connections) != 0 {
		t.Fatalf("the retired list must not be forwarded, got %+v", out.Connections)
	}
	if len(out.Plugins) != 3 || out.Plugins[0].Uuid != "hop/n1" {
		t.Fatalf("plugins = %+v, want home then the two connection rows", out.Plugins)
	}
	var rows []*pb.PluginInfo
	for _, pl := range out.Plugins {
		if IsConnectionRow(pl) {
			rows = append(rows, pl)
		}
	}
	want := []*pb.PluginInfo{
		ConnectionRow("hop/n1/rtb", "rtb", "hop/n1/rtb/r/1", "", ViewOf(2, 3, 0.5)),
		ConnectionRow("hop/n1/far", "far", "", "dial refused", View{}),
	}
	if len(rows) != len(want) {
		t.Fatalf("connection rows = %+v, want %+v", rows, want)
	}
	for i := range want {
		if !proto.Equal(rows[i], want[i]) {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// isIDField is the wire's naming for a qualified id within a request or a
// Tile, plus shell_session, a tile id by another name. MenuEntry's "id" is not
// one, so the rule is not read wire-wide.
func isIDField(fd protoreflect.FieldDescriptor) bool {
	name := string(fd.Name())
	return fd.Kind() == protoreflect.StringKind &&
		(name == "id" || strings.HasSuffix(name, "_id") || name == "shell_session")
}

// shell_session is optional like a reference, empty meaning the tile's own id,
// but names a tile in the tile's own namespace: it qualifies only when set and
// peels at every hop, as Id does.
func TestShellSessionQualifiesOnlyWhenSet(t *testing.T) {
	unset := &pb.Tile{Id: "far/7", GridId: "far/1"}
	if got := TransitQualifyTiles("hop", []*pb.Tile{unset})[0].ShellSession; got != "" {
		t.Errorf("an unset session qualified to %q", got)
	}
	leafOwn := &pb.Tile{Id: "7", GridId: "1"}
	QualifyOwnIDs("n1", leafOwn)
	if leafOwn.ShellSession != "" || leafOwn.Id != "n1/7" || leafOwn.GridId != "n1/1" {
		t.Errorf("QualifyOwnIDs = %+v, want ids qualified and no session", leafOwn)
	}
	clone := &pb.Tile{Id: "8", GridId: "1", ShellSession: "7"}
	QualifyOwnIDs("n1", clone)
	if clone.ShellSession != "n1/7" {
		t.Errorf("a clone's session qualified to %q, want n1/7", clone.ShellSession)
	}
	leaf := InboundHop("n1/1", "n1", false)
	if got := leaf.PeelTile(clone).ShellSession; got != "7" {
		t.Errorf("a leaf hop peeled the session to %q, want 7", got)
	}
}

// Every id field a Tile declares is in tileIDFields, so the prepend and the
// peel walk the same set and invert each other: a new id field on Tile fails
// here until both directions carry it.
func TestTilePeelInvertsThePrependOnEveryIDField(t *testing.T) {
	fields := (&pb.Tile{}).ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !isIDField(fd) {
			continue
		}
		in := &pb.Tile{}
		in.ProtoReflect().Set(fd, protoreflect.ValueOfString("far/7"))
		out := TransitQualifyTiles("hop", []*pb.Tile{in})[0]
		if got := out.ProtoReflect().Get(fd).String(); got != "hop/far/7" {
			t.Errorf("prepend %s = %q, want hop/far/7", fd.Name(), got)
		}
		back := Hop{Seg: "hop", Via: "hop", Transit: true}.PeelTile(out)
		if got := back.ProtoReflect().Get(fd).String(); got != "far/7" {
			t.Errorf("peel %s = %q, want far/7", fd.Name(), got)
		}
	}
}

// A request carrying more than one id is PeelRequest's whole: every id field,
// a Tile's included, is peeled. A new id on any request fails here until the
// codec carries it, because a hop that peels only the routed id forwards the
// rest with its own segment still on.
func TestPeelRequestPeelsEveryIDOfEveryMultiIDRequest(t *testing.T) {
	hop := Hop{Seg: "hop", Via: "hop", Transit: true}
	msgs := pb.File_gridwell_v1_data_proto.Messages()
	checked := 0
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		if !strings.HasSuffix(string(md.Name()), "Request") {
			continue
		}
		mt, err := protoregistry.GlobalTypes.FindMessageByName(md.FullName())
		if err != nil {
			t.Fatal(err)
		}
		req := mt.New()
		var ids []func(protoreflect.Message) string
		fields := md.Fields()
		for j := 0; j < fields.Len(); j++ {
			fd := fields.Get(j)
			switch {
			case isIDField(fd):
				req.Set(fd, protoreflect.ValueOfString("hop/far/7"))
				ids = append(ids, func(m protoreflect.Message) string { return m.Get(fd).String() })
			case fd.Kind() == protoreflect.MessageKind && fd.Message().FullName() == "gridwell.v1.Tile":
				tile := req.Mutable(fd).Message()
				tfs := fd.Message().Fields()
				for k := 0; k < tfs.Len(); k++ {
					if tfd := tfs.Get(k); isIDField(tfd) {
						tile.Set(tfd, protoreflect.ValueOfString("hop/far/7"))
						ids = append(ids, func(m protoreflect.Message) string { return m.Get(fd).Message().Get(tfd).String() })
					}
				}
			}
		}
		if len(ids) < 2 {
			continue
		}
		checked++
		out := PeelRequest(hop, req.Interface()).ProtoReflect()
		for _, id := range ids {
			if got := id(out); got != "far/7" {
				t.Errorf("%s: an id came through as %q, want far/7", md.Name(), got)
			}
		}
		if got := ids[0](req); got != "hop/far/7" {
			t.Errorf("%s: input mutated", md.Name())
		}
	}
	if checked == 0 {
		t.Fatal("no multi-id request found; the walk is not reading the wire")
	}
}

// Via is OwnerNamespaceOf's answer, so behind a node an id is peeled only when
// it chains through the same connection as the routed one, and behind a leaf a
// reference stays qualified, which is what makes it a link.
func TestHopPeelsOnlyWhatChainsThroughIt(t *testing.T) {
	transit := InboundHop("n1/rtb/far/1", "n1", true)
	for id, want := range map[string]string{
		"n1/rtb/afxl3c7/~a2V5": "rtb/afxl3c7/~a2V5",
		"n1/rtb/far/2":         "rtb/far/2",
		"n1/9":                 "n1/9",
		"n1/laptop/far/2":      "n1/laptop/far/2",
		"plug/3":               "plug/3",
		"":                     "",
	} {
		if got := transit.PeelID(id); got != want {
			t.Errorf("node transit hop PeelID(%q) = %q, want %q", id, got, want)
		}
	}
	conn := InboundHop("rtb/far/1", "", true)
	if got := conn.PeelID("rtb/afxl3c7/~a2V5"); got != "afxl3c7/~a2V5" {
		t.Errorf("connection hop PeelID = %q, want afxl3c7/~a2V5", got)
	}
	if got := conn.PeelID("rtbx/far/2"); got != "rtbx/far/2" {
		t.Errorf("connection hop peeled another connection's id: %q", got)
	}

	leaf := InboundHop("n1/5", "n1", false)
	got := leaf.PeelTile(&pb.Tile{Id: "n1/6", GridId: "n1/5", ChildGridId: "n1/9", LinkTargetId: "plug/3"})
	want := &pb.Tile{Id: "6", GridId: "5", ChildGridId: "n1/9", LinkTargetId: "plug/3"}
	if !proto.Equal(got, want) {
		t.Errorf("leaf PeelTile = %+v, want %+v", got, want)
	}
	if got := transit.PeelSearchQuery("id:n1/rtb/far/2"); got != "id:rtb/far/2" {
		t.Errorf("PeelSearchQuery = %q, want id:rtb/far/2", got)
	}
	if got := transit.PeelSearchQuery("soup"); got != "soup" {
		t.Errorf("PeelSearchQuery touched free text: %q", got)
	}
}

// Every id an event names gains the hop's segment, at a leaf and in transit
// alike, and the framing a GridFramingChanged carries rides verbatim.
func TestQualifyEventIDsPrefixesEveryGridID(t *testing.T) {
	framed := &pb.Event{Payload: &pb.Event_GridFramingChanged{GridFramingChanged: &pb.GridFramingChanged{
		GridId: "1", ViewCx: 1.5, ViewCy: -2.5, ViewZoom: 3}}}
	for _, ev := range []*pb.Event{
		QualifyEventIDs("u", framed, func(t *pb.Tile) *pb.Tile { return t }),
		TransitQualifyEvent("u", framed),
	} {
		got := ev.GetGridFramingChanged()
		want := &pb.GridFramingChanged{GridId: "u/1", ViewCx: 1.5, ViewCy: -2.5, ViewZoom: 3}
		if !proto.Equal(got, want) {
			t.Errorf("framing event = %v, want %v", got, want)
		}
	}
	if framed.GetGridFramingChanged().GridId != "1" {
		t.Error("qualification mutated its input")
	}
	changed := TransitQualifyEvent("u", &pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "1"}}})
	if changed.GetGridChanged().GridId != "u/1" {
		t.Errorf("GridChanged id = %q", changed.GetGridChanged().GridId)
	}
}

// A health event gains the hop's segment on its uuid and carries every other
// field verbatim, so a far source's state reads here as it did there.
func TestQualifyEventIDsKeepsTheWholeHealth(t *testing.T) {
	h := &pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{
		PluginUuid: "p1", Healthy: true, Detail: "d", LiveUpdatesOff: "too many watches"}}}
	got := TransitQualifyEvent("u", h).GetPluginHealth()
	want := &pb.EventPluginHealth{PluginUuid: "u/p1", Healthy: true, Detail: "d", LiveUpdatesOff: "too many watches"}
	if !proto.Equal(got, want) {
		t.Errorf("health event = %v, want %v", got, want)
	}
	if h.GetPluginHealth().PluginUuid != "p1" {
		t.Error("qualification mutated its input")
	}
}

// An arm QualifyEventIDs does not name falls through unqualified, so its ids
// reach the client in the namespace's own spelling and name nothing there.
func TestQualifyEventIDsNamesEveryPayloadArm(t *testing.T) {
	arms := (&pb.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("payload").Fields()
	for i := 0; i < arms.Len(); i++ {
		fd := arms.Get(i)
		ev := &pb.Event{}
		r := ev.ProtoReflect()
		r.Set(fd, r.NewField(fd))
		if QualifyEventIDs("u", ev, func(t *pb.Tile) *pb.Tile { return t }) == ev {
			t.Errorf("payload arm %s passes through unqualified", fd.Name())
		}
	}
}
