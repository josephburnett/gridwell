package traceevent

import (
	"strconv"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
)

// Every notice the strip shows is in the trace, under the source that raised
// it, so a dump answers "what did the user see" without a second list.
func TestNoticeCarriesItsSourceAndSeverity(t *testing.T) {
	e := Notice(errsurface.Error, "grid:g7abcde", "grid unavailable")
	if e.Src != "grid:g7abcde" || e.Kind != "notice" || e.KV["sev"] != "error" {
		t.Errorf("error notice is %+v", e)
	}
	if got := Notice(errsurface.Info, "textsave", "kept").KV["sev"]; got != "info" {
		t.Errorf("info notice severity is %q", got)
	}
}

// The console tags are written with brackets, which are punctuation for a
// console line and noise in a record's src field.
func TestLogSrcIsTheTagWithoutItsBrackets(t *testing.T) {
	if got := Log("[shellstream]", "opened").Src; got != "shellstream" {
		t.Errorf("log src is %q, want the bare tag", got)
	}
}

// A key with nothing in it is absent, so a reader grepping for an id never
// matches a record that names none.
func TestAnEmptyValueIsNoKey(t *testing.T) {
	if got := kv("pane", "p1", "tile", ""); len(got) != 1 || got["pane"] != "p1" {
		t.Errorf("kv is %v, want only the pane", got)
	}
	if got := kv("tile", ""); got != nil {
		t.Errorf("kv with nothing to say is %v, want nil", got)
	}
}

// One reason per site, no two sharing a spelling: the reason is the only
// thing that says which site asked for the paint.
func TestFrameReasonsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, why := range []string{WhyAnimation, WhyTransition, WhyGhost, WhyTraceFade,
		WhyNotice, WhyGridLoaded, WhyContent, WhyPreview, WhyDrag, WhyReask} {
		if why == "" {
			t.Fatal("a frame reason is empty")
		}
		if seen[why] {
			t.Errorf("two frame reasons read %q", why)
		}
		seen[why] = true
	}
}

// One record per drawn frame, under the first reason asked: a later ask in the
// same window is counted, not named, and an ask made during the draw belongs
// to the next frame.
func TestAFrameIsOneRecordUnderItsFirstReason(t *testing.T) {
	var f FrameAsks
	if !f.Ask(WhyGhost) {
		t.Fatal("the first ask did not request a frame")
	}
	if f.Ask(WhyNotice) || f.Ask(WhyDrag) {
		t.Fatal("a later ask requested a second frame")
	}
	asks := f.Take()
	if !f.Ask(WhyTransition) {
		t.Error("an ask after the take did not request the next frame")
	}
	e := asks.Drawn(3.25)
	if e.Src != "frame" || e.Kind != "draw" || e.Msg != WhyGhost {
		t.Errorf("the frame reads %+v, want frame/draw under %q", e, WhyGhost)
	}
	if e.KV["asks"] != "3" || e.KV["ms"] != "3.2" && e.KV["ms"] != "3.3" {
		t.Errorf("the frame's kv is %v, want 3 asks and the draw's duration", e.KV)
	}
}

// Every navigation verb has a direction and a name here. A verb inserted into
// the closed set fails the count rather than reaching a dump unnamed.
func TestEveryGestureIsNamed(t *testing.T) {
	cases := []struct {
		g    nav.Gesture
		kind string
		msg  string
	}{
		{nav.Gesture{Kind: nav.GestureDescend, PaneID: "p1", Door: &pb.Tile{Id: "t7abcde"}}, "push", "descend"},
		{nav.Gesture{Kind: nav.GestureAscend, PaneID: "p1", N: 2}, "pop", "ascend 2"},
		{nav.Gesture{Kind: nav.GestureRestore, Raw: "/a"}, "restore", "restore /a"},
		{nav.Gesture{Kind: nav.GestureRestoreFromHistory, Raw: "/b"}, "restore", "restore session /b"},
		{nav.Gesture{Kind: nav.GesturePromote, OldID: "e7abcde"}, "promote", "promote ephemeral"},
		{nav.Gesture{Kind: nav.GestureEnterLevel, Door: &pb.Tile{Id: "w7abcde"}}, "push", "enter pane tile"},
		{nav.Gesture{Kind: nav.GestureLeaveLevels, Count: 3}, "pop", "leave 3 levels"},
		{nav.Gesture{Kind: nav.GestureLandLevel, TileID: "t7abcde"}, "pop", "land level"},
		{nav.Gesture{Kind: nav.GestureReEngage, TileID: "t7abcde"}, "reengage", "re-engage content"},
		{nav.Gesture{Kind: nav.GestureFollowLink, Door: &pb.Tile{Id: "l7abcde"}}, "push", "follow link"},
	}
	if len(cases) != int(nav.GestureFollowLink)+1 {
		t.Fatalf("%d gestures named, %d declared", len(cases), int(nav.GestureFollowLink)+1)
	}
	for _, c := range cases {
		e := Nav(c.g)
		if e.Src != "nav" || e.Kind != c.kind || e.Msg != c.msg {
			t.Errorf("gesture %d is %+v, want kind %q msg %q", c.g.Kind, e, c.kind, c.msg)
		}
	}
	if e := Nav(nav.Gesture{Kind: nav.GestureFollowLink + 1}); e.Kind != "unknown" {
		t.Errorf("an unnamed gesture reads %+v", e)
	}
	// The door is what a reader follows into the next grid.
	if got := Nav(cases[0].g).KV["door"]; got != "t7abcde" {
		t.Errorf("the descent names door %q", got)
	}
}

