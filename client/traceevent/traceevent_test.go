package traceevent

import (
	"strconv"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
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
		WhyNotice, WhyGridLoaded, WhyContent, WhyPreview, WhyDrag} {
		if why == "" {
			t.Fatal("a frame reason is empty")
		}
		if seen[why] {
			t.Errorf("two frame reasons read %q", why)
		}
		seen[why] = true
	}
	if s, d := FrameScheduled(WhyGhost), FrameDrawn(WhyGhost); s.Kind == d.Kind || s.Msg != d.Msg {
		t.Errorf("the schedule %+v and the draw %+v are not one pair under one reason", s, d)
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
		e := Drop(v, "t7abcde", "g7abcde")
		if e.Src != "drag" || e.Kind != "drop" || e.Msg != name {
			t.Errorf("verdict %d is %+v, want msg %q", v, e, name)
		}
	}
	if got := Drop(dragdrop.DropLink+1, "", "").Msg; got != "unnamed verdict "+strconv.Itoa(int(dragdrop.DropLink)+1) {
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