// Every drop verdict is named, so a release that did nothing the user
// expected still says which verdict it took.
func TestEveryDropVerdictIsNamed(t *testing.T) {
	want := map[dragdrop.DropAction]string{
		dragdrop.DropNavigate:       "navigate",
		dragdrop.DropNavigateSplit:  "navigate in a split",
		dragdrop.DropFocusOnly:      "focus only",
		dragdrop.DropCreateTemplate: "create",
		dragdrop.DropPanEnd:         "pan end",
		dragdrop.DropDelete:         "delete",
		dragdrop.DropRejected:       "rejected",
		dragdrop.DropMove:           "move",
		dragdrop.DropClone:          "clone",
		dragdrop.DropLink:           "link",
	}
	if len(want) != int(dragdrop.DropLink)+1 {
		t.Fatalf("%d verdicts named, %d declared", len(want), int(dragdrop.DropLink)+1)
	}
	for v, name := range want {
		e := Drop(v, "p1", "t7abcde", "g7abcde")
		if e.Src != "drag" || e.Kind != "drop" || e.Msg != name {
			t.Errorf("verdict %d is %+v, want msg %q", v, e, name)
		}
		// The pane the gesture is in, so the frames and writes around the
		// release join to it without reading the verdict back.
		if e.KV["pane"] != "p1" || e.KV["tile"] != "t7abcde" || e.KV["grid"] != "g7abcde" {
			t.Errorf("verdict %d names %v", v, e.KV)
		}
	}
	if got := Drop(dragdrop.DropLink+1, "", "", "").Msg; got != "unnamed verdict "+strconv.Itoa(int(dragdrop.DropLink)+1) {
		t.Errorf("an unnamed verdict reads %q", got)
	}
}

// A focus record names both ends, because what is worth knowing is which pane
// the next click will act in and which one it left.
func TestFocusNamesBothEnds(t *testing.T) {
	e := Focus("p1", "p2")
	if e.Src != "pane" || e.Kind != "focus" || e.KV["from"] != "p1" || e.KV["pane"] != "p2" {
		t.Errorf("focus record is %+v", e)
	}
}

// A record is read by a person and joined on by a machine, so the two halves
// stay apart: prose in Msg, ids in KV under the names a dump is grepped by.
func TestFramingSplitsProseFromIds(t *testing.T) {
	e := Framing("g7abcde", "t7abcde", 1.5, -2, 0.25)
	if e.Src != "framing" || e.Kind != "persist" {
		t.Errorf("framing is %q/%q", e.Src, e.Kind)
	}
	if e.Msg != "center 1.5,-2 zoom 0.25" {
		t.Errorf("framing msg is %q", e.Msg)
	}
	if e.KV["grid"] != "g7abcde" || e.KV["tile"] != "t7abcde" {
		t.Errorf("framing kv is %v", e.KV)
	}
	// A root grid's framing lives on its own row, so there is no tile to name
	// and the key is absent rather than empty.
	if _, ok := Framing("g7abcde", "", 0, 0, 1).KV["tile"]; ok {
		t.Error("a root framing record claims a tile")
	}
}

// A parked write names the operation and the row, which is what says whether
// the same key was overwritten or a second one joined it.
func TestTheOutboxRecordsNameTheWriteAndTheCount(t *testing.T) {
	e := OutboxPark("SetFraming", "t7abcde")
	if e.Src != "outbox" || e.Kind != "park" || e.Msg != "SetFraming" || e.KV["id"] != "t7abcde" {
		t.Errorf("park record is %+v", e)
	}
	if got := OutboxDrain(3).Msg; got != "3 owed" {
		t.Errorf("drain record reads %q", got)
	}
	if got := TextSave("t7abcde", "c7abcde", 42); got.Msg != "42 bytes" || got.KV["content"] != "c7abcde" {
		t.Errorf("text save record is %+v", got)
	}
}

// An event's kind and the entity it names are what a reader follows from the
// server's write to the client's repaint.
func TestEveryEventPayloadIsNamed(t *testing.T) {
	cases := []struct {
		ev   *pb.Event
		name string
		id   string
	}{
		{&pb.Event{Payload: &pb.Event_TileChanged{TileChanged: &pb.TileChanged{Tile: &pb.Tile{Id: "t7abcde"}}}}, "tile changed", "t7abcde"},
		{&pb.Event{Payload: &pb.Event_TileRemoved{TileRemoved: &pb.TileRemoved{TileId: "t7abcde"}}}, "tile removed", "t7abcde"},
		{&pb.Event{Payload: &pb.Event_GridChanged{GridChanged: &pb.GridChanged{GridId: "g7abcde"}}}, "grid changed", "g7abcde"},
		{&pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{PluginUuid: "n7abcde"}}}, "namespace unhealthy", "n7abcde"},
		{&pb.Event{Payload: &pb.Event_PluginHealth{PluginHealth: &pb.EventPluginHealth{PluginUuid: "n7abcde", Healthy: true}}}, "namespace healthy", "n7abcde"},
	}
	// The closed set is the proto's, so a payload the wire gains and this
	// table does not fails here instead of reaching a dump as "unknown
	// payload" — which is what 346 TileChanged records in one dump read as.
	oneof := (&pb.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("payload")
	named := map[string]bool{}
	for _, c := range cases {
		named[string(c.ev.ProtoReflect().WhichOneof(oneof).Name())] = true
	}
	if len(named) != oneof.Fields().Len() {
		t.Fatalf("%d payloads named, %d declared", len(named), oneof.Fields().Len())
	}
	for _, c := range cases {
		r, a := EventRecv(c.ev), EventApplied(c.ev)
		if r.Src != "events" || r.Kind != "recv" || r.Msg != c.name || r.KV["id"] != c.id {
			t.Errorf("recv of %v is %+v", c.ev, r)
		}
		if a.Kind != "apply" || a.Msg != c.name {
			t.Errorf("apply of %v is %+v", c.ev, a)
		}
	}
	if got := EventRecv(&pb.Event{}).Msg; got != "unknown payload" {
		t.Errorf("an unnamed payload reads %q", got)
	}
	if got := EventRefetch("g7abcde").KV["grid"]; got != "g7abcde" {
		t.Errorf("the refetch names grid %q", got)
	}
}

// A close says whether the surface was frozen or torn down: a frozen tile
// keeps its face and its session, a closed one does not, and a tile that came
// back blank is a report about exactly that difference.
func TestStreamClosesSayWhichKind(t *testing.T) {
	if got := URLClose("p1", "t7abcde", true).Msg; got != "frozen" {
		t.Errorf("a frozen url close reads %q", got)
	}
	if got := ShellClose("p1", "t7abcde", false).Msg; got != "closed" {
		t.Errorf("a shell close reads %q", got)
	}
	if got := ShellExit("p1", "t7abcde", "the shell exited", true).Msg; got != "the shell exited (session gone)" {
		t.Errorf("a session-gone exit reads %q", got)
	}
	if e := URLOpen("p1", "t7abcde"); e.Src != "url" || e.Kind != "open" || e.KV["pane"] != "p1" {
		t.Errorf("a url open is %+v", e)
	}
	if e := ShellOpen("p1", "t7abcde"); e.Src != "shell" || e.Kind != "open" || e.KV["tile"] != "t7abcde" {
		t.Errorf("a shell open is %+v", e)
	}
}

// The client's first record names the build it runs and the browser running
// it, so a dump from a stale tab or an odd browser says so on its own.
func TestTheBootRecordNamesTheBuildAndTheBrowser(t *testing.T) {
	e := Boot("8c779f0", "go1.26.6", "Mozilla/5.0 (X11)")
	if e.Src != "client" || e.Kind != tracewire.KindBoot {
		t.Errorf("the boot record is %s/%s", e.Src, e.Kind)
	}
	if e.KV["commit"] != "8c779f0" || e.KV["go"] != "go1.26.6" || e.KV["ua"] != "Mozilla/5.0 (X11)" {
		t.Errorf("the boot record's kv is %v", e.KV)
	}
	if _, ok := Boot("", "go1.26.6", "ua").KV["commit"]; ok {
		t.Error("an unstamped build claims a commit")
	}
}

// A press names the pane, the button and the modifiers held, because the
// modifier is read at the press and never again; a release names only the
// pane and the button, its verdict being the drop record's.
func TestPressAndReleaseNameThePaneAndTheButton(t *testing.T) {
	p := Press("p1", 2, Mods{Ctrl: true, Shift: true}, false)
	if p.Src != "gesture" || p.Kind != "press" || p.Msg != "right" {
		t.Errorf("a right press reads %+v", p)
	}
	if p.KV["pane"] != "p1" || p.KV["mods"] != "ctrl+shift" {
		t.Errorf("a right press's kv is %v", p.KV)
	}
	if _, ok := Press("p1", 0, Mods{}, false).KV["mods"]; ok {
		t.Error("a bare press claims a modifier")
	}
	if got := Press("p1", 1, Mods{}, true).KV["via"]; got != "live view" {
		t.Errorf("a press forwarded from a live view says via %q", got)
	}
	r := Release("p2", 0)
	if r.Src != "gesture" || r.Kind != "release" || r.Msg != "left" || r.KV["pane"] != "p2" || len(r.KV) != 1 {
		t.Errorf("a left release reads %+v", r)
	}
	if got := Release("", 7).Msg; got != "button 7" {
		t.Errorf("an unnamed button reads %q", got)
	}
}
